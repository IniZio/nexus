package controller

import (
	"context"
	"errors"
	"strings"
	"time"

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

type Status string

const (
	StatusStarting      Status = "starting"
	StatusWorking       Status = "working"
	StatusIdle          Status = "idle"
	StatusWaitingOnUser Status = "waiting_on_user"
	StatusClosed        Status = "closed"
	StatusFailed        Status = "failed"
	StatusPaused        Status = "paused"
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
		StatusFailed:  true,
		StatusClosed:  true,
	},
	StatusFailed: {
		StatusStarting: true,
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
	ErrNoProject         = errors.New("controller: channel has no project")
	ErrNotImplemented    = errors.New("controller: not implemented")
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
}

type AgentInput struct {
	Text string
	Key  string
}

// AgentBackend manages agent lifecycle and communication.
type AgentBackend interface {
	Provision(ctx context.Context, project string, ref ThreadRef, principal string) (sandboxID, agentRef string, err error)
	Prompt(ctx context.Context, agentRef, text string) error
	Observe(ctx context.Context, agentRef string, wait bool) (herdragent.State, error)
	Answer(ctx context.Context, agentRef string, in AgentInput) error
	ReadAnswer(ctx context.Context, agentRef string) (string, error)
	Teardown(ctx context.Context, sandboxID string) error
}

// SandboxLifecycle controls sandbox pause/resume/stop.
type SandboxLifecycle interface {
	Pause(ctx context.Context, sandboxID string) error
	Resume(ctx context.Context, sandboxID string) error
	Stop(ctx context.Context, sandboxID string) error
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
	OnMention(ctx context.Context, t Task, ev Event) error  // turn.go
	OnReply(ctx context.Context, t Task, ev Event) error    // blocked.go
	OnTick(ctx context.Context, t Task, now time.Time) error // idle.go
}

type Deps struct {
	Chat      ChatAdapter
	Store     TaskStore
	Backend   AgentBackend
	Lifecycle SandboxLifecycle
	Linker    Linker
	Projects  ProjectResolver
	IdlePause time.Duration
	IdleStop  time.Duration
}

type Controller struct{ deps Deps }

func New(d Deps) *Controller { return &Controller{deps: d} }

var _ Flows = (*Controller)(nil)
