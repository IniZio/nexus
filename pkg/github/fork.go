package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ForkRequest represents a GitHub fork creation request
type ForkRequest struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
}

// ForkResponse represents a GitHub fork response
type ForkResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
	Private  bool   `json:"private"`
	HTMLURL  string `json:"html_url"`
	CloneURL string `json:"clone_url"`
}

// Repository represents a GitHub repository
type Repository struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
	Owner    struct {
		Login string `json:"login"`
		ID    int64  `json:"id"`
	} `json:"owner"`
	Private  bool   `json:"private"`
	HTMLURL  string `json:"html_url"`
	CloneURL string `json:"clone_url"`
}

// GetUserRepos lists all repositories owned by the authenticated user
func GetUserRepos(ctx context.Context, token string) ([]Repository, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user/repos", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("token %s", token))
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get user repos: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error (%d): %s", resp.StatusCode, string(body))
	}

	var repos []Repository
	if err := json.NewDecoder(resp.Body).Decode(&repos); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return repos, nil
}

// ForkRepository forks a repository to the authenticated user's account
// Idempotent: if fork already exists, returns the existing fork
func ForkRepository(ctx context.Context, token, owner, repo string) (*Repository, error) {
	// First check if we already have this fork
	userRepos, err := GetUserRepos(ctx, token)
	if err == nil {
		// Look for an existing fork of this repo
		for _, r := range userRepos {
			if r.Name == repo && r.Private {
				// Verify this is a fork of the target repo
				parent, err := getRepositoryParent(ctx, token, owner, repo)
				if err == nil && parent != nil {
					return &r, nil
				}
			}
		}
	}

	// Create fork if it doesn't exist
	client := &http.Client{Timeout: 10 * time.Second}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s/forks", owner, repo)
	req, err := http.NewRequestWithContext(ctx, "POST", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create fork request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("token %s", token))
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fork repository: %w", err)
	}
	defer resp.Body.Close()

	// Handle 422 Conflict (fork already exists)
	if resp.StatusCode == http.StatusUnprocessableEntity {
		// Fork already exists, retrieve it
		userRepos, err := GetUserRepos(ctx, token)
		if err != nil {
			return nil, fmt.Errorf("fork exists but failed to retrieve it: %w", err)
		}
		for _, r := range userRepos {
			if r.Name == repo {
				return &r, nil
			}
		}
		return nil, fmt.Errorf("fork exists but could not be found in user repos")
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error (%d): %s", resp.StatusCode, string(body))
	}

	var forkResp Repository
	if err := json.NewDecoder(resp.Body).Decode(&forkResp); err != nil {
		return nil, fmt.Errorf("failed to decode fork response: %w", err)
	}

	return &forkResp, nil
}

// IsForkOf verifies if a repository is a fork of another
func IsForkOf(ctx context.Context, token, forkOwner, forkRepo, origOwner, origRepo string) (bool, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", forkOwner, forkRepo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("token %s", token))
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("failed to get repository info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("GitHub API error (%d)", resp.StatusCode)
	}

	var repo struct {
		Fork   bool `json:"fork"`
		Parent struct {
			Owner struct {
				Login string `json:"login"`
			} `json:"owner"`
			Name string `json:"name"`
		} `json:"parent"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&repo); err != nil {
		return false, fmt.Errorf("failed to decode response: %w", err)
	}

	if !repo.Fork {
		return false, nil
	}

	return repo.Parent.Owner.Login == origOwner && repo.Parent.Name == origRepo, nil
}

// GetForkURL returns the HTTPS clone URL for a repository
func GetForkURL(ctx context.Context, token, owner, repo string) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	apiURL := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("token %s", token))
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to get repository info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("GitHub API error (%d): %s", resp.StatusCode, string(body))
	}

	var repository Repository
	if err := json.NewDecoder(resp.Body).Decode(&repository); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	// Return HTTPS URL with token embedded
	if repository.CloneURL != "" {
		// Replace git:// with https:// if needed, and format with token
		cloneURL := repository.CloneURL
		if strings.HasPrefix(cloneURL, "git@") {
			// Convert git SSH URL to HTTPS
			parts := strings.Split(cloneURL, ":")
			if len(parts) == 2 {
				parts = strings.Split(parts[1], "/")
				if len(parts) == 2 {
					return fmt.Sprintf("https://%s@github.com/%s/%s.git", token, parts[0], parts[1]), nil
				}
			}
		}
		return cloneURL, nil
	}

	return fmt.Sprintf("https://github.com/%s/%s.git", repository.Owner.Login, repository.Name), nil
}

// GetRepositoryInfo retrieves information about a repository
func GetRepositoryInfo(ctx context.Context, token, owner, repo string) (*Repository, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("token %s", token))
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get repository info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GitHub API error (%d): %s", resp.StatusCode, string(body))
	}

	var repository Repository
	if err := json.NewDecoder(resp.Body).Decode(&repository); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &repository, nil
}

// getRepositoryParent retrieves parent repository info for a fork
func getRepositoryParent(ctx context.Context, token, owner, repo string) (*Repository, error) {
	client := &http.Client{Timeout: 10 * time.Second}

	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("token %s", token))
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get repository info: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API error (%d)", resp.StatusCode)
	}

	var data struct {
		Fork   bool        `json:"fork"`
		Parent *Repository `json:"parent"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return data.Parent, nil
}
