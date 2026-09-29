package pkg

import (
	"context"
	"os/exec"
)

const unlisted = "curl"

func f(ctx context.Context, dyn string) {
	exec.Command("virtiofsd", "--help")
	exec.CommandContext(ctx, unlisted)
	_, _ = exec.LookPath("wget")
	_, _ = exec.LookPath("ok-tool")
	exec.Command(dyn)
}
