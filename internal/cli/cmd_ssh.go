package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/IniZio/nexus3/internal/core/service"
)

func init() {
	Register(Command{
		Name:    "ssh",
		Summary: "Dial a sandbox's sshd over vsock (use as SSH ProxyCommand with --stdio)",
		Run:     runSSH,
	})
	Register(Command{
		Name:    "config-ssh",
		Summary: "Write an SSH config stanza for a sandbox (ProxyCommand via nexus3 ssh --stdio)",
		Run:     runConfigSSH,
	})
}

// ── ssh ───────────────────────────────────────────────────────────────────────

// runSSH is the registered Run function for "nexus3 ssh [--stdio] <ref>".
// With --stdio it dials vsock port 22 on the guest and splices stdin/stdout,
// making it suitable as an SSH ProxyCommand.
func runSSH(ctx context.Context, args []string, _ *Output) error {
	fs := flag.NewFlagSet("ssh", flag.ContinueOnError)
	stdioFlag := fs.Bool("stdio", false, "ProxyCommand mode: splice stdin/stdout to the guest's vsock port 22")
	if err := fs.Parse(args); err != nil {
		return &UsageError{Msg: "ssh: " + err.Error()}
	}
	positional := fs.Args()
	if len(positional) != 1 {
		return &UsageError{Msg: "ssh: usage: ssh [--stdio] <sandbox-ref>"}
	}
	ref := positional[0]

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "ssh: " + err.Error(), Err: err}
	}

	if !*stdioFlag {
		return &UsageError{Msg: "ssh: --stdio is required (SSH ProxyCommand mode only)"}
	}

	return runSSHStdio(ctx, ref, svc, os.Stdin, os.Stdout)
}

// runSSHStdio resolves the sandbox, dials vsock port 22, and bidirectionally
// splices the connection to the provided stdin/stdout. It returns when either
// direction reaches EOF or the context is cancelled; before returning it closes
// the connection and waits for the conn→stdout copy to finish, so no goroutine
// writes to stdout after the call returns. The stdin→conn copy is not waited
// on — it may still be parked in a read of stdin (os.Stdin never reaches EOF in
// the ProxyCommand path) — so it may keep reading stdin after the return.
func runSSHStdio(ctx context.Context, ref string, svc *service.Service, stdin io.Reader, stdout io.Writer) error {
	conn, err := svc.DialGuest(ctx, ref, 22)
	if err != nil {
		return &CodedError{
			Code: sandboxCodeFor(err),
			Msg:  fmt.Sprintf("ssh: dial guest vsock:22: %v", err),
			Err:  err,
		}
	}
	defer conn.Close()

	// Two-direction splice: conn→stdout and stdin→conn.
	// We stop as soon as either direction finishes (EOF or error).
	done := make(chan error, 2)
	// outDone is closed only after the conn→stdout copy has stopped touching
	// stdout, so the caller's writer is quiescent by the time we return.
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		_, err := io.Copy(stdout, conn)
		done <- err
	}()
	go func() {
		_, err := io.Copy(conn, stdin)
		done <- err
	}()

	select {
	case <-ctx.Done():
	case <-done:
	}

	// Close the connection first: that unblocks the conn→stdout read so the
	// drain below cannot hang. We deliberately do NOT wait for the stdin→conn
	// direction — it may be parked forever in a read of os.Stdin.
	conn.Close()
	<-outDone
	return nil
}

// ── config-ssh ────────────────────────────────────────────────────────────────

// runConfigSSH is the registered Run function for "nexus3 config-ssh <ref>".
func runConfigSSH(ctx context.Context, args []string, out *Output) error {
	if len(args) != 1 {
		return &UsageError{Msg: "config-ssh: usage: config-ssh <sandbox-ref>"}
	}
	ref := args[0]

	svc, err := newSandboxService()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "config-ssh: " + err.Error(), Err: err}
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "config-ssh: resolve home dir: " + err.Error(), Err: err}
	}

	return runConfigSSHWithHome(ctx, ref, svc, homeDir, out)
}

// configSSHResult is the --json payload for a successful config-ssh run.
type configSSHResult struct {
	ConfigPath string `json:"config_path"`
	Host       string `json:"host"`
	Updated    bool   `json:"updated"`
}

// runConfigSSHWithHome writes an SSH config stanza for the sandbox identified
// by ref into <homeDir>/.ssh/config. It is idempotent: if a stanza for this
// handle already exists, it prints a message and exits 0. Extracted for
// testability.
func runConfigSSHWithHome(ctx context.Context, ref string, svc *service.Service, homeDir string, out *Output) error {
	sb, err := svc.Lookup(ctx, ref)
	if err != nil {
		return &CodedError{
			Code: sandboxCodeFor(err),
			Msg:  fmt.Sprintf("config-ssh: resolve %q: %v", ref, err),
			Err:  err,
		}
	}

	handle := sb.Handle()
	hostAlias := "nexus3-" + strings.ReplaceAll(handle, "/", "-")

	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "config-ssh: mkdir ~/.ssh: " + err.Error(), Err: err}
	}

	configPath := filepath.Join(sshDir, "config")

	// Check for an existing stanza by scanning for our marker comment.
	marker := "# nexus3 sandbox: " + handle
	if existing, err := os.ReadFile(configPath); err == nil {
		scanner := bufio.NewScanner(strings.NewReader(string(existing)))
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == marker {
				// Already present.
				fmt.Fprintf(os.Stderr, "config-ssh: stanza for %s already exists in %s\n", handle, configPath)
				if out.IsJSON() {
					out.EmitSuccess("config_ssh.done", configSSHResult{
						ConfigPath: configPath,
						Host:       hostAlias,
						Updated:    false,
					}, "")
				}
				return nil
			}
		}
	}

	stanza := fmt.Sprintf(`
%s
Host %s
    ProxyCommand nexus3 ssh --stdio %s
    StrictHostKeyChecking no
    UserKnownHostsFile /dev/null
`, marker, hostAlias, handle)

	f, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "config-ssh: open config: " + err.Error(), Err: err}
	}
	defer f.Close()
	if _, err := fmt.Fprint(f, stanza); err != nil {
		return &CodedError{Code: ErrCodeInternalError, Msg: "config-ssh: write config: " + err.Error(), Err: err}
	}

	if out.IsJSON() {
		out.EmitSuccess("config_ssh.done", configSSHResult{
			ConfigPath: configPath,
			Host:       hostAlias,
			Updated:    true,
		}, "")
		return nil
	}

	fmt.Printf("config-ssh: wrote stanza for %s to %s\n", handle, configPath)
	fmt.Printf("  ssh %s\n", hostAlias)
	return nil
}
