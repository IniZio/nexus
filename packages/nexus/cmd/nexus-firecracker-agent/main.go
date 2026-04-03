package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inizio/nexus/packages/nexus/pkg/runtime/firecracker"
	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"
)

// Request types
type execRequest struct {
	ID      string   `json:"id"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
	WorkDir string   `json:"workdir,omitempty"`
	Env     []string `json:"env,omitempty"`
}

type execResponse struct {
	ID       string `json:"id"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

func handleExec(req execRequest) execResponse {
	ctx, cancel := context.WithTimeout(context.Background(), agentExecTimeout())
	defer cancel()

	env := append([]string{}, os.Environ()...)
	if len(req.Env) > 0 {
		env = append(env, req.Env...)
	}
	env = ensurePathInEnv(env)

	commandPath := req.Command
	if resolved, err := lookPathInEnv(req.Command, env); err == nil {
		commandPath = resolved
	}

	cmd := exec.CommandContext(ctx, commandPath, req.Args...)
	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}
	cmd.Env = env

	// Capture both stdout and stderr separately
	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()
	exitCode := 0

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = 1
			if stderrBuf.Len() == 0 {
				stderrBuf.WriteString(err.Error())
			}
		}
	}

	return execResponse{
		ID:       req.ID,
		ExitCode: exitCode,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
	}
}

func agentExecTimeout() time.Duration {
	raw := strings.TrimSpace(os.Getenv("AGENT_EXEC_TIMEOUT_SEC"))
	if raw == "" {
		return 10 * time.Minute
	}

	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return 10 * time.Minute
	}

	return time.Duration(seconds) * time.Second
}

func ensurePathInEnv(env []string) []string {
	for i, entry := range env {
		if !strings.HasPrefix(entry, "PATH=") {
			continue
		}
		if strings.TrimSpace(strings.TrimPrefix(entry, "PATH=")) == "" {
			env[i] = "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
		}
		return env
	}

	return append(env, "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
}

func lookPathInEnv(command string, env []string) (string, error) {
	if strings.Contains(command, "/") {
		return command, nil
	}

	pathValue := ""
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			pathValue = strings.TrimPrefix(entry, "PATH=")
			break
		}
	}

	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, command)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() {
			continue
		}
		if info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}

	return "", exec.ErrNotFound
}

func serveConn(conn net.Conn) {
	defer conn.Close()

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	for {
		// Parse request
		var req execRequest
		if err := decoder.Decode(&req); err != nil {
			if err != io.EOF {
				log.Printf("Error decoding request: %v", err)
				// Try to send error response with request ID if available
				encoder.Encode(execResponse{ID: req.ID, ExitCode: 1, Stderr: fmt.Sprintf("decode error: %v", err)})
			}
			return
		}

		// Validate request ID is present
		if strings.TrimSpace(req.ID) == "" {
			log.Printf("Request missing ID field")
			encoder.Encode(execResponse{ExitCode: 1, Stderr: "request ID is required"})
			continue
		}

		// Handle request
		resp := handleExec(req)

		// Send response
		if err := encoder.Encode(resp); err != nil {
			log.Printf("Error encoding response: %v", err)
			return
		}
	}
}

func main() {
	emitDiagnostic("agent boot pid=%d", os.Getpid())

	if os.Getpid() == 1 {
		mountKernelFilesystems()
		emitDiagnostic("agent pid1 kernel filesystems mounted")
		if err := setupNetwork(); err != nil {
			emitDiagnostic("agent pid1 network setup failed (non-fatal): %v", err)
		} else {
			emitDiagnostic("agent pid1 network configured via dhcp")
		}
		setupDNS()
		emitDiagnostic("agent pid1 dns configured")
	}

	listener, transport, err := resolveListener()
	if err != nil {
		emitDiagnostic("agent listener setup failed: %v", err)
		log.Fatalf("Failed to listen: %v", err)
	}
	defer listener.Close()

	emitDiagnostic("agent listener ready transport=%s", transport)
	log.Printf("Firecracker agent listening (%s)", transport)

	for {
		conn, err := listener.Accept()
		if err != nil {
			emitDiagnostic("agent accept failed: %v", err)
			log.Printf("Failed to accept connection: %v", err)
			continue
		}
		emitDiagnostic("agent accepted connection")
		go serveConn(conn)
	}
}

func mountKernelFilesystems() {
	_ = os.MkdirAll("/proc", 0o755)
	_ = os.MkdirAll("/sys", 0o755)
	_ = os.MkdirAll("/dev", 0o755)
	_ = unix.Mount("proc", "/proc", "proc", 0, "")
	_ = unix.Mount("sysfs", "/sys", "sysfs", 0, "")
	_ = unix.Mount("devtmpfs", "/dev", "devtmpfs", 0, "")
}

// setupNetworkFunc is the function used to configure the guest network interface.
// Overridable in tests.
var setupNetworkFunc = realSetupNetwork

// setupNetwork brings up eth0 via DHCP using udhcpc.
// It is called at PID1 boot before setupDNS so that DNS resolution works.
func setupNetwork() error {
	return setupNetworkFunc()
}

func realSetupNetwork() error {
	// Bring the interface up first so udhcpc can configure it.
	if out, err := exec.Command("ip", "link", "set", "eth0", "up").CombinedOutput(); err != nil {
		return fmt.Errorf("ip link set eth0 up: %w: %s", err, strings.TrimSpace(string(out)))
	}

	// Run udhcpc in one-shot mode: -n (exit on failure), -q (quit after obtaining
	// lease), -t 10 (retry 10 times), -T 3 (3s between retries) → max ~30s wait.
	out, err := exec.Command(
		"udhcpc", "-i", "eth0", "-n", "-q", "-t", "10", "-T", "3",
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("udhcpc -i eth0: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// setupDNSPath is the path to /etc/resolv.conf. Overridable in tests.
var setupDNSPath = "/etc/resolv.conf"

// setupDNS writes /etc/resolv.conf with public DNS servers if it is empty or
// missing. This is needed because the kernel ip= cmdline arg configures the
// network interface but does not create /etc/resolv.conf.
func setupDNS() {
	const content = "nameserver 8.8.8.8\nnameserver 1.1.1.1\n"

	// Only write if missing or empty; respect pre-existing config from the rootfs.
	if data, err := os.ReadFile(setupDNSPath); err == nil && len(strings.TrimSpace(string(data))) > 0 {
		return
	}

	_ = os.MkdirAll("/etc", 0o755)
	_ = os.WriteFile(setupDNSPath, []byte(content), 0o644)
}

func resolveListener() (net.Listener, string, error) {
	if os.Getpid() == 1 || os.Getenv("AGENT_REQUIRE_VSOCK") == "1" {
		var lastErr error
		for attempt := 1; attempt <= 120; attempt++ {
			listener, err := listenVsock()
			if err == nil {
				emitDiagnostic("agent vsock listener ready after %d attempt(s)", attempt)
				return listener, "vsock", nil
			}
			lastErr = err
			if attempt == 1 || attempt%20 == 0 {
				emitDiagnostic("agent vsock listen attempt %d failed: %v", attempt, err)
			}
			time.Sleep(500 * time.Millisecond)
		}
		return nil, "", fmt.Errorf("listen vsock (required) failed: %w", lastErr)
	}

	if os.Getenv("AGENT_FORCE_TCP") == "1" {
		listener, err := listenTCP()
		return listener, "tcp", err
	}

	listener, err := listenVsock()
	if err == nil {
		return listener, "vsock", nil
	}

	tcpListener, tcpErr := listenTCP()
	if tcpErr != nil {
		return nil, "", fmt.Errorf("listen vsock: %w; listen tcp fallback: %v", err, tcpErr)
	}
	return tcpListener, "tcp-fallback", nil
}

func listenTCP() (net.Listener, error) {
	port := os.Getenv("AGENT_PORT")
	if port == "" {
		port = "8080"
	}
	return net.Listen("tcp", ":"+port)
}

func listenVsock() (net.Listener, error) {
	port := firecracker.DefaultAgentVSockPort
	if raw := strings.TrimSpace(os.Getenv("AGENT_VSOCK_PORT")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			return nil, fmt.Errorf("invalid AGENT_VSOCK_PORT %q", raw)
		}
		port = uint32(parsed)
	}

	fd, err := unix.Socket(unix.AF_VSOCK, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, err
	}

	if err := unix.Bind(fd, &unix.SockaddrVM{CID: unix.VMADDR_CID_ANY, Port: port}); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	if err := unix.Listen(fd, 128); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	file := os.NewFile(uintptr(fd), "vsock-listener")
	defer file.Close()

	listener, err := vsock.FileListener(file)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}

	return listener, nil
}

func emitDiagnostic(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	log.Print(msg)

	if console, err := os.OpenFile("/dev/console", os.O_WRONLY|os.O_APPEND, 0); err == nil {
		_, _ = fmt.Fprintln(console, msg)
		_ = console.Close()
	}

	if kmsg, err := os.OpenFile("/dev/kmsg", os.O_WRONLY|os.O_APPEND, 0); err == nil {
		_, _ = fmt.Fprintf(kmsg, "<6>nexus-firecracker-agent: %s\n", msg)
		_ = kmsg.Close()
	}
}
