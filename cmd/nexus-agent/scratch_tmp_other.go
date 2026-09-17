//go:build !linux

package main

import "os"

// Stub scratch-disk helpers for non-Linux platforms.
// The real implementation is in scratch_tmp_linux.go.

func parseScratchDiskArg(arg string) (string, bool) { return "", false }

func wipeMountScratchDisk(dev string, con *os.File) error { return nil }
