//go:build linux

package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
)

const journalSocketPath = "/run/systemd/journal/socket"

// listenDevLog binds /dev/log as a unixgram socket and calls offer per datagram.
func listenDevLog(ctx context.Context, offer func(string)) error {
	return listenDgram(ctx, "/dev/log", offer)
}

// listenJournal binds the native journal socket unless systemd is running.
// It never creates /run/systemd/system.
func listenJournal(ctx context.Context, offer func(string)) error {
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(journalSocketPath), 0o755); err != nil {
		return err
	}
	return listenDgram(ctx, journalSocketPath, offer)
}

func listenDgram(ctx context.Context, path string, offer func(string)) error {
	_ = os.Remove(path)
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return err
	}
	_ = os.Chmod(path, 0o666)
	go func() {
		<-ctx.Done()
		conn.Close()
	}()
	go func() {
		buf := make([]byte, syslogMaxDatagram+1)
		for {
			n, _, err := conn.ReadFromUnix(buf)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				continue
			}
			if n > syslogMaxDatagram {
				continue
			}
			offer(string(buf[:n]))
		}
	}()
	return nil
}
