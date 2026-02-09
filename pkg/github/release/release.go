package release

import (
	"context"
	"fmt"
	"os"

	"github.com/google/go-github/v70/github"
)

// ReleaseConfig holds configuration for the ReleaseManager
type ReleaseConfig struct {
	Token string
	Owner string
	Repo  string
}

// ReleaseManager handles GitHub release operations
type ReleaseManager struct {
	client *github.Client
	config ReleaseConfig
}

// NewReleaseConfig creates a new ReleaseConfig from environment variables
func NewReleaseConfig() ReleaseConfig {
	return ReleaseConfig{
		Token: os.Getenv("GITHUB_TOKEN"),
		Owner: os.Getenv("GITHUB_OWNER"),
		Repo:  os.Getenv("GITHUB_REPO"),
	}
}

// NewReleaseManager creates a new ReleaseManager
func NewReleaseManager(config ReleaseConfig) (*ReleaseManager, error) {
	if config.Token == "" {
		return nil, ErrNoGitHubToken
	}
	if config.Owner == "" {
		return nil, ErrNoGitHubOwner
	}
	if config.Repo == "" {
		return nil, ErrNoGitHubRepo
	}

	client := github.NewClient(nil).WithAuthToken(config.Token)

	return &ReleaseManager{
		client: client,
		config: config,
	}, nil
}

// Validate validates the configuration
func (c *ReleaseConfig) Validate() error {
	if c.Token == "" {
		return ErrNoGitHubToken
	}
	if c.Owner == "" {
		return ErrNoGitHubOwner
	}
	if c.Repo == "" {
		return ErrNoGitHubRepo
	}
	return nil
}

// CreateRelease creates a new release on GitHub
// Parameters:
//   - ctx: context.Context for the request
//   - tagName: the tag name for the release
//   - releaseName: the name of the release (if empty, uses tagName)
//   - body: the release notes/body content
//   - draft: whether this is a draft release
//
// Returns the created RepositoryRelease or an error
func (r *ReleaseManager) CreateRelease(ctx context.Context, tagName, releaseName, body string, draft bool) (*github.RepositoryRelease, error) {
	release := &github.RepositoryRelease{
		TagName: &tagName,
		Draft:   &draft,
		Name:    &releaseName,
		Body:    &body,
	}

	createdRelease, _, err := r.client.Repositories.CreateRelease(ctx, r.config.Owner, r.config.Repo, release)
	if err != nil {
		return nil, &ErrReleaseCreationFailed{Underlying: err}
	}

	return createdRelease, nil
}

// GetLatestRelease retrieves the latest release from GitHub
// Returns the latest RepositoryRelease or an error
func (r *ReleaseManager) GetLatestRelease(ctx context.Context) (*github.RepositoryRelease, error) {
	release, _, err := r.client.Repositories.GetLatestRelease(ctx, r.config.Owner, r.config.Repo)
	if err != nil {
		return nil, fmt.Errorf("failed to get latest release: %w", err)
	}

	return release, nil
}

// GetReleaseByTag retrieves a specific release by tag name
// Returns the RepositoryRelease or an error
func (r *ReleaseManager) GetReleaseByTag(ctx context.Context, tagName string) (*github.RepositoryRelease, error) {
	release, _, err := r.client.Repositories.GetReleaseByTag(ctx, r.config.Owner, r.config.Repo, tagName)
	if err != nil {
		return nil, fmt.Errorf("failed to get release by tag %s: %w", tagName, err)
	}

	return release, nil
}

// ListReleases lists releases for the repository
// Returns a list of RepositoryRelease or an error
func (r *ReleaseManager) ListReleases(ctx context.Context, perPage int) ([]*github.RepositoryRelease, error) {
	if perPage <= 0 {
		perPage = 100
	}

	opts := &github.ListOptions{PerPage: perPage}
	var releases []*github.RepositoryRelease

	for {
		list, resp, err := r.client.Repositories.ListReleases(ctx, r.config.Owner, r.config.Repo, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list releases: %w", err)
		}
		releases = append(releases, list...)
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}

	return releases, nil
}

// UploadReleaseAsset uploads an asset to an existing release (placeholder)
// The go-github API requires *os.File which complicates implementation
func (r *ReleaseManager) UploadReleaseAsset(ctx context.Context, releaseID int64, name string, content []byte) (*github.ReleaseAsset, error) {
	// Create temp file for upload
	tmpFile, err := os.CreateTemp("", "release-asset-*.tmp")
	if err != nil {
		return nil, &ErrAssetUploadFailed{Underlying: err}
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := tmpFile.Write(content); err != nil {
		return nil, &ErrAssetUploadFailed{Underlying: err}
	}
	tmpFile.Close()

	asset, _, err := r.client.Repositories.UploadReleaseAsset(ctx, r.config.Owner, r.config.Repo, releaseID, &github.UploadOptions{Name: name}, tmpFile)
	if err != nil {
		return nil, &ErrAssetUploadFailed{Underlying: err}
	}

	return asset, nil
}
