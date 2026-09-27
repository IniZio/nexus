package controller

import (
	"context"
	"fmt"
	"time"

	"github.com/IniZio/nexus/internal/herdragent"
)

// WaitNotBlockedTimeout is the deadline for waitNotBlocked; override in tests.
var WaitNotBlockedTimeout = 10 * time.Second

// waitNotBlocked polls Backend.Observe until the agent leaves StatusBlocked or
// the deadline fires. Returns the backend error on failure or a deadline error
// if the agent is still blocked when the timeout fires.
func (c *Controller) waitNotBlocked(ctx context.Context, agentRef string) error {
	deadline, cancel := context.WithTimeout(ctx, WaitNotBlockedTimeout)
	defer cancel()
	for {
		st, err := c.deps.Backend.Observe(deadline, agentRef, false)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("waitNotBlocked: %w", err)
		}
		if st.Status != herdragent.StatusBlocked {
			return nil
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("agent still blocked after answer: %w", context.DeadlineExceeded)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (c *Controller) OnReply(ctx context.Context, t Task, ev Event) error {
	if err := c.deps.Linker.Require(ctx, ev.User); err != nil {
		_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
		c.postFailureReason(ctx, t.ThreadRef, err)
		return err
	}
	if ev.User != t.Owner {
		msg := fmt.Sprintf("only %s can drive this thread", c.deps.Chat.Mention(t.Owner))
		_ = c.deps.Chat.Post(ctx, t.ThreadRef, msg)
		return ErrNotOwner
	}

	switch t.Status {
	case StatusWaitingOnUser:
		return c.handleBlockedReply(ctx, t, ev)
	case StatusIdle:
		return c.handleIdleReply(ctx, t, ev)
	case StatusPaused:
		return c.handlePausedReply(ctx, t, ev)
	case StatusStopped:
		return c.handleStoppedReply(ctx, t, ev)
	case StatusWorking:
		_ = c.deps.Chat.React(ctx, t.ThreadRef, "hourglass_flowing_sand")
		return nil
	default:
		return nil
	}
}

func (c *Controller) handleIdleReply(ctx context.Context, t Task, ev Event) error {
	nextSeq := t.StateChangeSeq + 1
	if err := c.deps.Store.Transition(ctx, t.ThreadRef, StatusIdle, StatusWorking, nextSeq); err != nil {
		return err
	}
	t.Status = StatusWorking
	t.StateChangeSeq = nextSeq
	if err := c.deps.Backend.Prompt(ctx, t.HerdrAgent, ev.Text); err != nil {
		return err
	}
	return c.runObserveLoop(ctx, t)
}

func (c *Controller) handleBlocked(ctx context.Context, t Task, st herdragent.State) error {
	seen := map[string]bool{}
	var tags string
	for _, u := range []string{t.Owner, t.LastAuthor} {
		if u != "" && !seen[u] {
			seen[u] = true
			tags += c.deps.Chat.Mention(u) + " "
		}
	}
	msg := fmt.Sprintf("%s%s", tags, st.Question)
	if err := c.deps.Chat.Post(ctx, t.ThreadRef, msg); err != nil {
		return err
	}
	return c.deps.Store.Transition(ctx, t.ThreadRef, StatusWorking, StatusWaitingOnUser, t.StateChangeSeq+1)
}

func (c *Controller) handleBlockedReply(ctx context.Context, t Task, ev Event) error {
	var in AgentInput
	if len(ev.Text) == 1 && ev.Text[0] >= '0' && ev.Text[0] <= '9' {
		in = AgentInput{Key: ev.Text}
	} else {
		in = AgentInput{Text: ev.Text}
	}
	if err := c.deps.Backend.Answer(ctx, t.HerdrAgent, in); err != nil {
		return err
	}
	if err := c.waitNotBlocked(ctx, t.HerdrAgent); err != nil {
		_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
		_ = c.deps.Chat.Post(ctx, t.ThreadRef, "agent error after answer: "+err.Error())
		return err
	}
	nextSeq := t.StateChangeSeq + 1
	if err := c.deps.Store.Transition(ctx, t.ThreadRef, StatusWaitingOnUser, StatusWorking, nextSeq); err != nil {
		return err
	}
	t.Status = StatusWorking
	t.StateChangeSeq = nextSeq
	return c.runObserveLoop(ctx, t)
}
