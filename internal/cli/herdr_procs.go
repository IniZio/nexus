package cli

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
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

// ParseHerdrProcs parses `ps -axo pid,lstart,args` output on linux or darwin.
// goos is "linux" or "darwin" (reserved for future format divergence).
func ParseHerdrProcs(psOutput string, _ string) ([]HerdrProc, error) {
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
		procs = append(procs, HerdrProc{
			PID:     pid,
			Start:   t,
			Kind:    kind,
			Session: extractSession(argv),
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
	return ParseHerdrProcs(string(out), "")
}
