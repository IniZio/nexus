package cli

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type HerdrProcKind int

const (
	HerdrServer HerdrProcKind = iota
	HerdrRemoteClientBridge
	Nexus3ClientAgent
	HerdrOther
)

// HerdrProc is one herdr-related process from the ps snapshot.
type HerdrProc struct {
	PID     int
	Start   time.Time
	Kind    HerdrProcKind
	Session string
	Argv    string
}

type SocketFinder func(pid int) []string

var lstartFormats = []string{
	"Mon Jan _2 15:04:05 2006",
	"Mon Jan  2 15:04:05 2006",
	"Mon Jan 02 15:04:05 2006",
}

func parseLstart(s string) (time.Time, error) {
	for _, f := range lstartFormats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("herdr procs: unrecognised lstart %q", s)
}

func extractSession(argv string) string {
	fields := strings.Fields(argv)
	for i, f := range fields {
		if f == "--session" && i+1 < len(fields) {
			return fields[i+1]
		}
	}
	return ""
}

func sessionFromSocketPath(path string) (string, bool) {
	const marker = "/sessions/"
	if i := strings.Index(path, marker); i >= 0 {
		after := path[i+len(marker):]
		if j := strings.IndexByte(after, '/'); j > 0 {
			return after[:j], true
		}
		return "", false
	}
	if strings.HasSuffix(path, "/herdr.sock") {
		return "", true
	}
	return "", false
}

func sessionFromSocketPaths(paths []string) (string, bool) {
	for _, p := range paths {
		if s, ok := sessionFromSocketPath(p); ok {
			return s, true
		}
	}
	return "", false
}

// parseSSXlpForPID returns socket paths for pid from `ss -xlp` output (field[4] on matching lines).
func parseSSXlpForPID(ssOut string, pid int) []string {
	needle := fmt.Sprintf("pid=%d,", pid)
	var paths []string
	for _, line := range strings.Split(ssOut, "\n") {
		if !strings.Contains(line, needle) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 5 {
			paths = append(paths, fields[4])
		}
	}
	return paths
}

func parseLsofFnForSockets(lsofOut string) []string {
	var paths []string
	for _, line := range strings.Split(lsofOut, "\n") {
		if strings.HasPrefix(line, "n") {
			paths = append(paths, line[1:])
		}
	}
	return paths
}

// OSSocketFinder returns a SocketFinder backed by the OS.
// Linux: ss -xlp once (lazily memoised). Darwin: lsof -U -a -p <pid> -Fn per pid.
func OSSocketFinder(ctx context.Context, goos string) SocketFinder {
	if goos == "darwin" {
		return func(pid int) []string {
			out, err := exec.CommandContext(ctx, "lsof", "-U", "-a", "-p", strconv.Itoa(pid), "-Fn").Output()
			if err != nil {
				return nil
			}
			return parseLsofFnForSockets(string(out))
		}
	}
	var once sync.Once
	var ssOut string
	return func(pid int) []string {
		once.Do(func() {
			out, _ := exec.CommandContext(ctx, "ss", "-xlp").Output()
			ssOut = string(out)
		})
		return parseSSXlpForPID(ssOut, pid)
	}
}

func classifyProc(argv string) (HerdrProcKind, bool) {
	if strings.Contains(argv, "nexus3-client") && strings.Contains(argv, "herdr") && strings.Contains(argv, "local-agent-startup") {
		return Nexus3ClientAgent, true
	}
	fields := strings.Fields(argv)
	if len(fields) == 0 {
		return HerdrOther, false
	}
	binary := fields[0]
	isHerdrBin := strings.HasSuffix(binary, "herdr") || strings.HasSuffix(binary, "/herdr")
	if !isHerdrBin {
		return HerdrOther, false
	}
	if strings.Contains(argv, "remote-client-bridge") {
		return HerdrRemoteClientBridge, true
	}
	skipNext := false
	for _, f := range fields[1:] {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(f, "--") {
			skipNext = true
			continue
		}
		if f == "server" {
			return HerdrServer, true
		}
	}
	return HerdrOther, true
}

// ParseHerdrProcs parses `ps -axo pid,lstart,args` output.
// findSockets, when non-nil, derives Session for HerdrServer processes from
// their owned UNIX socket path; falls back to argv --session then "unknown-<pid>".
func ParseHerdrProcs(psOutput string, goos string, findSockets SocketFinder) ([]HerdrProc, error) {
	lines := strings.Split(psOutput, "\n")
	var procs []HerdrProc
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 7 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		lstartStr := strings.Join(fields[1:6], " ")
		argv := strings.Join(fields[6:], " ")
		kind, include := classifyProc(argv)
		if !include {
			continue
		}
		t, err := parseLstart(lstartStr)
		if err != nil {
			return nil, err
		}
		session := extractSession(argv)
		if kind == HerdrServer && findSockets != nil {
			paths := findSockets(pid)
			if s, ok := sessionFromSocketPaths(paths); ok {
				session = s
			} else if session == "" {
				session = fmt.Sprintf("unknown-%d", pid)
			}
		}
		procs = append(procs, HerdrProc{
			PID:     pid,
			Start:   t,
			Kind:    kind,
			Session: session,
			Argv:    argv,
		})
	}
	return procs, nil
}

// ListHerdrProcesses runs ps and returns herdr-related processes.
func ListHerdrProcesses(ctx context.Context) ([]HerdrProc, error) {
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid,lstart,args").Output()
	if err != nil {
		return nil, fmt.Errorf("herdr procs ps: %w", err)
	}
	goos := runtime.GOOS
	return ParseHerdrProcs(string(out), goos, OSSocketFinder(ctx, goos))
}
