package clientagent

import (
	"os/exec"
	"strings"
	"testing"
)

// TestRemoteStateReadCommand_SurvivesSSHArgvJoin pins that the remote read is
// one shell-parsable string: ssh space-joins remote argv, so a {"sh","-c",cmd}
// split degrades to `cat` on stdin and yields empty output.
func TestRemoteStateReadCommand_SurvivesSSHArgvJoin(t *testing.T) {
	argv := ExecArgv("host", "/tmp/x.ctl", RemoteStateReadCommand())
	// Everything after the target is what sshd's login shell receives, joined.
	remote := strings.Join(argv[len(argv)-1:], " ")
	if strings.HasPrefix(remote, "sh -c") {
		t.Fatalf("remote command must not be a split sh -c: %q", remote)
	}
	// Simulate the login shell on a host with no forwards.state.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	out, err := exec.Command("sh", "-c", remote).Output()
	if err != nil {
		t.Fatalf("remote command failed: %v", err)
	}
	if strings.TrimSpace(string(out)) != `{"forwards":[]}` {
		t.Errorf("absent state must read as empty forwards, got %q", out)
	}
}
