package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

func init() {
	Register(Command{
		Name:    "controller",
		Summary: "Run the chat controller (execs nexus-controller)",
		Run:     runController,
	})
}

// controllerExecutable is called to get the path of the current executable.
// Overridable in tests.
var controllerExecutable = os.Executable

// lookupController resolves the nexus-controller binary path.
func lookupController() (string, error) {
	self, err := controllerExecutable()
	if err == nil {
		if resolved, err2 := filepath.EvalSymlinks(self); err2 == nil {
			self = resolved
		}
		sibling := filepath.Join(filepath.Dir(self), "nexus-controller")
		if info, err2 := os.Stat(sibling); err2 == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return sibling, nil
		}
	}
	return exec.LookPath("nexus-controller")
}

func runController(ctx context.Context, args []string, _ *Output) error {
	bin, err := lookupController()
	if err != nil {
		dir := "<unknown>"
		if self, e2 := controllerExecutable(); e2 == nil {
			if resolved, e3 := filepath.EvalSymlinks(self); e3 == nil {
				self = resolved
			}
			dir = filepath.Dir(self)
		}
		return &CodedError{
			Code: ErrCodeInternalError,
			Msg:  "nexus-controller not found next to " + dir + " or on PATH; install it alongside nexus",
			Err:  err,
		}
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return &ExitCodeError{Code: int32(exitErr.ExitCode())}
		}
		return &CodedError{Code: ErrCodeInternalError, Msg: "controller: " + err.Error(), Err: err}
	}
	return nil
}
