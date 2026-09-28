package controller

import (
	"context"
	"errors"
	"strings"
	"time"

	controllerconfig "github.com/IniZio/nexus/internal/controller/config"
	"github.com/IniZio/nexus/internal/herdragent"
)

// ThreadRef is opaque, adapter-scoped: "slack:<team>:<chan>:<thread_ts>".
type ThreadRef string

func NewThreadRef(team, channel, threadTS string) ThreadRef {
	return ThreadRef("slack:" + team + ":" + channel + ":" + threadTS)
}

func (r ThreadRef) Channel() string {
	parts := strings.SplitN(string(r), ":", 4)
	if len(parts) < 4 {
		return ""
	}
	return parts[2]
}

func (r ThreadRef) Team() string {
	parts := strings.SplitN(string(r), ":", 4)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

func (r ThreadRef) TS() string {
	parts := strings.SplitN(string(r), ":", 4)
	if len(parts) < 4 {
		return ""
	}
	return parts[3]
}

type Status string

const (
	StatusStarting      Status = "starting"
	StatusWorking       Status = "working"
	StatusIdle          Status = "idle"
	StatusWaitingOnUser Status = "waiting_on_user"
	StatusClosed        Status = "closed"
	StatusFailed        Status = "failed"
	StatusPaused        Status = "paused"
	// StatusStopped: sandbox stopped (not removed) after long idle; a reply
	// starts it again. Distinct from StatusClosed, which is terminal.
	StatusStopped Status = "stopped"
)

var validTransitions = map[Status]map[Status]bool{
	StatusStarting: {
		StatusWorking: true,
		StatusFailed:  true,
		StatusClosed:  true,
	},
	StatusWorking: {
		StatusIdle:          true,
		StatusWaitingOnUser: true,
		StatusFailed:        true,
		StatusClosed:        true,
	},
	StatusWaitingOnUser: {
		StatusWorking: true,
		StatusIdle:    true,
		StatusPaused:  true,
		StatusFailed:  true,
		StatusClosed:  true,
	},
	StatusIdle: {
		StatusWorking: true,
		StatusPaused:  true,
		StatusFailed:  true,
		StatusClosed:  true,
	},
	StatusPaused: {
		StatusWorking: true,
		StatusStopped: true,
		StatusFailed:  true,
		StatusClosed:  true,
	},
	StatusStopped: {
		StatusWorking: true,
		StatusFailed:  true,
		StatusClosed:  true,
	},
	StatusFailed: {
		StatusStarting: true,
		StatusWorking:  true,
		StatusClosed:   true,
	},
	StatusClosed: {},
}

func ValidTransition(from, to Status) bool {
	if from == to {
		return false
	}
	return validTransitions[from][to]
}

type Task struct {
	ThreadRef      ThreadRef
	Project        string
	Owner          string
	LastAuthor     string
	SandboxID      string
	HerdrAgent     string
	AgentSessionID string
	Status         Status
	TurnID         string
	StateChangeSeq uint64
	CreatedAt      time.Time
	LastActivityAt time.Time
	PreviewSlot    string
}

var (
	ErrNotFound          = errors.New("controller: task not found")
	ErrConflict          = errors.New("controller: status compare-and-set conflict")
	ErrInvalidTransition = errors.New("controller: invalid status transition")
	ErrNotLinked         = errors.New("controller: user has not linked the integration")
	ErrNoProject         = controllerconfig.ErrNoProject
	ErrNotImplemented    = errors.New("controller: not implemented")
	ErrNotOwner          = errors.New("controller: thread belongs to another user")
	ErrTurnTimeout       = errors.New("controller: turn deadline exceeded")
)

type EventKind string

const (
	EventMention      EventKind = "mention"
	EventReply        EventKind = "reply"
	EventSlashCommand EventKind = "slash_command"
)

type Event struct {
	Kind      EventKind
	ThreadRef ThreadRef
	User      string
	Text      string
}

type Handler func(ctx context.Context, ev Event) error

// ChatAdapter abstracts the chat platform (Slack, etc.).
type ChatAdapter interface {
	Run(ctx context.Context, h Handler) error
	Post(ctx context.Context, ref ThreadRef, text string) error
	PostEphemeral(ctx context.Context, ref ThreadRef, user, text string) error
	PostFile(ctx context.Context, ref ThreadRef, name string, content []byte) error
	React(ctx context.Context, ref ThreadRef, emoji string) error
	Mention(user string) string
}

// TaskStore persists Tasks; Transition is CAS on Status (!ValidTransition→ErrInvalidTransition, stored≠from→ErrConflict); ListIdle filters idle/waiting_on_user/paused with LastActivityAt < before.
type TaskStore interface {
	Get(ctx context.Context, ref ThreadRef) (Task, error)
	Upsert(ctx context.Context, t Task) error
	Transition(ctx context.Context, ref ThreadRef, from, to Status, seq uint64) error
	ListIdle(ctx context.Context, before time.Time) ([]Task, error)
	TouchActivity(ctx context.Context, ref ThreadRef, author string) error
	// ResetStuck transitions all starting/working tasks to failed; called on startup to clear
	// tasks that were interrupted mid-turn. Bypasses state-machine checks.
	ResetStuck(ctx context.Context) error
}

type AgentInput struct {
	Text string
	Key  string
}

// AgentBackend manages agent lifecycle and communication.
type AgentBackend interface {
	Provision(ctx context.Context, project string, ref ThreadRef, principal string) (sandboxID, agentRef string, err error)
	Prompt(ctx context.Context, agentRef, text string) (turnID string, err error)
	Observe(ctx context.Context, agentRef string, wait bool) (herdragent.State, error)
	Answer(ctx context.Context, agentRef string, in AgentInput) error
	ReadAnswer(ctx context.Context, agentRef, turnID string) (string, error)
	// Restart re-launches the guest agent after its sandbox was stopped and
	// started again, waits for readiness, and returns the (possibly new) agentRef.
	Restart(ctx context.Context, sandboxID, agentRef string) (newAgentRef string, err error)
	Teardown(ctx context.Context, sandboxID string) error
}

// SandboxLifecycle controls sandbox pause/resume/stop/start.
type SandboxLifecycle interface {
	Pause(ctx context.Context, sandboxID string) error
	Resume(ctx context.Context, sandboxID string) error
	Stop(ctx context.Context, sandboxID string) error
	Start(ctx context.Context, sandboxID string) error
}

// Linker maps user↔integration; Require returns ErrNotLinked if the user has not linked.
type Linker interface {
	Require(ctx context.Context, user string) error
	StartLink(ctx context.Context, user, integration string) (instructions string, err error)
}

// ProjectResolver maps channel→project name; Resolve returns ErrNoProject if none configured.
type ProjectResolver interface {
	Resolve(ctx context.Context, channel string) (project string, err error)
}

// Flows is the seam the Router dispatches into.
type Flows interface {
	OnMention(ctx context.Context, t Task, ev Event) error   // turn.go
	OnReply(ctx context.Context, t Task, ev Event) error     // blocked.go
	OnTick(ctx context.Context, t Task, now time.Time) error // idle.go
}

type Deps struct {
	Chat      ChatAdapter
	Store     TaskStore
	Backend   AgentBackend
	Lifecycle SandboxLifecycle
	Linker    Linker
	Projects  ProjectResolver
	// IdleFor returns the idle thresholds for a channel; nil means DefaultIdle.
	IdleFor func(channel string) IdleThresholds
	// PermMode returns the claude permission mode for a channel, or "" for the
	// backend default. Nil means always use the backend default.
	PermMode func(channel string) string
	// Model returns the claude model for a channel, or "" for the backend default.
	// Nil means always use the backend default. Use this to override the default
	// per channel (e.g. to use sonnet on channels where auto mode is required,
	// since haiku silently falls back from auto to default).
	Model func(channel string) string
	// TurnTimeout bounds one observe loop; zero means DefaultTurnTimeout.
	TurnTimeout time.Duration
	// Sleep is the seam used by runObserveLoop for idle settle-rechecks; nil
	// defaults to a real context-aware sleep.
	Sleep func(ctx context.Context, d time.Duration) error
}

// IdleThresholds: pause after Pause of inactivity, stop after Stop.
type IdleThresholds struct {
	Pause time.Duration
	Stop  time.Duration
}

var DefaultIdle = IdleThresholds{Pause: 30 * time.Minute, Stop: 4 * time.Hour}

const DefaultTurnTimeout = 30 * time.Minute

// RawAnswerPrefix is prepended by AgentBackend.ReadAnswer when both transcript
// and pane parsing fail; postAnswer uploads the payload as a file instead of
// posting it inline.
const RawAnswerPrefix = "\x00raw\x00"

type permModeKey struct{}

// WithPermMode returns a child context carrying a per-provision permission mode.
func WithPermMode(ctx context.Context, mode string) context.Context {
	return context.WithValue(ctx, permModeKey{}, mode)
}

// PermModeFromCtx returns the permission mode stored in ctx, or "" when not set.
func PermModeFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(permModeKey{}).(string)
	return v
}

type modelKey struct{}

// WithModel returns a child context carrying a per-provision model override.
func WithModel(ctx context.Context, model string) context.Context {
	return context.WithValue(ctx, modelKey{}, model)
}

// ModelFromCtx returns the model override stored in ctx, or "" when not set.
func ModelFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(modelKey{}).(string)
	return v
}

func (c *Controller) idleFor(channel string) IdleThresholds {
	if c.deps.IdleFor == nil {
		return DefaultIdle
	}
	return c.deps.IdleFor(channel)
}

func (c *Controller) turnTimeout() time.Duration {
	if c.deps.TurnTimeout <= 0 {
		return DefaultTurnTimeout
	}
	return c.deps.TurnTimeout
}

type Controller struct{ deps Deps }

// withChannelAgentOpts enriches ctx with the per-channel permission mode and
// model override stored in Deps, matching the logic in OnMention/Provision.
func (c *Controller) withChannelAgentOpts(ctx context.Context, ch string) context.Context {
	if c.deps.PermMode != nil {
		if pm := c.deps.PermMode(ch); pm != "" {
			ctx = WithPermMode(ctx, pm)
		}
	}
	if c.deps.Model != nil {
		if m := c.deps.Model(ch); m != "" {
			ctx = WithModel(ctx, m)
		}
	}
	return ctx
}

func New(d Deps) *Controller { return &Controller{deps: d} }

var _ Flows = (*Controller)(nil)
