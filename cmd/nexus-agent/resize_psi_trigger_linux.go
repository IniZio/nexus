//go:build linux

package main

import (
	"context"
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

var psiTriggerSpecs = []struct {
	resource string
	line     string
	trigger  string
}{
	{"memory", "some 100000 500000", resize.TriggerPSIMemory},
	{"memory", "full 50000 500000", resize.TriggerPSIMemory},
	{"cpu", "some 150000 1000000", resize.TriggerPSICPU},
}

func openPSITrigger(resource, line string) (int, error) {
	path := filepath.Join(psiTriggerBasePath, resource)
	fd, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK, 0)
	if err != nil {
		return -1, fmt.Errorf("open %s: %w", path, err)
	}
	if _, err := unix.Write(fd, []byte(line)); err != nil {
		unix.Close(fd)
		return -1, fmt.Errorf("write trigger to %s: %w", path, err)
	}
	return fd, nil
}

func openPSITriggers(con *os.File) []psiTrigger {
	var triggers []psiTrigger
	var loggedErr bool
	for _, spec := range psiTriggerSpecs {
		fd, err := openPSITrigger(spec.resource, spec.line)
		if err != nil {
			if !loggedErr {
				consoleLog(con, "nexus-agent: psi-trigger: %v (CONFIG_PSI off or unprivileged — heartbeat-only streaming)\n", err)
				loggedErr = true
			}
			continue
		}
		triggers = append(triggers, psiTrigger{fd: fd, trigger: spec.trigger})
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
