package controller

import (
	"context"
	"time"
)

func (c *Controller) OnTick(ctx context.Context, t Task, now time.Time) error {
	idleDur := now.Sub(t.LastActivityAt)

	if t.Status == StatusIdle && c.deps.IdlePause > 0 && idleDur >= c.deps.IdlePause {
		if err := c.deps.Lifecycle.Pause(ctx, t.SandboxID); err != nil {
			return err
		}
		return c.deps.Store.Transition(ctx, t.ThreadRef, StatusIdle, StatusPaused, t.StateChangeSeq+1)
	}

	if t.Status == StatusPaused && c.deps.IdleStop > 0 && idleDur >= c.deps.IdleStop {
		if err := c.deps.Lifecycle.Stop(ctx, t.SandboxID); err != nil {
			return err
		}
		return c.deps.Store.Transition(ctx, t.ThreadRef, StatusPaused, StatusClosed, t.StateChangeSeq+1)
	}

	return nil
}

func (c *Controller) handlePausedReply(ctx context.Context, t Task, ev Event) error {
	if err := c.deps.Lifecycle.Resume(ctx, t.SandboxID); err != nil {
		return err
	}
	nextSeq := t.StateChangeSeq + 1
	if err := c.deps.Store.Transition(ctx, t.ThreadRef, StatusPaused, StatusWorking, nextSeq); err != nil {
		return err
	}
	t.Status = StatusWorking
	t.StateChangeSeq = nextSeq
	if err := c.deps.Backend.Prompt(ctx, t.HerdrAgent, ev.Text); err != nil {
		return err
	}
	return c.runObserveLoop(ctx, t)
}
