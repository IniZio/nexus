//go:build !linux

package main

import (
	"context"
	"os"
)

func newPSIWatcher(ctx context.Context, _ *os.File) <-chan string {
	ch := make(chan string)
	go func() {
		<-ctx.Done()
		close(ch)
	}()
	return ch
}
