package herdragent

import (
	"context"
	"encoding/json"
	"fmt"
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

func (c *Client) readQuestion(ctx context.Context, target string, st State) State {
	out, err := c.run(ctx, "agent", "read", target, "--source", "recent-unwrapped", "--lines", "40")
	if err == nil {
		st.Question = tailBounded(strings.TrimSpace(out), 4000)
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
