package supervisor

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/IniZio/nexus/internal/core/domain"
	"github.com/IniZio/nexus/internal/herdragent"
	"github.com/IniZio/nexus/internal/hubclient"
)

const (
	delegateLabelWorktree = "worktree"
	delegateLabelPane     = "herdr_pane"
	frictionFile          = ".friction.md"
	delegatePollInterval  = 2 * time.Second
)

type delegateWatcher struct {
	em       hubEmitter
	sandbox  string
	worktree string
	paneID   string
	interval time.Duration
	readPane func(ctx context.Context, paneID string) (string, error)

	permActive bool
	offset     int64
	partial    string
}

// startDelegateWatch starts the watcher for a worktree-bound sandbox. It is a
// no-op when the sandbox carries no worktree label.
func startDelegateWatch(ctx context.Context, em hubEmitter, sb domain.Sandbox) {
	wt := sb.Labels[delegateLabelWorktree]
	if wt == "" {
		return
	}
	w := &delegateWatcher{
		em: em, sandbox: sb.ID.String(), worktree: wt,
		paneID: sb.Labels[delegateLabelPane], interval: delegatePollInterval,
		readPane: herdrReadPane,
	}
	go w.run(ctx)
}

func herdrReadPane(ctx context.Context, paneID string) (string, error) {
	out, err := exec.CommandContext(ctx, "herdr", "agent", "read", paneID, "--source", "recent-unwrapped", "--lines", "40").Output()
	return string(out), err
}

func (w *delegateWatcher) run(ctx context.Context) {
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.poll(ctx)
		}
	}
}

func (w *delegateWatcher) poll(ctx context.Context) {
	w.pollFriction(ctx)
	w.pollPermission(ctx)
}

func (w *delegateWatcher) pollPermission(ctx context.Context) {
	if w.paneID == "" || w.readPane == nil {
		return
	}
	screen, err := w.readPane(ctx, w.paneID)
	if err != nil {
		return
	}
	looks := herdragent.LooksLikeDialog(screen)
	if looks && !w.permActive {
		w.send(ctx, hubclient.TypeDelegatePermission, "", false)
	}
	w.permActive = looks
}

func (w *delegateWatcher) pollFriction(ctx context.Context) {
	f, err := os.Open(filepath.Join(w.worktree, frictionFile))
	if err != nil {
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return
	}
	if st.Size() < w.offset {
		w.offset, w.partial = 0, ""
	}
	if _, err := f.Seek(w.offset, 0); err != nil {
		return
	}
	r := bufio.NewReader(f)
	for {
		s, err := r.ReadString('\n')
		w.offset += int64(len(s))
		if err != nil {
			w.partial += s
			return
		}
		line := strings.TrimSpace(w.partial + strings.TrimSuffix(s, "\n"))
		w.partial = ""
		if line != "" {
			w.send(ctx, hubclient.TypeDelegateFriction, line, isBlockedLine(line))
		}
	}
}

func isBlockedLine(line string) bool {
	l := strings.ToUpper(strings.TrimLeft(line, "-*#> \t"))
	return strings.HasPrefix(l, "BLOCKED")
}

func (w *delegateWatcher) send(ctx context.Context, typ, line string, blocked bool) {
	raw, err := json.Marshal(hubclient.DelegatePayload{Sandbox: w.sandbox, Blocked: blocked, Line: line})
	if err != nil {
		return
	}
	c, cancel := context.WithTimeout(ctx, hubEmitTimeout)
	defer cancel()
	ev := hubclient.Event{
		Topic: hubclient.SandboxTopic(w.sandbox), Type: typ,
		Actor: hubclient.ActorSupervisor, Subject: w.sandbox, Payload: raw,
	}
	if err := w.em.emit(c, ev); err != nil {
		slog.Warn("hub.emit_failed", "type", typ, "sandbox", w.sandbox, "err", err)
	}
}
