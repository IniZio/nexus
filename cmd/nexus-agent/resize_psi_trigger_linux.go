//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"

	"github.com/IniZio/nexus/internal/core/resize"
)

var psiTriggerBasePath = "/proc/pressure"

type psiTrigger struct {
	fd      int
	trigger string
}

// fast needs CAP_SYS_RESOURCE (sub-2s window); slow is the unprivileged-legal
// form since Linux 6.5 (window a multiple of 2 s).
type psiTriggerSpec struct {
	resource string
	fast     string
	slow     string
	trigger  string
}

var psiTriggerSpecs = []psiTriggerSpec{
	{"memory", "some 100000 500000", "some 200000 2000000", resize.TriggerPSIMemory},
	{"memory", "full 50000 500000", "full 100000 2000000", resize.TriggerPSIMemory},
	{"cpu", "some 150000 1000000", "some 300000 2000000", resize.TriggerPSICPU},
}

func errnoName(err error) string {
	var errno unix.Errno
	if errors.As(err, &errno) {
		return unix.ErrnoName(errno)
	}
	return "non-errno"
}

// openPSITrigger arms one trigger. The line is newline-terminated because
// psi_write NUL-terminates its copy at buf[nbytes-1], discarding the final
// byte of whatever was written.
func openPSITrigger(resource, line string) (int, error) {
	path := filepath.Join(psiTriggerBasePath, resource)
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s for %q: %w (%s)", path, line, err, errnoName(err))
	}
	if _, err := unix.Write(fd, []byte(line+"\n")); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("write %q to %s: %w (%s)", line, path, err, errnoName(err))
	}
	return fd, nil
}

func openPSITriggers(con *os.File) []psiTrigger {
	var triggers []psiTrigger
	for _, spec := range psiTriggerSpecs {
		fd, fastErr := openPSITrigger(spec.resource, spec.fast)
		if fastErr == nil {
			consoleLog(con, "nexus-agent: psi-trigger: %s %q armed (fast)\n", spec.resource, spec.fast)
			triggers = append(triggers, psiTrigger{fd: fd, trigger: spec.trigger})
			continue
		}
		fd, slowErr := openPSITrigger(spec.resource, spec.slow)
		if slowErr == nil {
			consoleLog(con, "nexus-agent: psi-trigger: %s %q armed (slow, unprivileged window); fast attempt failed: %v\n", spec.resource, spec.slow, fastErr)
			triggers = append(triggers, psiTrigger{fd: fd, trigger: spec.trigger})
			continue
		}
		consoleLog(con, "nexus-agent: psi-trigger: %s not armed; fast attempt: %v; slow attempt: %v\n", spec.resource, fastErr, slowErr)
	}
	if len(triggers) == 0 {
		consoleLog(con, "nexus-agent: psi-trigger: no trigger armed — heartbeat-only streaming\n")
	}
	return triggers
}

func runPSIWatcher(ctx context.Context, triggers []psiTrigger, ch chan<- string) {
	if len(triggers) == 0 {
		<-ctx.Done()
		return
	}
	fds := make([]unix.PollFd, len(triggers))
	for i, t := range triggers {
		fds[i] = unix.PollFd{Fd: int32(t.fd), Events: unix.POLLPRI}
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		n, err := unix.Poll(fds, 200)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if n == 0 {
			continue
		}
		for i, fd := range fds {
			if fd.Revents&unix.POLLPRI != 0 {
				select {
				case ch <- triggers[i].trigger:
				case <-ctx.Done():
					return
				}
			}
		}
	}
}

func newPSIWatcher(ctx context.Context, con *os.File) <-chan string {
	ch := make(chan string, 4)
	triggers := openPSITriggers(con)
	go func() {
		defer close(ch)
		defer func() {
			for _, t := range triggers {
				unix.Close(t.fd)
			}
		}()
		runPSIWatcher(ctx, triggers, ch)
	}()
	return ch
}
