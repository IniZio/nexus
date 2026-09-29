package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/IniZio/nexus/internal/core/domain"
)

type virtiofsdProc struct {
	PID  int
	PGID int
}

// scanVirtiofsd is a variable so tests can stub the /proc walk.
var scanVirtiofsd = scanVirtiofsdProc

// scanVirtiofsdProc finds virtiofsd processes serving sandbox id's live mounts
// by their --socket-path argument (<dir>/<id>.vfsN). The socket file itself is
// unlinked once CH connects, so cmdline is the only durable marker.
func scanVirtiofsdProc(id domain.SandboxID) []virtiofsdProc {
	prefix := id.String() + ".vfs"
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var found []virtiofsdProc
	for _, e := range entries {
		pid, convErr := strconv.Atoi(e.Name())
		if convErr != nil {
			continue
		}
		raw, rerr := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if rerr != nil {
			continue
		}
		args := bytes.Split(bytes.TrimRight(raw, "\x00"), []byte{0})
		sock := ""
		for i := 0; i+1 < len(args); i++ {
			if string(args[i]) == "--socket-path" {
				sock = string(args[i+1])
			}
		}
		base := filepath.Base(sock)
		if sock == "" || !strings.HasPrefix(base, prefix) || strings.HasPrefix(base, prefix+"file") {
			continue
		}
		if pgid, ok := procPGID(pid); ok {
			found = append(found, virtiofsdProc{PID: pid, PGID: pgid})
		}
	}
	return found
}

func procPGID(pid int) (int, bool) {
	raw, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, false
	}
	end := bytes.LastIndexByte(raw, ')')
	if end < 0 {
		return 0, false
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 3 {
		return 0, false
	}
	pgid, err := strconv.Atoi(fields[2])
	return pgid, err == nil
}

// virtiofsdOutsideGroup counts procs not in netnsPGID. Such a virtiofsd was
// forked by a pre-fix binary and carries Pdeathsig on its supervisor.
func virtiofsdOutsideGroup(procs []virtiofsdProc, netnsPGID int) int {
	n := 0
	for _, p := range procs {
		if p.PGID != netnsPGID {
			n++
		}
	}
	return n
}
