package gitssh

import (
	"fmt"
	"strings"
)

type ParsedCommand struct {
	Service   string // "git-receive-pack" or "git-upload-pack"
	GitHost   string // "git@github.com" (user@host from argv[0])
	BareHost  string // "github.com" (host only, lower-cased)
	OwnerRepo string // "owner/repo" (normalised, no leading / or .git)
	RawPath   string // "/owner/repo.git" as passed by git (no quotes)
}

func ParseCommand(argv []string) (ParsedCommand, error) {
	for _, arg := range argv {
		if strings.HasPrefix(arg, "-") {
			return ParsedCommand{}, fmt.Errorf("argv element looks like an SSH option: %q", arg)
		}
	}

	if len(argv) < 2 {
		return ParsedCommand{}, fmt.Errorf("argv too short: need at least 2 elements, got %d", len(argv))
	}

	hostArg := argv[0]
	cmdArg := argv[1]

	var service string
	var rest string
	switch {
	case strings.HasPrefix(cmdArg, "git-receive-pack "):
		service = "git-receive-pack"
		rest = strings.TrimPrefix(cmdArg, "git-receive-pack ")
	case strings.HasPrefix(cmdArg, "git-upload-pack "):
		service = "git-upload-pack"
		rest = strings.TrimPrefix(cmdArg, "git-upload-pack ")
	default:
		return ParsedCommand{}, fmt.Errorf("argv[1] must start with git-receive-pack or git-upload-pack, got: %q", cmdArg)
	}

	pathStr := rest
	if len(pathStr) >= 2 {
		if (pathStr[0] == '\'' && pathStr[len(pathStr)-1] == '\'') ||
			(pathStr[0] == '"' && pathStr[len(pathStr)-1] == '"') {
			pathStr = pathStr[1 : len(pathStr)-1]
		}
	}

	if pathStr == "" {
		return ParsedCommand{}, fmt.Errorf("path is empty")
	}

	// scp-style URLs (git@host:owner/repo.git) omit the leading slash; canonicalise.
	if pathStr[0] != '/' {
		pathStr = "/" + pathStr
	}
	rawPath := pathStr

	stripped := strings.TrimPrefix(rawPath, "/")
	segments := strings.Split(stripped, "/")
	if len(segments) < 2 {
		return ParsedCommand{}, fmt.Errorf("path does not contain owner/repo: %q", rawPath)
	}
	// Exactly owner/repo(.git): a third segment would pass the policy check on
	// segments[0..1] while RawPath forwards the extra segment verbatim.
	if len(segments) > 2 {
		return ParsedCommand{}, fmt.Errorf("path has more than owner/repo segments: %q", rawPath)
	}
	owner := segments[0]
	repo := strings.TrimSuffix(segments[1], ".git")
	if owner == "" || repo == "" {
		return ParsedCommand{}, fmt.Errorf("empty owner or repo in path: %q", rawPath)
	}

	bareHost := hostArg
	if idx := strings.Index(hostArg, "@"); idx >= 0 {
		bareHost = hostArg[idx+1:]
	}
	bareHost = strings.ToLower(bareHost)

	return ParsedCommand{
		Service:   service,
		GitHost:   hostArg,
		BareHost:  bareHost,
		OwnerRepo: owner + "/" + repo,
		RawPath:   rawPath,
	}, nil
}
