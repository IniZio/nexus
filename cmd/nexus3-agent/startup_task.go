package main

import (
	"os"
	"os/exec"
)

// startupHookPath is the in-guest path of an optional executable that the agent
// runs once at boot as a background, non-fatal task. Images opt in by baking an
// executable here (e.g. a .nexus/Containerfile that writes it). It is nexus3's
// build-path-agnostic equivalent of honouring an OCI ENTRYPOINT: the agent stays
// PID 1 and keeps full ownership of the control plane, but runs the image's
// declared boot command.
//
// A var (not const) so tests can point it at a temp fixture.
//
// Contract:
//   - background: it never blocks the agent from serving Exec/Copy — PID 1
//     continues to the vsock listeners while the hook runs;
//   - non-fatal: a missing hook, a non-executable hook, or a non-zero exit is
//     logged and ignored. PID-1 exit panics the kernel, so a boot task must
//     never be able to take the sandbox down;
//   - fire-once: it runs a single time at boot. Long-running daemons (dockerd,
//     …) are the hook's own responsibility to background and readiness-gate
//     (e.g. start dockerd, then poll `docker info` until ready).
var startupHookPath = "/etc/nexus3/startup"

// startupHookDecision is the gating outcome for the startup hook, extracted as a
// pure value so the run/skip logic is unit-testable without a real /etc mount.
type startupHookDecision int

const (
	startupSkipAbsent        startupHookDecision = iota // no hook baked in — the common case
	startupSkipNonExecutable                            // present but a dir, or no exec bit
	startupRun                                          // present, regular, executable
)

// startupHookAction decides what to do with a stat() of startupHookPath.
func startupHookAction(fi os.FileInfo, statErr error) startupHookDecision {
	if statErr != nil {
		return startupSkipAbsent
	}
	if fi.IsDir() || fi.Mode().Perm()&0o111 == 0 {
		return startupSkipNonExecutable
	}
	return startupRun
}

// runStartupHook runs startupHookPath in the background if it exists and is
// executable. Output is streamed to the console for operator visibility. It
// returns immediately; the hook runs in a goroutine so the caller (PID 1) can
// proceed to bind the control plane while the hook (e.g. dockerd bring-up) runs.
func runStartupHook(con *os.File) {
	fi, statErr := os.Stat(startupHookPath)
	switch startupHookAction(fi, statErr) {
	case startupSkipAbsent:
		return
	case startupSkipNonExecutable:
		consoleLog(con, "nexus3-agent: startup hook %s present but not executable; skipping\n", startupHookPath)
		return
	}

	consoleLog(con, "nexus3-agent: startup hook %s: running (background, non-fatal)\n", startupHookPath)
	go func() {
		cmd := exec.Command(startupHookPath)
		// guestBaselineEnv supplies a correct HOME=/root and PATH; PID 1 gets no
		// environment from the kernel, so without this the hook's exec of docker,
		// dockerd, etc. would fail with "executable file not found in $PATH".
		cmd.Env = guestBaselineEnv()
		out, err := cmd.CombinedOutput()
		if len(out) > 0 {
			consoleLog(con, "nexus3-agent: startup hook output:\n%s", out)
		}
		if err != nil {
			consoleLog(con, "nexus3-agent: startup hook exited: %v (non-fatal; sandbox continues)\n", err)
			return
		}
		consoleLog(con, "nexus3-agent: startup hook complete\n")
	}()
}
