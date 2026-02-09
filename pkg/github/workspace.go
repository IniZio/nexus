package github

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

func ParseRepoURL(repoString string) (owner string, repo string, err error) {
	repoString = strings.TrimSpace(repoString)

	if strings.Contains(repoString, "://") {
		u, err := url.Parse(repoString)
		if err != nil {
			return "", "", fmt.Errorf("invalid repo URL: %w", err)
		}

		path := strings.TrimPrefix(u.Path, "/")
		path = strings.TrimSuffix(path, ".git")

		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return "", "", fmt.Errorf("invalid repo URL format: expected owner/repo")
		}

		return parts[0], parts[1], nil
	}

	parts := strings.Split(repoString, "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid repo format: expected owner/repo")
	}

	if parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo format: owner and repo must not be empty")
	}

	return parts[0], parts[1], nil
}

func BuildCloneURL(owner, repo string) string {
	return fmt.Sprintf("https://github.com/%s/%s.git", owner, repo)
}

func CloneRepository(url, destDir, token string) error {
	// If token provided and URL is GitHub, inject token for authentication
	if token != "" && strings.Contains(url, "github.com") {
		url = strings.Replace(url, "https://github.com",
			fmt.Sprintf("https://%s@github.com", token), 1)
	}

	args := []string{"clone", url}
	if destDir != "" {
		args = append(args, destDir)
	}

	cmd := exec.Command("git", args...)

	// Disable terminal prompt to prevent hanging on auth failures
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")

	var stderr strings.Builder
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to clone repository: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}
