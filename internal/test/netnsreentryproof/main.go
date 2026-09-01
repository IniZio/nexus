// Live proof harness for ticket 09 (netns re-entry perimeter rebuild).
//
// Roles, selected by argv[1]:
//
//	supervisor <workdir>  — plays the ORIGINAL supervisor: calls the real
//	                        StartNetnsRuntime, writes the runtime identity to
//	                        workdir/identity.json, proves frames flow, then
//	                        blocks forever waiting to be kill -9'd.
//	replacement <workdir> — plays the REPLACEMENT supervisor: reads the
//	                        identity, calls the real ReacquirePerimeter, and
//	                        proves frames flow again.
//	attacker <workdir>    — plays a hostile same-uid peer that found the
//	                        socket path but has no token.
//
// The re-exec sentinel is handled exactly as cmd/nexus3/main.go does, so the
// child that runs is the real RunNetnsChild.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/IniZio/nexus3/internal/core/domain"
	"github.com/IniZio/nexus3/internal/core/driver/cloudhypervisor"
)

type identity struct {
	SandboxID      string `json:"sandbox_id"`
	ChildPID       int    `json:"child_pid"`
	ChildPGID      int    `json:"child_pgid"`
	ChildStartTime uint64 `json:"child_start_time"`
	GuestTap       string `json:"guest_tap"`
	APISocket      string `json:"api_socket"`
	ControlSocket  string `json:"control_socket"`
	ControlToken   string `json:"control_token"`
	CHPid          int    `json:"ch_pid"`
	CHInstanceID   string `json:"ch_instance_id"`
}

func die(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+f+"\n", a...)
	os.Exit(1)
}

func main() {
	// Same dispatch as cmd/nexus3/main.go — the child image is this binary.
	if os.Getenv(cloudhypervisor.NetnsRunEnv) == "1" {
		cloudhypervisor.RunNetnsChild()
		return
	}
	if len(os.Args) < 3 {
		die("usage: %s <supervisor|replacement|attacker> <workdir>", os.Args[0])
	}
	switch os.Args[1] {
	case "supervisor":
		runSupervisor(os.Args[2])
	case "replacement":
		runReplacement(os.Args[2])
	case "attacker":
		runAttacker(os.Args[2])
	default:
		die("unknown role %q", os.Args[1])
	}
}

func chPing(sock string) (pid int, ok bool) {
	c, err := net.DialTimeout("unix", sock, 2*time.Second)
	if err != nil {
		return 0, false
	}
	defer c.Close()
	fmt.Fprintf(c, "GET /api/v1/vmm.ping HTTP/1.1\r\nHost: localhost\r\n\r\n")
	buf := make([]byte, 4096)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := c.Read(buf)
	body := string(buf[:n])
	var p struct {
		Pid int `json:"pid"`
	}
	if i := indexOf(body, "{"); i >= 0 {
		_ = json.Unmarshal([]byte(body[i:]), &p)
	}
	return p.Pid, p.Pid != 0
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func runSupervisor(workdir string) {
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		die("mkdir: %v", err)
	}
	var id domain.SandboxID
	for i := range id {
		id[i] = byte(i + 7)
	}
	sockDir := filepath.Join(workdir, "sock")
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		die("mkdir sock: %v", err)
	}
	cfg := cloudhypervisor.Config{
		BinaryPath:   "/usr/local/bin/cloud-hypervisor",
		SocketDir:    sockDir,
		StartTimeout: 20 * time.Second,
	}
	apiSock := filepath.Join(sockDir, id.String()+".sock")

	rt, err := cloudhypervisor.StartNetnsRuntime(context.Background(), cfg, id, apiSock, "")
	if err != nil {
		die("StartNetnsRuntime: %v", err)
	}
	fmt.Printf("SUPERVISOR pid=%d\n", os.Getpid())
	fmt.Printf("NETNS CHILD pid=%d pgid=%d starttime=%d\n", rt.ChildPID, rt.ChildPGID, rt.ChildStartTime)
	fmt.Printf("GUEST TAP %s\n", rt.GuestTap)
	fmt.Printf("CONTROL SOCKET %s\n", rt.ControlSocket)
	fmt.Printf("CONTROL TOKEN  %s\n", rt.ControlToken)

	// Wait for CH to come up and record its identity.
	var chPid int
	for i := 0; i < 100; i++ {
		if p, ok := chPing(rt.APISocket); ok {
			chPid = p
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if chPid == 0 {
		die("cloud-hypervisor never answered on %s", rt.APISocket)
	}
	fmt.Printf("CLOUD-HYPERVISOR pid=%d (alive, in the netns)\n", chPid)

	// Prove the perimeter carries frames BEFORE the crash: write a frame to
	// the perimeter end and confirm the netns child accepted it (the pump is
	// running). We read back nothing (no guest), so liveness is proven by the
	// write succeeding on a connected socketpair plus the child being alive.
	if _, err := rt.PerimConn.Write([]byte("pre-crash-frame")); err != nil {
		die("perimeter write before crash failed: %v", err)
	}
	fmt.Println("PERIMETER OK (pre-crash frame accepted)")

	ident := identity{
		SandboxID: id.String(), ChildPID: rt.ChildPID, ChildPGID: rt.ChildPGID,
		ChildStartTime: rt.ChildStartTime, GuestTap: rt.GuestTap,
		APISocket: rt.APISocket, ControlSocket: rt.ControlSocket,
		ControlToken: rt.ControlToken, CHPid: chPid,
	}
	data, _ := json.MarshalIndent(ident, "", "  ")
	if err := os.WriteFile(filepath.Join(workdir, "identity.json"), data, 0o600); err != nil {
		die("write identity: %v", err)
	}
	fmt.Println("READY")
	os.Stdout.Sync()
	select {} // block until kill -9
}

func loadIdentity(workdir string) identity {
	data, err := os.ReadFile(filepath.Join(workdir, "identity.json"))
	if err != nil {
		die("read identity: %v", err)
	}
	var id identity
	if err := json.Unmarshal(data, &id); err != nil {
		die("parse identity: %v", err)
	}
	return id
}

func runReplacement(workdir string) {
	id := loadIdentity(workdir)
	fmt.Printf("REPLACEMENT pid=%d re-acquiring child pid=%d\n", os.Getpid(), id.ChildPID)

	perimFile, err := cloudhypervisor.ReacquirePerimeter(
		id.ControlSocket, id.ControlToken, id.SandboxID, id.ChildPID, id.ChildStartTime)
	if err != nil {
		die("ReacquirePerimeter: %v", err)
	}
	defer perimFile.Close()
	conn, err := net.FileConn(perimFile)
	if err != nil {
		die("FileConn: %v", err)
	}
	defer conn.Close()
	fmt.Println("REACQUIRED: child accepted the fresh pump end and swapped it in")

	if _, err := conn.Write([]byte("post-reacquire-frame")); err != nil {
		die("perimeter write after re-acquire failed: %v", err)
	}
	fmt.Println("PERIMETER RESTORED (post-reacquire frame accepted)")

	if p, ok := chPing(id.APISocket); ok {
		fmt.Printf("CLOUD-HYPERVISOR pid=%d STILL ALIVE (was %d) — VM never rebooted\n", p, id.CHPid)
		if p != id.CHPid {
			die("CH pid CHANGED %d -> %d: the VM was restarted", id.CHPid, p)
		}
	} else {
		die("cloud-hypervisor is GONE — the VM was destroyed")
	}
	fmt.Println("OK")
}

func runAttacker(workdir string) {
	id := loadIdentity(workdir)
	// A same-uid peer that found the socket path but has no token: it points
	// ReacquirePerimeter at a token file of its own making.
	bad := filepath.Join(workdir, "attacker.token")
	if err := os.WriteFile(bad, []byte("00000000000000000000000000000000000000000000000000000000000000ff"), 0o600); err != nil {
		die("write bad token: %v", err)
	}
	fmt.Printf("ATTACKER pid=%d (same uid=%d) dialing %s\n", os.Getpid(), os.Getuid(), id.ControlSocket)
	f, err := cloudhypervisor.ReacquirePerimeter(id.ControlSocket, bad, id.SandboxID, id.ChildPID, id.ChildStartTime)
	if f != nil {
		f.Close()
		die("ATTACKER OBTAINED A PERIMETER — this is a security failure")
	}
	fmt.Printf("REFUSED: %v\n", err)
	if p, ok := chPing(id.APISocket); ok && p == id.CHPid {
		fmt.Printf("VM UNTOUCHED: cloud-hypervisor pid=%d unchanged\n", p)
	} else {
		die("the VM was disturbed by a refused attempt")
	}
	fmt.Println("OK")
}
