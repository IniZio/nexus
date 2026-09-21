package vm

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/IniZio/nexus/internal/openshell"
)

const (
	chStartTimeout        = 10 * time.Second
	stopGracefulTimeout   = 5 * time.Second
	vsockHandshakeTimeout = 5 * time.Second
	defaultMemMiB         = 2048
	defaultVCPUs          = 2
	defaultInitPath       = "/srv/openshell-vm-sandbox-init.sh"
	chSerialMode          = "File"
)

// Config configures a Backend.
type Config struct {
	CHBinary      string
	KernelPath    string
	StateDir      string
	DefaultMemMiB int
	DefaultVCPUs  int32
	InitPath      string
}

func (c *Config) initPath() string {
	if c.InitPath != "" {
		return c.InitPath
	}
	return defaultInitPath
}

func (c *Config) memMiB() uint64 {
	if c.DefaultMemMiB > 0 {
		return uint64(c.DefaultMemMiB)
	}
	return defaultMemMiB
}

func (c *Config) vcpus() uint32 {
	if c.DefaultVCPUs > 0 {
		return uint32(c.DefaultVCPUs)
	}
	return defaultVCPUs
}

type sandboxRuntime struct {
	ID           string `json:"id"`
	PID          int    `json:"pid"`
	CID          uint32 `json:"cid"`
	APISocket    string `json:"api_socket"`
	VsockSocket  string `json:"vsock_socket"`
	BridgeSocket string `json:"bridge_socket"`
	StateDir     string `json:"state_dir"`
}

// Backend manages cloud-hypervisor VM lifecycles. Implements openshell.Backend.
type Backend struct {
	cfg     Config
	mu      sync.Mutex
	handles map[string]*vmHandle
}

// New creates a Backend. CHBinary, KernelPath, and StateDir must be set.
func New(cfg Config) (*Backend, error) {
	if cfg.CHBinary == "" {
		return nil, errors.New("vm: CHBinary must be set")
	}
	if cfg.KernelPath == "" {
		return nil, errors.New("vm: KernelPath must be set")
	}
	if cfg.StateDir == "" {
		return nil, errors.New("vm: StateDir must be set")
	}
	return &Backend{cfg: cfg, handles: make(map[string]*vmHandle)}, nil
}

func (b *Backend) Boot(ctx context.Context, spec openshell.SandboxSpec, images openshell.ImageArtifacts) (openshell.VMHandle, error) {
	id := spec.ID
	if id == "" {
		return nil, errors.New("vm: SandboxSpec.ID must be set")
	}
	if images.RootfsExt4 == "" {
		return nil, errors.New("vm: ImageArtifacts.RootfsExt4 must be set")
	}
	if images.OverlayExt4 == "" {
		return nil, errors.New("vm: ImageArtifacts.OverlayExt4 must be set")
	}

	sbDir := filepath.Join(b.cfg.StateDir, id)
	if err := os.MkdirAll(sbDir, 0o700); err != nil {
		return nil, fmt.Errorf("vm: boot %s: mkdir: %w", id, err)
	}

	apiSocket := filepath.Join(sbDir, "api.sock")
	vsockSocket := filepath.Join(sbDir, id+".vsock")
	serialLog := filepath.Join(sbDir, "serial.log")
	bridgeSocket := filepath.Join(sbDir, "bridge.sock")

	cid, err := allocateCID(b.cfg.StateDir)
	if err != nil {
		return nil, fmt.Errorf("vm: boot %s: allocate CID: %w", id, err)
	}

	cmd, err := spawnCH(ctx, b.cfg.CHBinary, apiSocket)
	if err != nil {
		releaseCID(b.cfg.StateDir, cid)
		return nil, fmt.Errorf("vm: boot %s: spawn CH: %w", id, err)
	}

	pid := cmd.Process.Pid
	cli := newCHClient(apiSocket)

	cleanup := func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = os.Remove(apiSocket)
		releaseCID(b.cfg.StateDir, cid)
	}

	memBytes := uint64(spec.Resources.BootMemBytes)
	if memBytes == 0 {
		memBytes = b.cfg.memMiB() * 1024 * 1024
	}
	vcpus := uint32(spec.Resources.VCPUs)
	if vcpus == 0 {
		vcpus = b.cfg.vcpus()
	}

	cmdline := fmt.Sprintf("console=ttyS0 root=/dev/vda rootfstype=ext4 ro panic=-1 init=%s", b.cfg.initPath())

	vmCfg := chVMConfig{
		Payload: chVMPayload{Kernel: b.cfg.KernelPath, Cmdline: cmdline},
		CPUs:    &chVMCPUs{BootVCPUs: vcpus, MaxVCPUs: vcpus, Nested: false},
		Memory:  &chVMMemory{SizeBytes: memBytes},
		Serial:  &chVMSerial{Mode: chSerialMode, File: serialLog},
		Disks:   buildDisks(images),
		Balloon: &chVMBalloon{SizeBytes: 0, DeflateOnOOM: true},
		Vsock:   &chVMVsock{CID: uint64(cid), Socket: vsockSocket},
	}

	if err := cli.VMCreate(ctx, vmCfg); err != nil {
		cleanup()
		return nil, fmt.Errorf("vm: boot %s: vm.create: %w", id, err)
	}
	if err := cli.VMBoot(ctx); err != nil {
		cleanup()
		return nil, fmt.Errorf("vm: boot %s: vm.boot: %w", id, err)
	}

	bridgeCtx, bridgeStop := context.WithCancel(context.Background())
	go func() {
		if err := runBridge(bridgeCtx, bridgeSocket, vsockSocket); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("vm: bridge %s: %v", id, err)
		}
	}()

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
		close(waitCh)
		bridgeStop()
	}()

	rt := sandboxRuntime{
		ID: id, PID: pid, CID: cid,
		APISocket: apiSocket, VsockSocket: vsockSocket,
		BridgeSocket: bridgeSocket, StateDir: sbDir,
	}
	if err := writeRuntime(sbDir, rt); err != nil {
		log.Printf("vm: boot %s: write runtime: %v", id, err)
	}

	h := &vmHandle{rt: rt, proc: cmd, waitCh: waitCh, bridgeStop: bridgeStop, cli: cli}
	b.mu.Lock()
	b.handles[id] = h
	b.mu.Unlock()
	return h, nil
}

func (b *Backend) Adopt(ctx context.Context, id string) (openshell.VMHandle, error) {
	sbDir := filepath.Join(b.cfg.StateDir, id)
	rt, err := readRuntime(sbDir)
	if err != nil {
		return nil, fmt.Errorf("vm: adopt %s: read runtime: %w", id, err)
	}

	if err := syscall.Kill(rt.PID, 0); err != nil {
		return nil, fmt.Errorf("vm: adopt %s: process %d not alive: %w", id, rt.PID, err)
	}

	cli := newCHClient(rt.APISocket)
	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := cli.Ping(pingCtx); err != nil {
		return nil, fmt.Errorf("vm: adopt %s: ping api: %w", id, err)
	}

	bridgeCtx, bridgeStop := context.WithCancel(context.Background())
	go func() {
		if err := runBridge(bridgeCtx, rt.BridgeSocket, rt.VsockSocket); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("vm: bridge (adopted) %s: %v", id, err)
		}
	}()

	waitCh := make(chan error, 1)
	go func() {
		for {
			if err := syscall.Kill(rt.PID, 0); err != nil {
				waitCh <- err
				close(waitCh)
				bridgeStop()
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()

	h := &vmHandle{
		rt:         rt,
		proc:       &exec.Cmd{Process: &os.Process{}},
		waitCh:     waitCh,
		bridgeStop: bridgeStop,
		cli:        cli,
	}
	b.mu.Lock()
	b.handles[id] = h
	b.mu.Unlock()
	return h, nil
}

func buildDisks(images openshell.ImageArtifacts) []chVMDisk {
	disks := []chVMDisk{
		{Path: images.RootfsExt4, ImageType: "Raw", ReadOnly: true},
		{Path: images.OverlayExt4, ImageType: "Raw"},
	}
	if images.ImageExt4 != "" {
		disks = append(disks, chVMDisk{Path: images.ImageExt4, ImageType: "Raw"})
	}
	return disks
}

func spawnCH(ctx context.Context, binary, socketPath string) (*exec.Cmd, error) {
	probeCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	pingErr := newCHClient(socketPath).Ping(probeCtx)
	cancel()
	switch {
	case pingErr == nil:
		return nil, fmt.Errorf("live VMM already bound to %s", socketPath)
	case chIsAbsent(pingErr):
		_ = os.Remove(socketPath)
	default:
		return nil, fmt.Errorf("pre-flight ping %s: %w", socketPath, pingErr)
	}

	var stderr strings.Builder
	cmd := exec.Command(binary, "--api-socket", socketPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Stdout = io.Discard
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}

	pid := cmd.Process.Pid
	cleanup := func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = cmd.Wait()
		_ = os.Remove(socketPath)
	}

	pollCtx, cancel := context.WithTimeout(ctx, chStartTimeout)
	defer cancel()

	c := newCHClient(socketPath)
	for {
		if err := pollCtx.Err(); err != nil {
			tail := stderr.String()
			cleanup()
			if tail != "" {
				return nil, fmt.Errorf("API not ready within %s: %w\n%s", chStartTimeout, err, tail)
			}
			return nil, fmt.Errorf("API not ready within %s: %w", chStartTimeout, err)
		}
		if err := c.Ping(pollCtx); err == nil {
			return cmd, nil
		}
		select {
		case <-pollCtx.Done():
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func writeRuntime(stateDir string, rt sandboxRuntime) error {
	b, err := json.Marshal(rt)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(stateDir, "runtime.json"), b, 0o600)
}

func readRuntime(stateDir string) (sandboxRuntime, error) {
	b, err := os.ReadFile(filepath.Join(stateDir, "runtime.json"))
	if err != nil {
		return sandboxRuntime{}, err
	}
	var rt sandboxRuntime
	return rt, json.Unmarshal(b, &rt)
}

type vmHandle struct {
	rt         sandboxRuntime
	proc       *exec.Cmd
	waitCh     <-chan error
	bridgeStop context.CancelFunc
	cli        *chClient
}

func (h *vmHandle) ID() string            { return h.rt.ID }
func (h *vmHandle) CID() uint32           { return h.rt.CID }
func (h *vmHandle) ControlSocket() string { return h.rt.BridgeSocket }
func (h *vmHandle) Wait() <-chan error    { return h.waitCh }

func (h *vmHandle) Runtime() openshell.RuntimeIdentity {
	return openshell.RuntimeIdentity{
		PID:         h.rt.PID,
		APISocket:   h.rt.APISocket,
		VsockSocket: h.rt.VsockSocket,
		StateDir:    h.rt.StateDir,
	}
}

func (h *vmHandle) DialGuest(ctx context.Context, port uint32) (net.Conn, error) {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "unix", h.rt.VsockSocket)
	if err != nil {
		return nil, fmt.Errorf("vm: dial guest %s port %d: %w", h.rt.ID, port, err)
	}

	deadline := time.Now().Add(vsockHandshakeTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetDeadline(deadline); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vm: dial guest %s port %d: set deadline: %w", h.rt.ID, port, err)
	}

	if _, err := fmt.Fprintf(conn, "CONNECT %d\n", port); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vm: dial guest %s port %d: CONNECT: %w", h.rt.ID, port, err)
	}

	br := bufio.NewReader(conn)
	reply, err := br.ReadString('\n')
	if err != nil {
		conn.Close()
		if err == io.EOF {
			return nil, fmt.Errorf("vm: dial guest %s port %d: EOF (no listener)", h.rt.ID, port)
		}
		return nil, fmt.Errorf("vm: dial guest %s port %d: handshake: %w", h.rt.ID, port, err)
	}
	reply = strings.TrimRight(reply, "\r\n")
	if !strings.HasPrefix(reply, "OK") {
		conn.Close()
		return nil, fmt.Errorf("vm: dial guest %s port %d: rejected: %q", h.rt.ID, port, reply)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vm: dial guest %s port %d: clear deadline: %w", h.rt.ID, port, err)
	}
	return &vsockConn{Conn: conn, r: io.MultiReader(br, conn)}, nil
}

type vsockConn struct {
	net.Conn
	r io.Reader
}

func (c *vsockConn) Read(b []byte) (int, error) { return c.r.Read(b) }

func (h *vmHandle) Stop(ctx context.Context) error {
	shutCtx, cancel := context.WithTimeout(ctx, stopGracefulTimeout)
	_ = h.cli.VMShutdown(shutCtx)
	cancel()

	_ = h.cli.VMMShutdown(ctx)

	select {
	case <-h.waitCh:
		return nil
	case <-time.After(stopGracefulTimeout):
		return h.Kill()
	}
}

func (h *vmHandle) Kill() error {
	if h.proc == nil || h.proc.Process == nil {
		return nil
	}
	pid := h.proc.Process.Pid
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("vm: kill %s: %w", h.rt.ID, err)
	}
	return nil
}

var (
	_ openshell.Backend  = (*Backend)(nil)
	_ openshell.VMHandle = (*vmHandle)(nil)
)
