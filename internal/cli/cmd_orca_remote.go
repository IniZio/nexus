package cli

// cmd_orca_remote.go — SSH transport for "nexus3 orca" subcommands.
//
// When --remote <dest> is passed (or NEXUS3_REMOTE is set), orca create/
// suspend/resume/destroy are forwarded over SSH to a remote nexus3 host.
// The remote is invoked with NEXUS3_ORCA_REMOTE_INNER=1 so that the host-side
// invocation runs locally without recursing.
//
// For "create", the client re-writes the connection JSON so that:
//   - ProxyCommand goes through the CLIENT-side SSH hop to reach the host,
//     then calls `nexus3 ssh --stdio <sandboxID>` on the host.
//   - IdentityFile is the ed25519 private key shipped from the host and stored
//     locally under ~/.local/share/nexus3/orca/<instanceID>/id_ed25519.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// ── SSH target ────────────────────────────────────────────────────────────────

// sshTarget represents a remote SSH endpoint (user@host with optional port).
type sshTarget struct {
	dest string // "user@host" or "host"
	port int    // 0 means default (22)
}

// args returns the ssh connection arguments (port flag if non-zero, then dest).
// These are prepended before the remote command argument.
func (t *sshTarget) args() []string {
	if t.port > 0 {
		return []string{"-p", strconv.Itoa(t.port), t.dest}
	}
	return []string{t.dest}
}

// proxyPrefix returns the ssh command prefix suitable for use in a ProxyCommand
// field, e.g. "ssh user@host" or "ssh -p 2222 user@host".
func (t *sshTarget) proxyPrefix() string {
	if t.port > 0 {
		return fmt.Sprintf("ssh -p %d %s", t.port, t.dest)
	}
	return "ssh " + t.dest
}

// parseSSHTarget parses a string of the form "user@host", "host", or
// "ssh://user@host:port" into an sshTarget. Returns an error for empty input.
func parseSSHTarget(s string) (*sshTarget, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty SSH target")
	}

	// ssh:// URL form.
	if strings.HasPrefix(s, "ssh://") {
		u, err := url.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("parse SSH URL %q: %w", s, err)
		}
		dest := u.Host // may include port
		port := 0
		if p := u.Port(); p != "" {
			port, err = strconv.Atoi(p)
			if err != nil {
				return nil, fmt.Errorf("parse SSH URL port %q: %w", p, err)
			}
			dest = u.Hostname()
		}
		if u.User != nil {
			dest = u.User.Username() + "@" + u.Hostname()
		}
		return &sshTarget{dest: dest, port: port}, nil
	}

	// Bare form: "user@host" or "host" or "user@host:port".
	// Only strip a trailing :port if the suffix after the last ':' is all digits,
	// to avoid breaking bare hostnames that contain colons (IPv6 excluded per spec).
	target := &sshTarget{}
	if idx := strings.LastIndex(s, ":"); idx >= 0 {
		suffix := s[idx+1:]
		if isAllDigits(suffix) {
			port, err := strconv.Atoi(suffix)
			if err != nil {
				return nil, fmt.Errorf("parse SSH port %q: %w", suffix, err)
			}
			target.port = port
			target.dest = s[:idx]
			return target, nil
		}
	}
	target.dest = s
	return target, nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ── remote resolver ───────────────────────────────────────────────────────────

// resolveOrcaRemote inspects args and environment to determine whether the orca
// command should be forwarded to a remote host.
//
// If NEXUS3_ORCA_REMOTE_INNER == "1" we are the host-side inner invocation;
// strip any --remote flag but always return remote=nil (force local execution).
//
// Otherwise, strip --remote <val> / --remote=<val> from args, collecting the
// value. Precedence: --remote flag > NEXUS3_REMOTE env.
func resolveOrcaRemote(args []string) (remote *sshTarget, rest []string, err error) {
	inner := os.Getenv("NEXUS3_ORCA_REMOTE_INNER") == "1"

	var remoteVal string
	rest = make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--remote" {
			if i+1 < len(args) {
				remoteVal = args[i+1]
				i++ // consume value
			}
			continue
		}
		if strings.HasPrefix(a, "--remote=") {
			remoteVal = strings.TrimPrefix(a, "--remote=")
			continue
		}
		rest = append(rest, a)
	}

	if inner {
		// Host-side: always force local, ignore any remote value.
		return nil, rest, nil
	}

	// Client-side: --remote flag takes precedence over env.
	// Treat an empty or whitespace-only NEXUS3_REMOTE as unset.
	if remoteVal == "" {
		remoteVal = strings.TrimSpace(os.Getenv("NEXUS3_REMOTE"))
	}
	if remoteVal == "" {
		return nil, rest, nil
	}

	t, err := parseSSHTarget(remoteVal)
	if err != nil {
		return nil, nil, fmt.Errorf("--remote: %w", err)
	}
	return t, rest, nil
}

// ── remote create ─────────────────────────────────────────────────────────────

// orcaCreateRemote runs "nexus3 orca create" on the remote host r, captures the
// resulting orcaCreateResult JSON, re-writes the connection to route through the
// SSH hop, ships the identity file locally, and emits the modified JSON on w.
func orcaCreateRemote(ctx context.Context, w io.Writer, r *sshTarget) error {
	instanceID := os.Getenv("ORCA_VM_INSTANCE_ID")
	if instanceID == "" {
		return fmt.Errorf("orca create: ORCA_VM_INSTANCE_ID is not set")
	}

	hostBin, err := resolveHostNexus3(ctx, r)
	if err != nil {
		return fmt.Errorf("orca create --remote: %w", err)
	}

	envTokens := buildOrcaEnvTokens(true)
	remoteCmd := buildRemoteCmd(envTokens, hostBin, "orca", "create")

	stdout, err := sshRunCapture(ctx, r, remoteCmd)
	if err != nil {
		return fmt.Errorf("orca create --remote: remote execution failed: %w", err)
	}

	// Parse the last JSON line from stdout.
	var res orcaCreateResult
	if parseErr := parseLastJSONLine(stdout, &res); parseErr != nil {
		return fmt.Errorf("orca create --remote: parse remote output: %w (stdout=%q)", parseErr, stdout)
	}

	// Ship the identity file from the remote host to a local path.
	// Use sshCaptureRaw (no trimming) so the trailing newline required by
	// OpenSSH is preserved; trimming it causes "error in libcrypto".
	localKey := ""
	if hostKey := res.Connection.Target.IdentityFile; hostKey != "" {
		keyBytes, catErr := sshCaptureRaw(ctx, r, "cat "+shellQuote(hostKey))
		if catErr != nil {
			return fmt.Errorf("orca create --remote: fetch identity file: %w", catErr)
		}
		if len(keyBytes) == 0 {
			return fmt.Errorf("orca create --remote: shipped identity key is empty")
		}
		if keyBytes[len(keyBytes)-1] != '\n' {
			keyBytes = append(keyBytes, '\n')
		}

		localKeyDir := filepath.Join(os.Getenv("HOME"), ".local", "share", "nexus3", "orca", instanceID)
		if mkErr := os.MkdirAll(localKeyDir, 0700); mkErr != nil {
			return fmt.Errorf("orca create --remote: mkdir local key dir: %w", mkErr)
		}
		localKey = filepath.Join(localKeyDir, "id_ed25519")
		if writeErr := os.WriteFile(localKey, keyBytes, 0600); writeErr != nil {
			return fmt.Errorf("orca create --remote: write local key: %w", writeErr)
		}
	}

	// Rewrite connection: ProxyCommand goes through the SSH hop, then calls
	// nexus3 ssh --stdio on the host (expanding %h to the sandbox ID).
	res.Connection.Target.ProxyCommand = r.proxyPrefix() + " " + hostBin + " ssh --stdio %h"
	if localKey != "" {
		res.Connection.Target.IdentityFile = localKey
	}

	return json.NewEncoder(w).Encode(res)
}

// ── remote lifecycle (suspend/resume/destroy) ─────────────────────────────────

// orcaLifecycleRemote forwards a lifecycle verb (suspend/resume/destroy) to the
// remote host, streaming stdout and stderr locally.
func orcaLifecycleRemote(ctx context.Context, verb string, r *sshTarget) error {
	instanceID := os.Getenv("ORCA_VM_INSTANCE_ID")
	if instanceID == "" {
		return fmt.Errorf("orca %s: ORCA_VM_INSTANCE_ID is not set", verb)
	}

	hostBin, err := resolveHostNexus3(ctx, r)
	if err != nil {
		return fmt.Errorf("orca %s --remote: %w", verb, err)
	}

	envTokens := buildOrcaLifecycleEnvTokens(instanceID)
	remoteCmd := buildRemoteCmd(envTokens, hostBin, "orca", verb)
	return sshRun(ctx, r, remoteCmd)
}

// ── helpers ───────────────────────────────────────────────────────────────────

// resolveHostNexus3 finds the nexus3 binary on the remote host by first trying
// `command -v nexus3`, then falling back to well-known paths.
func resolveHostNexus3(ctx context.Context, r *sshTarget) (string, error) {
	out, err := sshCapture(ctx, r, "command -v nexus3 || true")
	if err != nil {
		return "", fmt.Errorf("resolve nexus3 on host: %w", err)
	}
	if out != "" {
		return out, nil
	}
	// Fall back to well-known paths.
	out, err = sshCapture(ctx, r,
		`for p in "$HOME/.local/bin/nexus3" /usr/local/bin/nexus3; do [ -x "$p" ] && { echo "$p"; break; }; done`)
	if err != nil {
		return "", fmt.Errorf("resolve nexus3 on host (fallback): %w", err)
	}
	if out == "" {
		return "", fmt.Errorf("remote host: nexus3 binary not found")
	}
	return out, nil
}

// buildOrcaEnvTokens builds the env token list for orca create forwarding.
// Includes all ORCA_* env vars, NEXUS3_IMAGE, NEXUS3_DEDICATED_CRED_STORE (if
// set), and NEXUS3_ORCA_REMOTE_INNER=1.
func buildOrcaEnvTokens(includeAllOrca bool) []string {
	var tokens []string
	if includeAllOrca {
		for _, e := range os.Environ() {
			if strings.HasPrefix(e, "ORCA_") {
				tokens = append(tokens, e)
			}
		}
	}
	img := os.Getenv("NEXUS3_IMAGE")
	if img == "" {
		img = "nexus3-agent-base"
	}
	tokens = append(tokens, "NEXUS3_IMAGE="+img)
	if cred := os.Getenv("NEXUS3_DEDICATED_CRED_STORE"); cred != "" {
		tokens = append(tokens, "NEXUS3_DEDICATED_CRED_STORE="+cred)
	}
	tokens = append(tokens, "NEXUS3_ORCA_REMOTE_INNER=1")
	return tokens
}

// buildOrcaLifecycleEnvTokens builds env tokens for lifecycle verbs (suspend/
// resume/destroy). Only the instance ID, image, cred store, and inner guard are
// forwarded (no full ORCA_* dump needed for lifecycle).
func buildOrcaLifecycleEnvTokens(instanceID string) []string {
	tokens := []string{"ORCA_VM_INSTANCE_ID=" + instanceID}
	img := os.Getenv("NEXUS3_IMAGE")
	if img == "" {
		img = "nexus3-agent-base"
	}
	tokens = append(tokens, "NEXUS3_IMAGE="+img)
	if cred := os.Getenv("NEXUS3_DEDICATED_CRED_STORE"); cred != "" {
		tokens = append(tokens, "NEXUS3_DEDICATED_CRED_STORE="+cred)
	}
	tokens = append(tokens, "NEXUS3_ORCA_REMOTE_INNER=1")
	return tokens
}

// buildRemoteCmd returns a single shell command string of the form:
//
//	env 'KEY=VAL' ... '/path/to/bin' 'arg' ...
//
// Each token is single-quote-escaped. The whole string is passed as ONE ssh
// remote-command argument so the remote shell receives it as a single unit.
func buildRemoteCmd(envTokens []string, argv ...string) string {
	parts := make([]string, 0, 1+len(envTokens)+len(argv))
	parts = append(parts, "env")
	for _, t := range envTokens {
		parts = append(parts, shellQuote(t))
	}
	for _, a := range argv {
		parts = append(parts, shellQuote(a))
	}
	return strings.Join(parts, " ")
}

// shellQuote returns a POSIX single-quoted version of s, safe for use in a
// shell command. Every single-quote inside s is replaced with '\” (end-quote,
// literal quote, re-open-quote).
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// sshRun executes remoteCmd on r over SSH, streaming stdout and stderr to the
// local process's stdout and stderr.
func sshRun(ctx context.Context, r *sshTarget, remoteCmd string) error {
	args := append(r.args(), remoteCmd)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// sshRunCapture executes remoteCmd on r over SSH, capturing stdout and
// streaming stderr to the local process's stderr. Returns captured stdout.
func sshRunCapture(ctx context.Context, r *sshTarget, remoteCmd string) (string, error) {
	args := append(r.args(), remoteCmd)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return buf.String(), err
}

// sshCapture executes remoteCmd on r over SSH, capturing both stdout and
// stderr (stderr goes to local stderr as a side-channel). Returns trimmed stdout.
func sshCapture(ctx context.Context, r *sshTarget, remoteCmd string) (string, error) {
	args := append(r.args(), remoteCmd)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return strings.TrimSpace(buf.String()), err
}

// sshCaptureRaw executes remoteCmd on r over SSH and returns raw stdout bytes
// with NO trimming. Stderr is forwarded to the local process's stderr.
// Use this (rather than sshCapture) when byte-exact output is required —
// e.g. fetching an OpenSSH private key whose trailing newline must be preserved.
func sshCaptureRaw(ctx context.Context, r *sshTarget, remoteCmd string) ([]byte, error) {
	args := append(r.args(), remoteCmd)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return []byte(buf.String()), err
}

// parseLastJSONLine scans lines of s from the end and unmarshals the last line
// beginning with '{' into v. Returns an error if no such line is found.
func parseLastJSONLine(s string, v interface{}) error {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "{") {
			return json.Unmarshal([]byte(line), v)
		}
	}
	return fmt.Errorf("no JSON object found in output")
}
