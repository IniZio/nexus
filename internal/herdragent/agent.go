package herdragent

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IniZio/nexus/internal/herdrout"
)

type Status string

const (
	StatusIdle    Status = "idle"
	StatusWorking Status = "working"
	StatusBlocked Status = "blocked"
	StatusDone    Status = "done"
	StatusUnknown Status = "unknown"
)

type State struct {
	Status   Status `json:"agent_status"`
	Seq      uint64 `json:"state_change_seq,omitempty"`
	Agent    string `json:"agent,omitempty"`
	PaneID   string `json:"pane_id,omitempty"`
	Question string `json:"question,omitempty"`
	Settled  bool   `json:"settled"`
	Reason   string `json:"agent_state_reason,omitempty"`
}

type Runner func(ctx context.Context, argv ...string) (string, error)

type Option func(*Client)

func WithSettle(d time.Duration) Option {
	return func(c *Client) { c.settleDur = d }
}

func WithUnknownWindow(d time.Duration) Option {
	return func(c *Client) { c.unknownWindow = d }
}

func WithSleep(f func(context.Context, time.Duration) error) Option {
	return func(c *Client) { c.sleep = f }
}

type Client struct {
	run           Runner
	settleDur     time.Duration
	unknownWindow time.Duration
	sleep         func(context.Context, time.Duration) error
}

func New(run Runner, opts ...Option) *Client {
	c := &Client{
		run:           run,
		settleDur:     1500 * time.Millisecond,
		unknownWindow: 15 * time.Second,
		sleep: func(ctx context.Context, d time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(d):
				return nil
			}
		},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func parseAgentGet(out string) State {
	type inner struct {
		Result struct {
			Agent struct {
				Agent  string `json:"agent"`
				Status string `json:"agent_status"`
				PaneID string `json:"pane_id"`
				Seq    uint64 `json:"state_change_seq"`
			} `json:"agent"`
		} `json:"result"`
	}
	var r inner
	if json.Unmarshal([]byte(out), &r) != nil {
		for line := range strings.SplitSeq(out, "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "{") && json.Unmarshal([]byte(line), &r) == nil {
				break
			}
		}
	}
	if r.Result.Agent.Status == "" {
		return State{Status: StatusUnknown, Reason: "parse_error"}
	}
	st := State{
		Status: Status(r.Result.Agent.Status),
		Seq:    r.Result.Agent.Seq,
		Agent:  r.Result.Agent.Agent,
		PaneID: r.Result.Agent.PaneID,
	}
	if st.Status == StatusUnknown {
		st.Reason = "undetected"
	}
	return st
}

func (c *Client) get(ctx context.Context, target string) State {
	out, err := c.run(ctx, "agent", "get", target)
	if err != nil {
		code, _, ok := herdrout.ParseHerdrErrorCode(out)
		if ok {
			return State{Status: StatusUnknown, Reason: code}
		}
		return State{Status: StatusUnknown, Reason: "herdr_unavailable"}
	}
	return parseAgentGet(out)
}

// isFullWidthRule returns true when s is a line consisting only of ─ (U+2500)
// characters with at least 20 of them.
func isFullWidthRule(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	count := 0
	for _, r := range s {
		if r != '─' {
			return false
		}
		count++
	}
	return count >= 20
}

// linesAfterLastRule returns the lines that follow the last full-width ─ rule.
// Falls back to the last 15 non-empty lines when no rule is found.
func linesAfterLastRule(screen string) []string {
	lines := strings.Split(screen, "\n")
	lastIdx := -1
	for i, l := range lines {
		if isFullWidthRule(l) {
			lastIdx = i
		}
	}
	if lastIdx >= 0 {
		return lines[lastIdx+1:]
	}
	var nonempty []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			nonempty = append(nonempty, l)
		}
	}
	if len(nonempty) > 15 {
		nonempty = nonempty[len(nonempty)-15:]
	}
	return nonempty
}

var dialogCursorRe = regexp.MustCompile(`(?m)^\s*❯\s*\d+\.\s`)

var dialogCues = []string{
	"do you want to proceed?",
	"requires approval",
	"esc to cancel",
	"tab to amend",
}

// LooksLikeDialog returns true when screen contains a Claude Code permission
// dialog. It inspects only the text after the last full-width horizontal rule
// (or the last ~15 non-empty lines if no rule is present) and requires both a
// known cue phrase and a numbered cursor option line.
func LooksLikeDialog(screen string) bool {
	after := linesAfterLastRule(screen)
	text := strings.Join(after, "\n")
	lower := strings.ToLower(text)
	for _, cue := range dialogCues {
		if strings.Contains(lower, cue) && dialogCursorRe.MatchString(text) {
			return true
		}
	}
	return false
}

func (c *Client) checkScreenDialog(ctx context.Context, target string, st State) (State, bool) {
	out, err := c.run(ctx, "agent", "read", target, "--source", "recent-unwrapped", "--lines", "40")
	if err != nil || !LooksLikeDialog(out) {
		return st, false
	}
	promoted := st
	promoted.Status = StatusBlocked
	promoted.Question = tailBounded(ExtractQuestion(out), 4000)
	promoted.Reason = "screen_dialog"
	promoted.Settled = true
	return promoted, true
}

func (c *Client) Observe(ctx context.Context, target string, wait time.Duration) State {
	if wait > 0 {
		_, waitErr := c.run(ctx, "agent", "wait", target,
			"--until", "idle",
			"--until", "done",
			"--until", "blocked",
			"--timeout", fmt.Sprintf("%d", wait.Milliseconds()))
		if waitErr != nil && ctx.Err() != nil {
			return State{Status: StatusUnknown, Reason: "timeout"}
		}
	}

	st := c.get(ctx, target)
	if st.Status == StatusUnknown {
		return st
	}
	if st.Status == StatusWorking {
		st.Settled = true
		return st
	}
	if st.Status == StatusBlocked {
		return c.readQuestion(ctx, target, st)
	}
	return c.settlePoll(ctx, target, st)
}

func tailBounded(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[len(s)-max:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s
}

// ExtractQuestion strips noise from a blocked-dialog pane dump:
// removes box-drawing characters, drops ⎿ hook-error lines and
// "Failed with non-blocking status code" / "hook error" lines,
// and returns the last 30 non-empty lines.
func ExtractQuestion(screen string) string {
	var kept []string
	for _, line := range strings.Split(screen, "\n") {
		// Drop hook-error lines.
		if strings.Contains(line, "⎿") ||
			strings.Contains(line, "hook error") ||
			strings.Contains(line, "Failed with non-blocking status code") {
			continue
		}
		// Strip box-drawing characters (U+2500–U+257F range).
		var b strings.Builder
		for _, r := range line {
			if r >= 0x2500 && r <= 0x257F {
				continue
			}
			b.WriteRune(r)
		}
		cleaned := strings.TrimRight(b.String(), " \t")
		if cleaned != "" {
			kept = append(kept, cleaned)
		}
	}
	if len(kept) > 30 {
		kept = kept[len(kept)-30:]
	}
	return strings.Join(kept, "\n")
}

func (c *Client) readQuestion(ctx context.Context, target string, st State) State {
	out, err := c.run(ctx, "agent", "read", target, "--source", "recent-unwrapped", "--lines", "40")
	if err == nil {
		st.Question = tailBounded(ExtractQuestion(out), 4000)
	}
	st.Settled = true
	return st
}

func (c *Client) settlePoll(ctx context.Context, target string, st State) State {
	const maxRepolls = 3
	for range maxRepolls {
		prevSeq := st.Seq
		if err := c.sleep(ctx, c.settleDur); err != nil {
			st.Settled = false
			return st
		}
		next := c.get(ctx, target)
		if next.Status == StatusUnknown {
			return next
		}
		if next.Seq == prevSeq {
			if promoted, ok := c.checkScreenDialog(ctx, target, next); ok {
				return promoted
			}
			next.Settled = true
			return next
		}
		st = next
		if st.Status == StatusBlocked {
			return c.readQuestion(ctx, target, st)
		}
		if st.Status == StatusWorking {
			st.Settled = true
			return st
		}
		if st.Status == StatusIdle || st.Status == StatusDone {
			if promoted, ok := c.checkScreenDialog(ctx, target, st); ok {
				return promoted
			}
		}
	}
	st.Settled = false
	return st
}

func isHardUnknown(reason string) bool {
	return reason == "herdr_unavailable" || reason == "parse_error"
}

func (c *Client) Ready(ctx context.Context, target string, baseline State, timeout time.Duration) (State, bool) {
	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var st State
	hardStreak, softSleeps := 0, 0
	for {
		st = c.get(ctx2, target)
		if st.Status == StatusIdle && st.Seq > baseline.Seq {
			st.Settled = true
			return st, true
		}
		if st.Status == StatusUnknown {
			if isHardUnknown(st.Reason) {
				hardStreak++
				softSleeps = 0
				if hardStreak >= 3 {
					return st, false
				}
			} else {
				softSleeps++
				hardStreak = 0
				if time.Duration(softSleeps)*c.settleDur >= c.unknownWindow {
					return st, false
				}
			}
		} else {
			hardStreak = 0
			softSleeps = 0
		}
		if err := c.sleep(ctx2, c.settleDur); err != nil {
			return st, false
		}
	}
}
