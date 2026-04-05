//go:build !darwin

package main

import (
	"context"
	"errors"
)

var bootstrapFirecrackerExecContextDarwinFn = func(projectRoot string, execCtx doctorExecContext) error {
	return errors.New("firecracker bootstrap via Lima is only supported on darwin")
}

var runLimaCheckCommandFn = func(ctx context.Context, projectRoot, command string, args []string) (string, error) {
	return "", errors.New("lima check command runner is only supported on darwin")
}
