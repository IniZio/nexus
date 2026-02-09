package release

import "errors"

// ErrNoGitHubToken indicates that the GITHUB_TOKEN environment variable is not set
var ErrNoGitHubToken = errors.New("GITHUB_TOKEN environment variable not set")

// ErrNoGitHubOwner indicates that the GITHUB_OWNER environment variable is not set
var ErrNoGitHubOwner = errors.New("GITHUB_OWNER environment variable not set")

// ErrNoGitHubRepo indicates that the GITHUB_REPO environment variable is not set
var ErrNoGitHubRepo = errors.New("GITHUB_REPO environment variable not set")

// ErrNoConventionalCommits indicates that no conventional commits were found in the specified range
var ErrNoConventionalCommits = errors.New("no conventional commits found in the specified range")

// ErrInvalidTagRange indicates that the tag range is invalid (both tags empty)
var ErrInvalidTagRange = errors.New("invalid tag range: at least one tag must be specified")

// ErrReleaseAlreadyExists indicates that a release with the same tag already exists
var ErrReleaseAlreadyExists = errors.New("release with this tag already exists")

// ErrTagNotFound indicates that the specified tag was not found
var ErrTagNotFound = errors.New("tag not found")

// ErrReleaseCreationFailed indicates that the release creation failed
type ErrReleaseCreationFailed struct {
	Underlying error
}

func (e *ErrReleaseCreationFailed) Error() string {
	return "release creation failed: " + e.Underlying.Error()
}

func (e *ErrReleaseCreationFailed) Unwrap() error {
	return e.Underlying
}

// ErrAssetUploadFailed indicates that the asset upload failed
type ErrAssetUploadFailed struct {
	Underlying error
}

func (e *ErrAssetUploadFailed) Error() string {
	return "asset upload failed: " + e.Underlying.Error()
}

func (e *ErrAssetUploadFailed) Unwrap() error {
	return e.Underlying
}
