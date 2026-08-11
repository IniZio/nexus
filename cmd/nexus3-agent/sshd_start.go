package main

import (
	"os"
	"os/exec"
)

// startSSHD creates the privilege-separation directory expected by sshd and
// launches /usr/sbin/sshd in the background.  sshd without -D forks into the
// background on its own, so the call returns quickly.
//
// Host keys are assumed to be baked into the image (ssh-keygen -A at image
// build time).  As a safe fallback, ssh-keygen -A is run first; it is
// idempotent and fast when keys already exist.
//
// The function is non-fatal: if sshd fails to start, the error is logged to
// the console but the agent continues running.
func startSSHD(con *os.File) {
	// Create the privilege-separation directory; sshd exits immediately
	// without it ("Missing privilege separation directory: /run/sshd").
	if err := os.MkdirAll("/run/sshd", 0o755); err != nil {
		consoleLog(con, "nexus3-agent: sshd: mkdir /run/sshd: %v\n", err)
		// Non-fatal — attempt to start sshd anyway.
	}

	// Normalize /root ownership so sshd StrictModes passes.
	// nexus3 images are built by rootless buildkit (userns subuid mapping), so
	// every file baked into the image — including /root — is owned by the subuid
	// base (uid/gid 1003) rather than uid 0.  At runtime the guest boots as real
	// root (no userns), so sshd StrictModes rejects pubkey login because the home
	// directory is not owned by the login user.  Chown /root and /root/.ssh (if
	// it already exists) to 0:0 here, at boot, as real root.  authorized_keys and
	// .ssh are seeded after boot and will already be owned by root; we only need
	// to fix what the image baked in.
	if err := os.Chown("/root", 0, 0); err != nil {
		consoleLog(con, "nexus3-agent: sshd: chown /root: %v\n", err)
		// Non-fatal — attempt to start sshd anyway.
	}
	if _, err := os.Stat("/root/.ssh"); err == nil {
		if err := os.Chown("/root/.ssh", 0, 0); err != nil {
			consoleLog(con, "nexus3-agent: sshd: chown /root/.ssh: %v\n", err)
		}
	}

	// Ensure host keys exist (idempotent; fast if already present).
	if out, err := exec.Command("ssh-keygen", "-A").CombinedOutput(); err != nil {
		consoleLog(con, "nexus3-agent: sshd: ssh-keygen -A: %v: %s\n", err, out)
		// Non-fatal — baked keys may already be present.
	}

	// Launch sshd.  Without -D it daemonises automatically (double-fork).
	// Do NOT use CombinedOutput: the forked daemon inherits the pipe write
	// end, causing CombinedOutput to block forever waiting for the pipe to
	// close.  Use Start()+Process.Release() instead: Start returns as soon
	// as the process is spawned, Release() detaches the child so it can be
	// reaped by the kernel (PID 1 reaps all orphans).
	cmd := exec.Command("/usr/sbin/sshd")
	if err := cmd.Start(); err != nil {
		consoleLog(con, "nexus3-agent: sshd: start failed: %v\n", err)
		return
	}
	// Detach: we don't own the daemon's lifecycle; PID 1 will reap it.
	_ = cmd.Process.Release()
	consoleLog(con, "nexus3-agent: sshd: started (pid=%d)\n", cmd.Process.Pid)
}
