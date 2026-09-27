package controller

import (
	"context"
	"errors"
	"time"

	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/herdragent"
)

const answerFileSizeLimit = 11 * 1024

func (c *Controller) OnMention(ctx context.Context, t Task, ev Event) error {
	if err := c.deps.Linker.Require(ctx, ev.User); err != nil {
		_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
		return err
	}

	project, err := c.deps.Projects.Resolve(ctx, t.ThreadRef.Channel())
	if err != nil {
		_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
		return err
	}

	if t.SandboxID == "" {
		principal := vault.SlackPrincipal(t.ThreadRef.Team(), ev.User)
		sbID, agRef, provErr := c.deps.Backend.Provision(ctx, project, t.ThreadRef, principal)
		if provErr != nil {
			_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
			_ = c.deps.Chat.Post(ctx, t.ThreadRef, "provision error: "+provErr.Error())
			return provErr
		}
		t.SandboxID = sbID
		t.HerdrAgent = agRef
		t.Project = project
		if uErr := c.deps.Store.Upsert(ctx, t); uErr != nil {
			return uErr
		}
	}

	if t.Status != StatusWorking {
		nextSeq := t.StateChangeSeq + 1
		if tErr := c.deps.Store.Transition(ctx, t.ThreadRef, t.Status, StatusWorking, nextSeq); tErr != nil && !errors.Is(tErr, ErrConflict) {
			return tErr
		}
		t.Status = StatusWorking
		t.StateChangeSeq = nextSeq
	}

	if err := c.deps.Backend.Prompt(ctx, t.HerdrAgent, ev.Text); err != nil {
		return err
	}

	return c.runObserveLoop(ctx, t)
}

func (c *Controller) runObserveLoop(ctx context.Context, t Task) error {
	turnCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	for {
		st, err := c.deps.Backend.Observe(turnCtx, t.HerdrAgent, true)
		if err != nil {
			return err
		}
		if !st.Settled {
			continue
		}
		if repoll, rErr := c.deps.Backend.Observe(turnCtx, t.HerdrAgent, false); rErr == nil && repoll.Settled && repoll.Seq != st.Seq {
			st = repoll
		}

		switch st.Status {
		case herdragent.StatusDone, herdragent.StatusIdle:
			if st.Status == herdragent.StatusDone {
				answer, rErr := c.deps.Backend.ReadAnswer(ctx, t.HerdrAgent)
				if rErr != nil {
					return rErr
				}
				if pErr := c.postAnswer(ctx, t, answer); pErr != nil {
					return pErr
				}
			}
			_ = c.deps.Store.Transition(ctx, t.ThreadRef, StatusWorking, StatusIdle, t.StateChangeSeq+1)
			_ = c.deps.Chat.React(ctx, t.ThreadRef, "white_check_mark")
			return nil
		case herdragent.StatusBlocked:
			return c.handleBlocked(ctx, t, st)
		}
	}
}

func (c *Controller) postAnswer(ctx context.Context, t Task, answer string) error {
	if len(answer) > answerFileSizeLimit {
		return c.deps.Chat.PostFile(ctx, t.ThreadRef, "answer.txt", []byte(answer))
	}
	return c.deps.Chat.Post(ctx, t.ThreadRef, answer)
}
