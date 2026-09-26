package controller

import (
	"context"
	"fmt"

	"github.com/IniZio/nexus/internal/herdragent"
)

func (c *Controller) OnReply(ctx context.Context, t Task, ev Event) error {
	switch t.Status {
	case StatusWaitingOnUser:
		return c.handleBlockedReply(ctx, t, ev)
	case StatusPaused:
		return c.handlePausedReply(ctx, t, ev)
	default:
		return nil
	}
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
	nextSeq := t.StateChangeSeq + 1
	if err := c.deps.Store.Transition(ctx, t.ThreadRef, StatusWaitingOnUser, StatusWorking, nextSeq); err != nil {
		return err
	}
	t.Status = StatusWorking
	t.StateChangeSeq = nextSeq
	return c.runObserveLoop(ctx, t)
}
