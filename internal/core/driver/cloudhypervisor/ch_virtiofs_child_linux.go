//go:build linux

package cloudhypervisor

import (
	"fmt"
	"os"
	"syscall"
)

const (
	// VirtiofsRunEnv is the sentinel environment variable checked in cmd/nexus/main.go.
	// When set to "1" the binary runs as the virtiofsd file-mount child.
	VirtiofsRunEnv = "NEXUS_VIRTIOFS_RUN"

	virtiofsEnvHostFile   = "NEXUS_VFS_HOSTFILE"
	virtiofsEnvBindTarget = "NEXUS_VFS_BINDTARGET"
	virtiofsEnvBin        = "NEXUS_VFS_BIN"
	virtiofsEnvSharedDir  = "NEXUS_VFS_SHAREDDIR"
	virtiofsEnvSocket     = "NEXUS_VFS_SOCKET"
	virtiofsEnvReadOnly   = "NEXUS_VFS_READONLY"
	virtiofsEnvFakeOwner  = "NEXUS_VFS_FAKEOWNER"
)

// virtiofsdChildAttr returns SysProcAttr that places the child in a new
// user + mount namespace with uid/gid 0 mapped to the caller's uid/gid.
// Setpgid:true with a non-zero pgid joins that existing group (the netns
// child's, so virtiofsd shares the VM's lifetime); pgid 0 makes the child its
// own group leader.
func virtiofsdChildAttr(pgid int) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Cloneflags: syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getuid(), Size: 1},
		},
		GidMappings: []syscall.SysProcIDMap{
			{ContainerID: 0, HostID: os.Getgid(), Size: 1},
		},
		GidMappingsEnableSetgroups: false,
		Setpgid:                    true,
		Pgid:                       pgid,
	}
}

// RunVirtiofsdChild is the child-side entry point for spawnVirtiofsdForFile.
// It is invoked when the re-exec'd process detects VirtiofsRunEnv=1.
// It bind-mounts the host file into the stage directory (private mount
// namespace, root-mapped user namespace) then exec's virtiofsd in place.
func RunVirtiofsdChild() {
	hostFile := os.Getenv(virtiofsEnvHostFile)
	bindTarget := os.Getenv(virtiofsEnvBindTarget)
	virtiofsdBin := os.Getenv(virtiofsEnvBin)
	sharedDir := os.Getenv(virtiofsEnvSharedDir)
	socket := os.Getenv(virtiofsEnvSocket)
	readOnly := os.Getenv(virtiofsEnvReadOnly) == "1"
	fakeOwner := os.Getenv(virtiofsEnvFakeOwner) == "1"

	if err := syscall.Mount(hostFile, bindTarget, "", syscall.MS_BIND, ""); err != nil {
		fmt.Fprintf(os.Stderr, "virtiofsd child: bind %s → %s: %v\n", hostFile, bindTarget, err)
		os.Exit(1)
	}

	args := virtiofsdArgs(sharedDir, socket, readOnly, fakeOwner)
	argv := append([]string{virtiofsdBin}, args...)
	if err := syscall.Exec(virtiofsdBin, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "virtiofsd child: exec %s: %v\n", virtiofsdBin, err)
		os.Exit(1)
	}
}
