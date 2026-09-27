package controller

import (
	"context"
	"fmt"
	"time"
)

func (c *Controller) OnTick(ctx context.Context, t Task, now time.Time) error {
	idleDur := now.Sub(t.LastActivityAt)
	thresh := c.idleFor(t.ThreadRef.Channel())

	if (t.Status == StatusIdle || t.Status == StatusWaitingOnUser) &&
		thresh.Pause > 0 && idleDur >= thresh.Pause {
		if err := c.deps.Lifecycle.Pause(ctx, t.SandboxID); err != nil {
			return err
		}
		return c.deps.Store.Transition(ctx, t.ThreadRef, t.Status, StatusPaused, t.StateChangeSeq+1)
	}

	if t.Status == StatusPaused && thresh.Stop > 0 && idleDur >= thresh.Stop {
		if err := c.deps.Lifecycle.Stop(ctx, t.SandboxID); err != nil {
			return err
		}
		return c.deps.Store.Transition(ctx, t.ThreadRef, StatusPaused, StatusStopped, t.StateChangeSeq+1)
	}

	return nil
}

func (c *Controller) handlePausedReply(ctx context.Context, t Task, ev Event) error {
	if err := c.deps.Lifecycle.Resume(ctx, t.SandboxID); err != nil {
		_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
		_ = c.deps.Chat.Post(ctx, t.ThreadRef, fmt.Sprintf("could not resume sandbox: %v", err))
		return err
	}
	nextSeq := t.StateChangeSeq + 1
	if err := c.deps.Store.Transition(ctx, t.ThreadRef, StatusPaused, StatusWorking, nextSeq); err != nil {
		_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
		_ = c.deps.Chat.Post(ctx, t.ThreadRef, fmt.Sprintf("could not resume sandbox: %v", err))
		return err
	}
	t.Status = StatusWorking
	t.StateChangeSeq = nextSeq
	if err := c.deps.Backend.Prompt(ctx, t.HerdrAgent, ev.Text); err != nil {
		return err
	}
	return c.runObserveLoop(ctx, t)
}

func (c *Controller) handleStoppedReply(ctx context.Context, t Task, ev Event) error {
	errReact := func(err error) error {
		_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
		_ = c.deps.Chat.Post(ctx, t.ThreadRef, fmt.Sprintf("could not restart sandbox: %v", err))
		return err
	}

	if err := c.deps.Lifecycle.Start(ctx, t.SandboxID); err != nil {
		return errReact(err)
	}
	newRef, err := c.deps.Backend.Restart(ctx, t.SandboxID, t.HerdrAgent)
	if err != nil {
		return errReact(err)
	}
	if newRef != t.HerdrAgent {
		t.HerdrAgent = newRef
		if err := c.deps.Store.Upsert(ctx, t); err != nil {
			return errReact(err)
		}
	}
	nextSeq := t.StateChangeSeq + 1
	if err := c.deps.Store.Transition(ctx, t.ThreadRef, StatusStopped, StatusWorking, nextSeq); err != nil {
		return errReact(err)
	}
	t.Status = StatusWorking
	t.StateChangeSeq = nextSeq
	if err := c.deps.Backend.Prompt(ctx, t.HerdrAgent, ev.Text); err != nil {
		return err
	}
	return c.runObserveLoop(ctx, t)
}
