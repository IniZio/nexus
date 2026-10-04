//go:build !linux

package main

import (
	"errors"
	"time"
)

func setRealtimeClock(time.Time) error { return errors.New("set clock: unsupported platform") }
