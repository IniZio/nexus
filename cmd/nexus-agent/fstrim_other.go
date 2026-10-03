//go:build !linux

package main

import "context"

func startFSTrimmer(context.Context) <-chan struct{} { return nil }

func trimAllOnce() uint64 { return 0 }
