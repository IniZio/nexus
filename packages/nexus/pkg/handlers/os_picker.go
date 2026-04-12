package handlers

import (
	"context"
	"os/exec"
	"runtime"
	"strings"

	rpckit "github.com/inizio/nexus/packages/nexus/pkg/rpcerrors"
)

type PickDirectoryParams struct {
	Prompt string `json:"prompt,omitempty"`
}

type PickDirectoryResult struct {
	Path      string `json:"path,omitempty"`
	Cancelled bool   `json:"cancelled"`
}

func HandlePickDirectory(_ context.Context, p PickDirectoryParams) (*PickDirectoryResult, *rpckit.RPCError) {
	if runtime.GOOS != "darwin" {
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "native folder picker is only supported on macOS"}
	}

	prompt := strings.TrimSpace(p.Prompt)
	if prompt == "" {
		prompt = "Select repository folder"
	}

	cmd := exec.Command(
		"osascript",
		"-e", "set selectedFolder to choose folder with prompt "+appleScriptString(prompt),
		"-e", "POSIX path of selectedFolder",
	)
	output, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			msg := strings.TrimSpace(string(ee.Stderr))
			if strings.Contains(msg, "User canceled") {
				return &PickDirectoryResult{Cancelled: true}, nil
			}
		}
		return nil, &rpckit.RPCError{Code: rpckit.ErrInternalError.Code, Message: "failed to open folder picker"}
	}

	path := strings.TrimSpace(string(output))
	if path == "" {
		return &PickDirectoryResult{Cancelled: true}, nil
	}

	return &PickDirectoryResult{Path: path, Cancelled: false}, nil
}

func appleScriptString(value string) string {
	escaped := strings.ReplaceAll(value, "\\", "\\\\")
	escaped = strings.ReplaceAll(escaped, "\"", "\\\"")
	return "\"" + escaped + "\""
}
