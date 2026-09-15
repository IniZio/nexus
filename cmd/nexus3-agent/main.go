package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mdlayher/vsock"
	"golang.org/x/sys/unix"

	"github.com/IniZio/nexus3/internal/core/agent"
	"github.com/IniZio/nexus3/internal/core/agent/wire"
	"github.com/IniZio/nexus3/internal/core/driver"
)

var agentBuildTag = "dev" // stamped at build time; default "dev"

func main() {
	// git-ssh: dispatch before PID-1 init
	if len(os.Args) >= 2 && os.Args[1] == "git-ssh" {
		runGitSSHShim(os.Args[2:])
	}

	isPid1 := os.Getpid() == 1

	// hotSwap from NEXUS3_HOT_SWAP env (skip cold-boot init if exec'd by prior agent)
	hotSwap := os.Getenv("NEXUS3_HOT_SWAP") != ""
	os.Unsetenv("NEXUS3_HOT_SWAP")

	// Cold-boot mount pseudo-filesystems (skip if hot-swap)
	if isPid1 && !hotSwap {
		mountGuestFS()
		initPid1Env()
	}

	con := openConsole()
	if con != nil {
		defer con.Close()
	}

	initSlogHandler()

	if hotSwap {
		consoleLog(con, "nexus3-agent: hot-swap boot (pid=%d build=%s); skipping cold-boot init\n", os.Getpid(), agentBuildTag)
	} else {
		consoleLog(con, "nexus3-agent: starting (pid=%d build=%s)\n", os.Getpid(), agentBuildTag)
	}
	if isPid1 && !hotSwap {
		consoleLog(con, "nexus3-agent: /tmp: RAM-backed tmpfs (32 MiB seed; resizer target = max(1 GiB, min(50%%%% MemTotal, 2 GiB)))\n")
	}

	// Cold-boot network/sshd (skip if hot-swap)
	if isPid1 && !hotSwap {
		setupNetwork(con)
		startSSHD(con)
	}

	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	// Parse kernel cmdline args
	var wsMounts []agent.GuestMount
	var memCeilingBytes int64
	var sandboxHandle string // set from --sandbox-handle=<handle> on the kernel cmdline
	var isBuilderRole bool
	var cacheDiskMounts []agent.CacheDiskMount
	var scratchDev string            // set from --scratch-disk=<dev> on the kernel cmdline
	var builderToolRecipeJSON string // set from --tool-recipe=<json>
	var builderTargetArch string     // set from --target-arch=<arch>
	{
		for _, arg := range os.Args[1:] {
			switch {
			case arg == "--builder-role":
				isBuilderRole = true
			case strings.HasPrefix(arg, "--scratch-disk="):
				if dev, ok := parseScratchDiskArg(arg); ok {
					scratchDev = dev
				} else {
					consoleLog(con, "nexus3-agent: ignoring malformed --scratch-disk arg: %q\n", arg)
				}
			case strings.HasPrefix(arg, "--mem-ceiling="):
				v := strings.TrimPrefix(arg, "--mem-ceiling=")
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					memCeilingBytes = n
				} else {
					consoleLog(con, "nexus3-agent: ignoring malformed --mem-ceiling arg: %q\n", arg)
				}
			case strings.HasPrefix(arg, "--cache-disk="):
				pair := strings.TrimPrefix(arg, "--cache-disk=")
				parts := strings.SplitN(pair, ":", 2)
				if len(parts) == 2 && parts[0] != "" && parts[1] != "" {
					cacheDiskMounts = append(cacheDiskMounts, agent.CacheDiskMount{
						Device:    parts[0],
						MountPath: parts[1],
					})
				} else {
					consoleLog(con, "nexus3-agent: ignoring malformed --cache-disk arg: %q\n", arg)
				}
			case strings.HasPrefix(arg, "--tool-recipe="):
				builderToolRecipeJSON = strings.TrimPrefix(arg, "--tool-recipe=")
			case strings.HasPrefix(arg, "--target-arch="):
				builderTargetArch = strings.TrimPrefix(arg, "--target-arch=")
			case strings.HasPrefix(arg, "--sandbox-handle="):
				sandboxHandle = strings.TrimPrefix(arg, "--sandbox-handle=")
			case strings.HasPrefix(arg, "--workspace-mount="):
				m, ok := parseWorkspaceMountArg(arg)
				if ok {
					wsMounts = append(wsMounts, m)
				} else {
					consoleLog(con, "nexus3-agent: ignoring malformed --workspace-mount arg: %q\n", arg)
				}
			default:
				consoleLog(con, "nexus3-agent: WARN: unrecognized cmdline arg %q — host/guest version skew? agent build=%s\n", arg, agentBuildTag)
			}
		}
	}

	// Record scratch-disk for guestBaselineEnv (D-SD-04)
	agentScratchDisk = scratchDev != ""

	// Set guest hostname from --sandbox-handle= or "nexus3"
	if isPid1 {
		hn := sandboxHandle
		if hn == "" {
			hn = "nexus3"
		}
		if err := syscall.Sethostname([]byte(hn)); err != nil {
			consoleLog(con, "nexus3-agent: sethostname(%q): %v\n", hn, err)
		} else {
			consoleLog(con, "nexus3-agent: hostname=%q\n", hn)
		}
	}

	// Cold-boot init (hotSwap gate; mutation-pin: boot_sequence_test.go)
	coldBootCfg := bootConfig{
		mountGuestFS: func() { /* already done above */ },
		wipeScratchDisk: func(dev string) error {
			return wipeMountScratchDisk(dev, con)
		},
		initPid1Env:  func() { /* already done above */ },
		setupNetwork: func() { /* already done above */ },
		startSSHD:    func() { /* already done above */ },
		mountWorkspace: func(mounts []agent.GuestMount) error {
			consoleLog(con, "nexus3-agent: mounting workspace (%d mounts)\n", len(mounts))
			if err := agent.MountWorkspace(mounts); err != nil {
				return err
			}
			consoleLog(con, "nexus3-agent: workspace mounts complete\n")
			return nil
		},
		runBootTasks: func() { runBootTasks(con) },
	}
	if err := runColdBootInit(hotSwap, isPid1, wsMounts, scratchDev, coldBootCfg, nil); err != nil {
		consoleFatal(con, isPid1, "nexus3-agent: workspace mount failed: %v\n", err)
	}

	// Workspace mount selection and disk telemetry
	workspacePath := ""
	var resizableDisks []resizableDisk
	if wsMount, ok, err := selectWorkspaceMount(wsMounts); err != nil {
		consoleFatal(con, isPid1, "nexus3-agent: workspace mount selection: %v\n", err)
	} else if ok {
		workspacePath = wsMount.Target
		resizableDisks = resizableDisksFromWorkspaceMounts(wsMounts)
		if len(resizableDisks) == 0 {
			consoleLog(con, "nexus3-agent: auto-resize: workspace mount %q: cannot derive disk index from device %q; disk telemetry disabled\n", wsMount.Target, wsMount.Device)
		} else {
			consoleLog(con, "nexus3-agent: auto-resize: disk telemetry: %d disk(s) at index(es):", len(resizableDisks))
			for _, d := range resizableDisks {
				consoleLog(con, " [%d]%s", d.Index, d.MountPath)
			}
			consoleLog(con, "\n")
		}
	} else {
		consoleLog(con, "nexus3-agent: auto-resize: NO workspace mount found in %d mount(s); disk telemetry disabled (host may predate the 5-field mount spec)\n", len(wsMounts))
	}
	_ = workspacePath // retained for possible future diagnostic use

	resizableDisks = selectResizableDisks(isBuilderRole, cacheDiskMounts, resizableDisks)
	if isBuilderRole && len(resizableDisks) > 0 {
		consoleLog(con, "nexus3-agent: builder role: resize telemetry: %d cache disk(s)\n", len(resizableDisks))
	}

	// Auto-resize: PID-1 regular-agent only (builder child cannot rebind vsock:3002)
	if !isBuilderRole {
		consoleLog(con, "nexus3-agent: auto-resize: starting services (mem-ceiling=%d)\n", memCeilingBytes)
		startResizeServices(ctx, con, resizableDisks, memCeilingBytes)
	}

	// Builder-role: run build and exit
	if isBuilderRole {
		consoleLog(con, "nexus3-agent: builder role starting (cache disks: %d)\n", len(cacheDiskMounts))
		opts := agent.BuilderRoleOptions{
			CacheDisks: cacheDiskMounts,
			TargetArch: builderTargetArch,
		}
		recipe, err := parseBuilderToolRecipe(builderToolRecipeJSON)
		if err != nil {
			consoleFatal(con, isPid1, "nexus3-agent: builder role: %v\n", err)
		}
		opts.ToolRecipe = recipe
		if err := agent.RunBuilderRole(ctx, opts); err != nil {
			consoleFatal(con, isPid1, "nexus3-agent: builder role: %v\n", err)
		}
		consoleLog(con, "nexus3-agent: builder role complete\n")
		os.Exit(0)
	}

	// Bind vsock listeners
	var ctrlLis, dataLis net.Listener
	{
		var err error
		consoleLog(con, "nexus3-agent: vsock.Listen port %d\n", driver.AgentControlPort)
		ctrlLis, err = vsock.Listen(driver.AgentControlPort, nil)
		if err != nil {
			consoleFatal(con, isPid1, "nexus3-agent: control listener (port %d): %v\n",
				driver.AgentControlPort, err)
		}
		consoleLog(con, "nexus3-agent: control plane listening\n")

		consoleLog(con, "nexus3-agent: vsock.Listen port %d\n", wire.DataPort)
		dataLis, err = vsock.Listen(wire.DataPort, nil)
		if err != nil {
			consoleFatal(con, isPid1, "nexus3-agent: data listener (port %d): %v\n",
				wire.DataPort, err)
		}
	}
	consoleLog(con, "nexus3-agent: data plane listening; running agent\n")

	go startSSHForward(ctx, con)
	go startPortForwardMux(ctx, con)

	a := New(ctrlLis, dataLis)
	if err := a.Run(ctx); err != nil {
		consoleFatal(con, isPid1, "nexus3-agent: run: %v\n", err)
	}
	consoleLog(con, "nexus3-agent: clean shutdown\n")
}

// initPid1Env merges /etc/environment then applies PATH fallback (order matters).
func initPid1Env() {
	for _, kv := range readEtcEnvironment() {
		idx := strings.IndexByte(kv, '=')
		if idx < 0 {
			continue
		}
		k := kv[:idx]
		if os.Getenv(k) == "" {
			os.Setenv(k, kv[idx+1:])
		}
	}
	if os.Getenv("PATH") == "" {
		os.Setenv("PATH", "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")
	}
}

var guestMountFn = func(source, target, fstype string, flags uintptr, data string) error {
	return syscall.Mount(source, target, fstype, flags, data)
}

// mountGuestFS mounts standard pseudo-filesystems; /tmp is 32 MiB seed (PID-1 only).
func mountGuestFS() {
	tryMount := func(src, target, fstype, data string) {
		if err := guestMountFn(src, target, fstype, 0, data); err != nil && err != syscall.EBUSY {
			fmt.Fprintf(os.Stderr, "nexus3-agent: mount %s: %v\n", target, err)
		}
	}

	tryMount("devtmpfs", "/dev", "devtmpfs", "")

	if _, err := os.Stat("/dev/ptmx"); os.IsNotExist(err) {
		if err := unix.Mknod("/dev/ptmx", unix.S_IFCHR|0o666,
			int(unix.Mkdev(5, 2))); err != nil {
			fmt.Fprintf(os.Stderr, "nexus3-agent: mknod /dev/ptmx: %v\n", err)
		}
	}

	if err := os.MkdirAll("/dev/pts", 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "nexus3-agent: mkdir /dev/pts: %v\n", err)
	} else {
		tryMount("devpts", "/dev/pts", "devpts", "")
	}

	if err := os.MkdirAll("/dev/shm", 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "nexus3-agent: mkdir /dev/shm: %v\n", err)
	} else {
		tryMount("tmpfs", "/dev/shm", "tmpfs", "mode=1777,nosuid,nodev,size=512m")
	}

	tryMount("proc", "/proc", "proc", "")
	tryMount("sys", "/sys", "sysfs", "")

	if err := os.MkdirAll("/sys/fs/cgroup", 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "nexus3-agent: mkdir /sys/fs/cgroup: %v\n", err)
	} else {
		tryMount("cgroup2", "/sys/fs/cgroup", "cgroup2", "")
	}

	tryMount("tmpfs", "/tmp", "tmpfs", "size=32m")
}

// openConsole opens /dev/console (nil if unavailable).
func openConsole() *os.File {
	f, _ := os.OpenFile("/dev/console", os.O_WRONLY, 0)
	return f
}

func consoleLog(con *os.File, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if con != nil {
		_, _ = fmt.Fprint(con, msg)
	}
	_, _ = fmt.Fprint(os.Stderr, msg)
}

// consoleFatal logs msg and exits.
func consoleFatal(con *os.File, isPid1 bool, format string, args ...any) {
	consoleLog(con, format, args...)
	time.Sleep(3 * time.Second)
	if isPid1 {
		syscall.Sync()
	}
	os.Exit(1)
}
