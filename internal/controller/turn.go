package controller

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/vault"
	"github.com/IniZio/nexus/internal/herdragent"
)

const answerFileSizeLimit = 11 * 1024

const idleSettlePause = 1500 * time.Millisecond

func (c *Controller) sleep(ctx context.Context, d time.Duration) error {
	if c.deps.Sleep != nil {
		return c.deps.Sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Controller) OnMention(ctx context.Context, t Task, ev Event) error {
	if err := c.deps.Linker.Require(ctx, ev.User); err != nil {
		_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
		c.postFailureReason(ctx, t.ThreadRef, err)
		return err
	}

	project, err := c.deps.Projects.Resolve(ctx, t.ThreadRef.Channel())
	if err != nil {
		_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
		c.postFailureReason(ctx, t.ThreadRef, err)
		return err
	}

	if t.SandboxID == "" {
		_ = c.deps.Chat.React(ctx, t.ThreadRef, "eyes")
		_ = c.deps.Chat.Post(ctx, t.ThreadRef, "provisioning a sandbox — first run can take several minutes")
		principal := vault.SlackPrincipal(t.ThreadRef.Team(), ev.User)
		if c.deps.PermMode != nil {
			if pm := c.deps.PermMode(t.ThreadRef.Channel()); pm != "" {
				ctx = WithPermMode(ctx, pm)
			}
		}
		if c.deps.Model != nil {
			if m := c.deps.Model(t.ThreadRef.Channel()); m != "" {
				ctx = WithModel(ctx, m)
			}
		}
		sbID, agRef, provErr := c.deps.Backend.Provision(ctx, project, t.ThreadRef, principal)
		if provErr != nil {
			if sbID != "" {
				_ = c.deps.Backend.Teardown(ctx, sbID)
			}
			_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
			_ = c.deps.Store.Transition(ctx, t.ThreadRef, t.Status, StatusFailed, t.StateChangeSeq+1)
			c.postFailureReason(ctx, t.ThreadRef, provErr)
			return provErr
		}
		t.SandboxID = sbID
		t.HerdrAgent = agRef
		t.Project = project
		t.Status = StatusStarting
		if uErr := c.deps.Store.Upsert(ctx, t); uErr != nil {
			return uErr
		}
	} else if t.Owner != "" && ev.User != t.Owner {
		_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
		_ = c.deps.Chat.Post(ctx, t.ThreadRef, "this thread belongs to "+c.deps.Chat.Mention(t.Owner))
		return ErrNotOwner
	}

	if t.Status != StatusWorking {
		nextSeq := t.StateChangeSeq + 1
		if tErr := c.deps.Store.Transition(ctx, t.ThreadRef, t.Status, StatusWorking, nextSeq); tErr != nil && !errors.Is(tErr, ErrConflict) {
			return tErr
		}
		t.Status = StatusWorking
		t.StateChangeSeq = nextSeq
	}

	turnID, err := c.deps.Backend.Prompt(ctx, t.HerdrAgent, ev.Text)
	if err != nil {
		_ = c.deps.Store.Transition(ctx, t.ThreadRef, StatusWorking, StatusFailed, t.StateChangeSeq+1)
		c.postFailureReason(ctx, t.ThreadRef, err)
		return err
	}
	if turnID != "" {
		t.TurnID = turnID
		_ = c.deps.Store.Upsert(ctx, t)
	}

	return c.runObserveLoop(ctx, t)
}

func (c *Controller) runObserveLoop(ctx context.Context, t Task) error {
	d := c.turnTimeout()
	turnCtx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	for {
		select {
		case <-turnCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			nextSeq := t.StateChangeSeq + 1
			if tErr := c.deps.Store.Transition(ctx, t.ThreadRef, StatusWorking, StatusIdle, nextSeq); tErr != nil && !errors.Is(tErr, ErrConflict) {
				return tErr
			}
			_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
			_ = c.deps.Chat.Post(ctx, t.ThreadRef, fmt.Sprintf("turn timed out after %s; reply to continue", d))
			return ErrTurnTimeout
		default:
		}

		st, err := c.deps.Backend.Observe(turnCtx, t.HerdrAgent, true)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if turnCtx.Err() != nil {
				nextSeq := t.StateChangeSeq + 1
				if tErr := c.deps.Store.Transition(ctx, t.ThreadRef, StatusWorking, StatusIdle, nextSeq); tErr != nil && !errors.Is(tErr, ErrConflict) {
					return tErr
				}
				_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
				_ = c.deps.Chat.Post(ctx, t.ThreadRef, fmt.Sprintf("turn timed out after %s; reply to continue", d))
				return ErrTurnTimeout
			}
			_ = c.deps.Store.Transition(ctx, t.ThreadRef, StatusWorking, StatusFailed, t.StateChangeSeq+1)
			c.postFailureReason(ctx, t.ThreadRef, err)
			return err
		}
		if !st.Settled {
			continue
		}
		if repoll, rErr := c.deps.Backend.Observe(turnCtx, t.HerdrAgent, false); rErr == nil && repoll.Settled && repoll.Seq != st.Seq {
			st = repoll
		}

		switch st.Status {
		case herdragent.StatusDone:
			answer, rErr := c.deps.Backend.ReadAnswer(ctx, t.HerdrAgent, t.TurnID)
			if rErr != nil {
				_ = c.deps.Store.Transition(ctx, t.ThreadRef, StatusWorking, StatusFailed, t.StateChangeSeq+1)
				c.postFailureReason(ctx, t.ThreadRef, rErr)
				return rErr
			}
			if pErr := c.postAnswer(ctx, t, answer); pErr != nil {
				return pErr
			}
			if tErr := c.deps.Store.Transition(ctx, t.ThreadRef, StatusWorking, StatusIdle, t.StateChangeSeq+1); tErr != nil && !errors.Is(tErr, ErrConflict) {
				return tErr
			}
			_ = c.deps.Chat.React(ctx, t.ThreadRef, "white_check_mark")
			return nil
		case herdragent.StatusIdle:
			if sErr := c.sleep(turnCtx, idleSettlePause); sErr != nil {
				if turnCtx.Err() != nil {
					nextSeq := t.StateChangeSeq + 1
					if tErr := c.deps.Store.Transition(ctx, t.ThreadRef, StatusWorking, StatusIdle, nextSeq); tErr != nil && !errors.Is(tErr, ErrConflict) {
						return tErr
					}
					_ = c.deps.Chat.React(ctx, t.ThreadRef, "warning")
					_ = c.deps.Chat.Post(ctx, t.ThreadRef, fmt.Sprintf("turn timed out after %s; reply to continue", d))
					return ErrTurnTimeout
				}
				return ctx.Err()
			}
			if repoll, rErr := c.deps.Backend.Observe(turnCtx, t.HerdrAgent, false); rErr == nil && repoll.Settled {
				st = repoll
			}
			if st.Status == herdragent.StatusBlocked {
				return c.handleBlocked(ctx, t, st)
			}
			answer, rErr := c.deps.Backend.ReadAnswer(ctx, t.HerdrAgent, t.TurnID)
			if rErr != nil {
				_ = c.deps.Store.Transition(ctx, t.ThreadRef, StatusWorking, StatusFailed, t.StateChangeSeq+1)
				c.postFailureReason(ctx, t.ThreadRef, rErr)
				return rErr
			}
			if pErr := c.postAnswer(ctx, t, answer); pErr != nil {
				return pErr
			}
			if tErr := c.deps.Store.Transition(ctx, t.ThreadRef, StatusWorking, StatusIdle, t.StateChangeSeq+1); tErr != nil && !errors.Is(tErr, ErrConflict) {
				return tErr
			}
			_ = c.deps.Chat.React(ctx, t.ThreadRef, "white_check_mark")
			return nil
		case herdragent.StatusBlocked:
			return c.handleBlocked(ctx, t, st)
		}
	}
}

// postFailureReason sends the error reason in-thread for user-actionable errors;
// for all others it posts a generic message and logs the real cause.
func (c *Controller) postFailureReason(ctx context.Context, ref ThreadRef, err error) {
	var msg string
	if errors.Is(err, ErrNotLinked) || errors.Is(err, ErrNoProject) {
		msg = err.Error()
	} else {
		slog.Error("controller turn failure", "ref", ref, "err", err)
		msg = "failed, see controller log"
	}
	_ = c.deps.Chat.Post(ctx, ref, msg)
}

func (c *Controller) postAnswer(ctx context.Context, t Task, answer string) error {
	if strings.HasPrefix(answer, RawAnswerPrefix) {
		raw := []byte(strings.TrimPrefix(answer, RawAnswerPrefix))
		return c.deps.Chat.PostFile(ctx, t.ThreadRef, "agent-output.txt", raw)
	}
	if len(answer) > answerFileSizeLimit {
		return c.deps.Chat.PostFile(ctx, t.ThreadRef, "answer.txt", []byte(answer))
	}
	return c.deps.Chat.Post(ctx, t.ThreadRef, answer)
}
