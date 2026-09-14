package gitssh

import (
	"fmt"
	"strings"
)

// ParsedCommand holds the validated fields extracted from a git SSH argv.
type ParsedCommand struct {
	Service   string // "git-receive-pack" or "git-upload-pack"
	GitHost   string // "git@github.com" (user@host from argv[0])
	BareHost  string // "github.com" (host only, lower-cased)
	OwnerRepo string // "owner/repo" (normalised, no leading / or .git)
	RawPath   string // "/owner/repo.git" as passed by git (no quotes)
}

// ParseCommand validates and parses the argv list from a gitssh.Request.
//
// Format expected (from git's GIT_SSH_COMMAND):
//
//	argv[0]     = "[user@]host"
//	argv[1]     = "git-receive-pack '/path'" or "git-upload-pack '/path'"
//	              (the full command is a single shell token with quoted path)
//
// Rejection rules (each returns a descriptive error):
//   - Any argv element starting with "-" (SSH option injection)
//   - Less than 2 elements
//   - argv[1] does not start with "git-receive-pack " or "git-upload-pack "
//   - Path is empty or starts with anything other than "/"
//
// Path is unquoted (single or double quotes stripped) before storage in RawPath.
// OwnerRepo is derived: strip leading "/", split on "/", take first two
// segments, strip ".git" from the second segment.
func ParseCommand(argv []string) (ParsedCommand, error) {
	// Check for flag injection in any element.
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

	// Determine service.
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

	// Unquote path portion.
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

	// Normalise rawPath: git SSH URLs (git@host:owner/repo.git) produce a path
	// WITHOUT a leading slash; git HTTPS-over-SSH (/owner/repo.git) uses one.
	// Accept both; canonicalise to always have a leading "/" for RawPath.
	if pathStr[0] != '/' {
		pathStr = "/" + pathStr
	}
	rawPath := pathStr

	// Derive owner/repo from path.
	stripped := strings.TrimPrefix(rawPath, "/")
	segments := strings.SplitN(stripped, "/", 3)
	if len(segments) < 2 {
		return ParsedCommand{}, fmt.Errorf("path does not contain owner/repo: %q", rawPath)
	}
	owner := segments[0]
	repo := strings.TrimSuffix(segments[1], ".git")
	if owner == "" || repo == "" {
		return ParsedCommand{}, fmt.Errorf("empty owner or repo in path: %q", rawPath)
	}

	// Derive bareHost from hostArg (strip user@ prefix if present).
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
