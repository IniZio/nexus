//go:build linux

package cloudhypervisor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

const spawnParentEnv = "NEXUS_TEST_SPAWN_PARENT"

// runSpawnParentHelper runs in a re-exec'd test binary: it spawns virtiofsd
// into the group named by the env, prints "<pid>" and exits, so the parent
// test can observe the child after its spawner is gone.
func runSpawnParentHelper() {
	pgid, _ := strconv.Atoi(os.Getenv("NEXUS_TEST_PGID"))
	mode := os.Getenv("NEXUS_TEST_MODE")
	ctx := context.Background()
	sock := os.Getenv("NEXUS_TEST_SOCK")
	bin := os.Getenv("NEXUS_TEST_BIN")
	var vp *managedProcess
	var err error
	if mode == "file" {
		vp, err = spawnVirtiofsdForFile(ctx, bin, sock, os.Getenv("NEXUS_TEST_STAGE"), os.Getenv("NEXUS_TEST_HOSTFILE"), false, pgid)
	} else {
		vp, err = spawnVirtiofsd(ctx, bin, sock, os.Getenv("NEXUS_TEST_SHARED"), false, pgid)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "spawn:", err)
		os.Exit(2)
	}
	fmt.Println(vp.pid)
}

func pgidOf(t *testing.T, pid int) int {
	t.Helper()
	g, err := syscall.Getpgid(pid)
	if err != nil {
		t.Fatalf("getpgid(%d): %v", pid, err)
	}
	return g
}

func runSpawnParent(t *testing.T, env ...string) (int, error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(self, "-test.run=^$")
	cmd.Env = append(os.Environ(), append([]string{spawnParentEnv + "=1"}, env...)...)
	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("%w: %s", err, out)
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

func groupLeader(t *testing.T) int {
	t.Helper()
	leader := exec.Command("sleep", "300")
	leader.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := leader.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-leader.Process.Pid, syscall.SIGKILL)
		_ = leader.Wait()
	})
	return leader.Process.Pid
}

func requireSurvivesInGroup(t *testing.T, pid, pgid int) {
	t.Helper()
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	time.Sleep(300 * time.Millisecond)
	if exited, state := processExited(pid); exited {
		t.Fatalf("virtiofsd %d died with its spawner (state %q)", pid, state)
	}
	if got := pgidOf(t, pid); got != pgid {
		t.Fatalf("virtiofsd pgid = %d, want %d", got, pgid)
	}
}

func TestSpawnVirtiofsd_SurvivesSpawnerExitInNetnsGroup(t *testing.T) {
	pgid := groupLeader(t)
	dir := t.TempDir()
	pid, err := runSpawnParent(t,
		"NEXUS_TEST_PGID="+strconv.Itoa(pgid),
		"NEXUS_TEST_BIN="+fakeVirtiofsd(t),
		"NEXUS_TEST_SOCK="+filepath.Join(dir, "a.vfs0"),
		"NEXUS_TEST_SHARED="+dir,
	)
	if err != nil {
		t.Fatal(err)
	}
	requireSurvivesInGroup(t, pid, pgid)
}

func TestSpawnVirtiofsdForFile_SurvivesSpawnerExitInNetnsGroup(t *testing.T) {
	pgid := groupLeader(t)
	dir := t.TempDir()
	host := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(host, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(dir, "stage")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	pid, err := runSpawnParent(t,
		"NEXUS_TEST_MODE=file",
		"NEXUS_TEST_PGID="+strconv.Itoa(pgid),
		"NEXUS_TEST_BIN="+fakeVirtiofsd(t),
		"NEXUS_TEST_SOCK="+filepath.Join(dir, "a.vfs1"),
		"NEXUS_TEST_STAGE="+stage,
		"NEXUS_TEST_HOSTFILE="+host,
	)
	if err != nil {
		t.Fatal(err)
	}
	requireSurvivesInGroup(t, pid, pgid)
}

func TestManagedProcess_KillSharedGroupSparesLeader(t *testing.T) {
	pgid := groupLeader(t)
	dir := t.TempDir()
	vp, err := spawnVirtiofsd(t.Context(), fakeVirtiofsd(t), filepath.Join(dir, "b.vfs0"), dir, false, pgid)
	if err != nil {
		t.Fatal(err)
	}
	vp.kill()
	if exited, _ := processExited(vp.pid); !exited {
		t.Fatal("virtiofsd still alive after kill")
	}
	if exited, _ := processExited(pgid); exited {
		t.Fatal("kill of a shared-group virtiofsd took down the group leader")
	}
}
