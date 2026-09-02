package main

import (
	"encoding/json"
	"os"
	"os/exec"

	"github.com/IniZio/nexus3/internal/core/bootspec"
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
	// Snapshot the package var on the caller's goroutine. The spawned goroutine
	// must never read startupHookPath itself: it outlives this call, so a test
	// that restores the var (t.Cleanup, or a second case in the same test) would
	// race the goroutine's read. Reading once here also makes the stat and the
	// exec provably refer to the same path.
	path := startupHookPath

	fi, statErr := os.Stat(path)
	switch startupHookAction(fi, statErr) {
	case startupSkipAbsent:
		return
	case startupSkipNonExecutable:
		consoleLog(con, "nexus3-agent: startup hook %s present but not executable; skipping\n", path)
		return
	}

	consoleLog(con, "nexus3-agent: startup hook %s: running (background, non-fatal)\n", path)
	go func() {
		cmd := exec.Command(path)
		// guestBaselineEnv supplies a correct HOME=/root and PATH; PID 1 gets no
		// environment from the kernel, so without this the hook's exec of docker,
		// dockerd, etc. would fail with "executable file not found in $PATH".
		cmd.Env = guestBaselineEnv(agentScratchDisk)
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

// bootspecPath is the in-guest location of the generic boot manifest.
// A var (not const) so tests can point it at a temp fixture.
var bootspecPath = bootspec.Path

// runBootTask executes one bootspec.Task. For background tasks it spawns a
// goroutine and returns immediately; for foreground tasks it blocks the caller
// (the supervisor goroutine) until the process exits. Either way PID 1 is
// never blocked: runBootTasks itself runs inside a goroutine.
//
// Empty Argv is skipped. A non-zero exit is logged and non-fatal.
func runBootTask(con *os.File, task bootspec.Task) {
	if len(task.Argv) == 0 {
		name := task.Name
		if name == "" {
			name = "<unnamed>"
		}
		consoleLog(con, "nexus3-agent: boot task %q: empty argv; skipping\n", name)
		return
	}
	label := task.Name
	if label == "" {
		label = task.Argv[0]
	}

	argv0, err := exec.LookPath(task.Argv[0])
	if err != nil {
		consoleLog(con, "nexus3-agent: boot task %q: %v; skipping\n", label, err)
		return
	}

	launch := func() {
		cmd := exec.Command(argv0, task.Argv[1:]...)
		if task.Cwd != "" {
			cmd.Dir = task.Cwd
		}
		// task.Env overrides baseline keys; envToMap handles KEY=VALUE pairs.
		cmd.Env = mergeEnv(guestBaselineEnv(agentScratchDisk), envToMap(task.Env))
		out, err := cmd.CombinedOutput()
		if len(out) > 0 {
			consoleLog(con, "nexus3-agent: boot task %q output:\n%s", label, out)
		}
		if err != nil {
			consoleLog(con, "nexus3-agent: boot task %q exited: %v (non-fatal; sandbox continues)\n", label, err)
		} else {
			consoleLog(con, "nexus3-agent: boot task %q complete\n", label)
		}
	}

	if task.Background {
		go launch()
	} else {
		launch()
	}
}

// runBootTasks is the generic boot supervisor. It reads bootspecPath
// (/etc/nexus3/boot.json) and runs each declared task in order. A missing or
// unparseable manifest falls back to the legacy /etc/nexus3/startup behavior.
//
// PID-1 safety: this function returns immediately — all work runs in a
// goroutine. Foreground tasks block that goroutine (sequencing), never PID 1.
func runBootTasks(con *os.File) {
	data, err := os.ReadFile(bootspecPath)
	if err != nil {
		// Absent manifest is the common case — fall through to legacy hook.
		runStartupHook(con)
		return
	}
	var spec bootspec.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		consoleLog(con, "nexus3-agent: boot.json unparseable: %v; falling back to startup hook\n", err)
		runStartupHook(con)
		return
	}

	// Run all tasks inside a single goroutine so foreground tasks are sequenced
	// without blocking the agent's main serving path (PID 1).
	go func() {
		for _, task := range spec.Tasks {
			runBootTask(con, task)
		}
	}()
}
