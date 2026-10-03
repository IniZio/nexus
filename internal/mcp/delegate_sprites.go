package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/IniZio/nexus/internal/core/store"
	"github.com/IniZio/nexus/internal/hubclient"
)

const (
	// The marker sits in $HOME (outside the clone, so it never dirties git status);
	// the sprite user cannot write /run/nexus.
	spritesDoneMarker = "/home/sprite/.nexus-delegate-done"
	spritesWorkDir    = "/home/sprite/work"
	spritesBackend    = "sprites"
)

const spritesStandingOrders = `STANDING ORDERS (nexus sprite sandbox)
These orders define the environment and the completion contract, not the method. If a workflow plugin or harness is loaded (e.g. groundwork), deliver the brief through its process.
You are running inside a dedicated Fly.io sprite created for this task. You are the unprivileged user "sprite"; the sprite is the isolation boundary.
Your git clone of the repo is ` + spritesWorkDir + `. Work there. Write scratch and output files under ` + spritesWorkDir + `/.scratch/, not /tmp. Never commit .scratch.
Resolve blockers yourself — install tools and fix code that prevents the work from running. Record any friction you hit — what happened, the evidence, the workaround — in your report.
Egress is policy-gated. A 403 from the proxy names the policy that denied you: report it, do not route around it.
Completion: commit your work (push only if the branch has an upstream; with no remote, commit locally). If a loaded workflow has its own completion gate, pass it first. Then, as your final act, write a one-line summary to ` + spritesDoneMarker + ` (e.g. ` + "`" + `echo "all tests green" > ` + spritesDoneMarker + "`" + `); the host treats that file as done.

`

// delegateTarget is where the in-guest agent works and signals completion.
type delegateTarget struct {
	sprites bool
	marker  string
	workDir string
	orders  string
}

// delegateTargetFor picks the target from the sandbox record's backend.
func delegateTargetFor(ctx context.Context, svc SandboxService, ref string) delegateTarget {
	if sb, ok := delegateSandbox(ctx, svc, ref); ok && sb.Backend == spritesBackend {
		return delegateTarget{sprites: true, marker: spritesDoneMarker, workDir: spritesWorkDir, orders: spritesStandingOrders}
	}
	return delegateTarget{marker: delegateDoneMarker, workDir: "/workspace", orders: standingOrders}
}

// emitDelegateDoneEvent writes one hub event; tests replace it.
var emitDelegateDoneEvent = func(ctx context.Context, ev hubclient.Event) {
	hubclient.New().EmitBestEffort(ctx, ev)
}

// emitDelegateDoneOnce emits delegate.done for id the first time only. The
// flag file in host state makes the guard survive restarts of the MCP server.
func emitDelegateDoneOnce(ctx context.Context, id, line string) {
	flag, ok := delegateDoneFlag(id)
	if !ok {
		return
	}
	if err := os.MkdirAll(filepath.Dir(flag), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(flag, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_ = f.Close()
	raw, err := json.Marshal(hubclient.DelegatePayload{Sandbox: id, Line: line})
	if err != nil {
		return
	}
	ectx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	emitDelegateDoneEvent(ectx, hubclient.Event{
		Topic: hubclient.SandboxTopic(id), Type: hubclient.TypeDelegateDone,
		Subject: id, Payload: raw,
	})
}

func clearDelegateDoneFlag(id string) {
	if flag, ok := delegateDoneFlag(id); ok {
		_ = os.Remove(flag)
	}
}

func delegateDoneFlag(id string) (string, bool) {
	root, err := store.DefaultRoot()
	if err != nil {
		return "", false
	}
	return filepath.Join(root, "delegate-done", id), true
}
