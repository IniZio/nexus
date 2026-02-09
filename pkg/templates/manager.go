package templates

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type TemplateRepo struct {
	URL    string
	Branch string
	Path   string // Path within repo to templates
}

type Manager struct {
	baseDir string
}

func NewManager(baseDir string) *Manager {
	return &Manager{baseDir: baseDir}
}

// PullRepo clones or updates a remote template repository
func (m *Manager) PullRepo(repo TemplateRepo) error {
	repoName := extractRepoName(repo.URL)
	repoDir := filepath.Join(m.baseDir, "remotes", repoName)

	// Check if repo already exists
	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		// Clone new repo
		return m.cloneRepo(repo, repoDir)
	}
	// Update existing repo
	return m.updateRepo(repo, repoDir)
}

func (m *Manager) cloneRepo(repo TemplateRepo, repoDir string) error {
	options := &git.CloneOptions{
		URL: repo.URL,
	}

	if repo.Branch != "" {
		options.ReferenceName = plumbing.NewBranchReferenceName(repo.Branch)
		options.SingleBranch = true
	}

	_, err := git.PlainClone(repoDir, false, options)
	if err != nil {
		// Fall back to main if specified branch doesn't exist
		if repo.Branch != "" && repo.Branch != "main" {
			options.ReferenceName = plumbing.NewBranchReferenceName("main")
			_, err = git.PlainClone(repoDir, false, options)
			if err != nil {
				// Fall back to master if main doesn't exist
				options.ReferenceName = plumbing.NewBranchReferenceName("master")
				_, err = git.PlainClone(repoDir, false, options)
			}
		}
		if err != nil {
			return fmt.Errorf("failed to clone template repo %s: %w", repo.URL, err)
		}
	}

	fmt.Printf("Cloned template repo %s to %s\n", repo.URL, repoDir)
	return nil
}

func (m *Manager) updateRepo(repo TemplateRepo, repoDir string) error {
	r, err := git.PlainOpen(repoDir)
	if err != nil {
		return fmt.Errorf("failed to open repo %s: %w", repoDir, err)
	}

	w, err := r.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree for %s: %w", repoDir, err)
	}

	options := &git.PullOptions{
		RemoteName: "origin",
	}

	branchToTry := repo.Branch
	if branchToTry == "" {
		branchToTry = "main"
	}

	tryBranches := []string{branchToTry}
	if branchToTry != "main" {
		tryBranches = append(tryBranches, "main")
	}
	if branchToTry != "master" {
		tryBranches = append(tryBranches, "master")
	}

	var pullErr error
	for _, branch := range tryBranches {
		options.ReferenceName = plumbing.NewBranchReferenceName(branch)
		pullErr = w.Pull(options)
		if pullErr == nil || pullErr == git.NoErrAlreadyUpToDate {
			break
		}
	}

	if pullErr != nil && pullErr != git.NoErrAlreadyUpToDate {
		return fmt.Errorf("failed to pull template repo %s: %w", repo.URL, pullErr)
	}

	if pullErr == git.NoErrAlreadyUpToDate {
		fmt.Printf("Template repo %s is already up to date\n", repo.URL)
	} else {
		fmt.Printf("Updated template repo %s\n", repo.URL)
	}

	return nil
}

// ListRepos returns all pulled remote repositories
func (m *Manager) ListRepos() ([]string, error) {
	reposDir := filepath.Join(m.baseDir, "remotes")
	entries, err := os.ReadDir(reposDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var repos []string
	for _, entry := range entries {
		if entry.IsDir() {
			repos = append(repos, entry.Name())
		}
	}

	return repos, nil
}

// extractRepoName extracts repository name from URL
func extractRepoName(url string) string {
	parts := strings.Split(strings.TrimSuffix(url, ".git"), "/")
	return parts[len(parts)-1]
}

// GetRepoDir returns the directory path for a given repository name
func (m *Manager) GetRepoDir(repoName string) string {
	return filepath.Join(m.baseDir, "remotes", repoName)
}

// PullWithoutUpdate clones a repository if it doesn't exist, but does NOT fetch updates.
// Used during normal operations (like Merge) to use cached repos without network calls.
func (m *Manager) PullWithoutUpdate(repo TemplateRepo) error {
	repoName := extractRepoName(repo.URL)
	repoDir := m.GetRepoDir(repoName)

	if _, err := os.Stat(repoDir); !os.IsNotExist(err) {
		return nil
	}

	return m.cloneRepo(repo, repoDir)
}

// PullWithUpdate explicitly updates a repository from remote.
// Used by 'nexus update' to fetch latest changes.
func (m *Manager) PullWithUpdate(repo TemplateRepo) error {
	repoName := extractRepoName(repo.URL)
	repoDir := m.GetRepoDir(repoName)

	if _, err := os.Stat(repoDir); os.IsNotExist(err) {
		return m.cloneRepo(repo, repoDir)
	}

	return m.updateRepo(repo, repoDir)
}

// GetRepoSHA returns the current commit SHA for a repository
func (m *Manager) GetRepoSHA(repo TemplateRepo) (string, error) {
	repoName := extractRepoName(repo.URL)
	repoDir := m.GetRepoDir(repoName)

	r, err := git.PlainOpen(repoDir)
	if err != nil {
		return "", fmt.Errorf("failed to open repo %s: %w", repoDir, err)
	}

	ref, err := r.Head()
	if err != nil {
		return "", fmt.Errorf("failed to get head for %s: %w", repoDir, err)
	}

	return ref.Hash().String(), nil
}
