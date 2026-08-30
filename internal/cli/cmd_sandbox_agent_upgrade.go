package cli

// sandbox agent-upgrade <handle> [--agent <path>] [--force] [--timeout <duration>]
//
// Hot-swaps the in-guest nexus3-agent binary without stopping the sandbox.
// Protocol:
//  1. Read the replacement binary (--agent, or auto-located via LookPath).
//  2. Push it to the guest via the existing Copy PUSH RPC with ExpectedBytes.
//  3. Call RestartAgent{staged_path, expected_bytes, force}.
//  4. Poll Ping until the new agent answers.
//  5. Call AgentInfo to read the new build tag; print old→new.
//
// The RestartAgent RPC replaces PID 1 via syscall.Exec; the connection resets.
// A transport reset is treated as "restart initiated".

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/driver"
)

func init() {
	Register(Command{
		Name:    "sandbox agent-upgrade",
		Summary: "Hot-swap the in-guest nexus3-agent binary without stopping the sandbox",
		Run:     runSandboxAgentUpgrade,
	})
}

func runSandboxAgentUpgrade(ctx context.Context, args []string, out *Output) error {
	fs := flag.NewFlagSet("sandbox agent-upgrade", flag.ContinueOnError)
	var (
		agentFlag   = fs.String("agent", "", "path to the replacement nexus3-agent binary (default: auto-locate via PATH)")
		forceFlag   = fs.Bool("force", false, "force upgrade even if active exec sessions exist (those sessions will be killed)")
		timeoutFlag = fs.Duration("timeout", 30*time.Second, "maximum time to wait for the new agent to become ready")
	)
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "sandbox agent-upgrade: " + err.Error()}
	}
	positional := fs.Args()
	if len(positional) != 1 {
		return &UsageError{Msg: "sandbox agent-upgrade: usage: sandbox agent-upgrade <sandbox-ref> [--agent <path>] [--force] [--timeout <duration>]"}
	}
	ref := positional[0]

	// Locate the replacement binary.
	agentBin := *agentFlag
	if agentBin == "" {
		var err error
		agentBin, err = exec.LookPath("nexus3-agent")
		if err != nil {
			return &CodedError{
				Code: ErrCodeInternalError,
				Msg:  "sandbox agent-upgrade: cannot locate nexus3-agent in PATH; use --agent <path>",
				Err:  err,
			}
		}
	}
	agentBin = filepath.Clean(agentBin)

	// Build the service and driver.
	svc, err := newSandboxService()
	if err != nil {
		return errSandbox("sandbox agent-upgrade", err)
	}

	// Resolve the sandbox.
	sb, err := svc.Get(ctx, ref)
	if err != nil {
		return errSandbox("sandbox agent-upgrade", err)
	}

	// The driver must support DialGuest.
	drv, serr := SelectSubstrate()
	if serr != nil {
		return &CodedError{
			Code: sandboxErrCodeNoSubstrate,
			Msg:  "sandbox agent-upgrade: " + serr.Msg,
			Err:  serr,
		}
	}
	gd, ok := drv.(driver.GuestDialer)
	if !ok {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  "sandbox agent-upgrade: driver does not support guest dialing",
		}
	}

	ac := agent.NewClient(gd, sb.ID)

	// Read the old version before the swap.
	oldTag, err := ac.AgentInfo(ctx)
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("sandbox agent-upgrade: read current agent version: %v", err),
			Err:  err,
		}
	}

	fmt.Fprintf(out.Stderr(), "sandbox agent-upgrade: current build tag: %s\n", oldTag)
	fmt.Fprintf(out.Stderr(), "sandbox agent-upgrade: pushing %s ...\n", agentBin)

	// Verify the binary exists and is readable before dialing.
	if _, statErr := os.Stat(agentBin); statErr != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("sandbox agent-upgrade: stat %q: %v", agentBin, statErr),
			Err:  statErr,
		}
	}

	upgradeCtx, cancel := context.WithTimeout(ctx, *timeoutFlag)
	defer cancel()

	newTag, err := ac.AgentUpgrade(upgradeCtx, agent.AgentUpgradeOptions{
		LocalBinaryPath: agentBin,
		Force:           *forceFlag,
	})
	if err != nil {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("sandbox agent-upgrade: %v", err),
			Err:  err,
		}
	}

	if newTag == oldTag {
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  fmt.Sprintf("sandbox agent-upgrade: version unchanged after restart (tag=%q); binary may not have been stamped", newTag),
		}
	}

	type agentUpgradeJSON struct {
		Sandbox string `json:"sandbox"`
		OldTag  string `json:"old_tag"`
		NewTag  string `json:"new_tag"`
		Binary  string `json:"binary"`
	}
	out.EmitSuccess("sandbox.agent_upgrade", agentUpgradeJSON{
		Sandbox: sb.Handle(),
		OldTag:  oldTag,
		NewTag:  newTag,
		Binary:  agentBin,
	}, fmt.Sprintf("agent upgraded %s → %s", oldTag, newTag))
	return nil
}
