package release

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v70/github"
)

// ChangelogConfig holds configuration for the changelog generator
type ChangelogConfig struct {
	Token   string
	Owner   string
	Repo    string
	PerPage int
}

// ChangelogGenerator generates changelogs from conventional commits using GitHub API
type ChangelogGenerator struct {
	client *github.Client
	config ChangelogConfig
}

// CommitType represents a category of conventional commits
type CommitType string

const (
	CommitTypeFeat     CommitType = "feat"
	CommitTypeFix      CommitType = "fix"
	CommitTypeDocs     CommitType = "docs"
	CommitTypeStyle    CommitType = "style"
	CommitTypeRefactor CommitType = "refactor"
	CommitTypePerf     CommitType = "perf"
	CommitTypeTest     CommitType = "test"
	CommitTypeBuild    CommitType = "build"
	CommitTypeCi       CommitType = "ci"
	CommitTypeChore    CommitType = "chore"
)

// CommitEntry represents a parsed conventional commit
type CommitEntry struct {
	Type        CommitType
	Scope       string
	Description string
	Hash        string
	Author      string
	Date        time.Time
	Body        string
}

// ChangelogSection represents a section in the changelog
type ChangelogSection struct {
	Type        CommitType
	Title       string
	Commits     []CommitEntry
}

// NewChangelogConfig creates a new ChangelogConfig from environment variables
func NewChangelogConfig() ChangelogConfig {
	perPage := 100
	if val := os.Getenv("GITHUB_RELEASE_PER_PAGE"); val != "" {
		if n, err := fmt.Sscanf(val, "%d", &perPage); n == 1 && err == nil {
			if perPage <= 0 {
				perPage = 100
			}
		}
	}

	return ChangelogConfig{
		Token:   os.Getenv("GITHUB_TOKEN"),
		Owner:   os.Getenv("GITHUB_OWNER"),
		Repo:    os.Getenv("GITHUB_REPO"),
		PerPage: perPage,
	}
}

// NewChangelogGenerator creates a new ChangelogGenerator
func NewChangelogGenerator(config ChangelogConfig) (*ChangelogGenerator, error) {
	if config.Token == "" {
		return nil, fmt.Errorf("GITHUB_TOKEN environment variable not set")
	}
	if config.Owner == "" {
		return nil, fmt.Errorf("GITHUB_OWNER environment variable not set")
	}
	if config.Repo == "" {
		return nil, fmt.Errorf("GITHUB_REPO environment variable not set")
	}
	if config.PerPage <= 0 {
		config.PerPage = 100
	}

	return &ChangelogGenerator{
		client: github.NewClient(nil).WithAuthToken(config.Token),
		config: config,
	}, nil
}

// Validate validates the configuration
func (c *ChangelogConfig) Validate() error {
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

// conventionalCommitRegex matches conventional commit messages
var conventionalCommitRegex = regexp.MustCompile(`^([a-z]+)(?:\(([^)]+)\))?: (.+)$`)

// parseCommit parses a single commit message into a CommitEntry
func parseCommit(commit *github.RepositoryCommit) (*CommitEntry, error) {
	if commit.Commit == nil || commit.Commit.Message == nil {
		return nil, fmt.Errorf("commit has no message")
	}

	message := *commit.Commit.Message
	lines := strings.SplitN(message, "\n", 2)
	firstLine := lines[0]

	match := conventionalCommitRegex.FindStringSubmatch(firstLine)
	if match == nil {
		return nil, ErrNoConventionalCommits
	}

	entry := &CommitEntry{
		Type:        CommitType(match[1]),
		Description: match[3],
		Hash:        commit.GetSHA(),
	}

	if commit.Author != nil {
		entry.Author = commit.Author.GetLogin()
	}

	if commit.Commit != nil && commit.Commit.Author != nil && commit.Commit.Author.Date != nil {
		entry.Date = commit.Commit.Author.Date.Time
	}

	if len(lines) > 1 {
		entry.Body = strings.TrimSpace(lines[1])
	}

	// Extract scope if present
	if len(match) > 2 && match[2] != "" {
		entry.Scope = match[2]
	}

	return entry, nil
}

// groupCommits groups commits by their type
func groupCommits(commits []*CommitEntry) []ChangelogSection {
	sections := map[CommitType]ChangelogSection{
		CommitTypeFeat:     {Type: CommitTypeFeat, Title: "Features"},
		CommitTypeFix:      {Type: CommitTypeFix, Title: "Bug Fixes"},
		CommitTypeDocs:     {Type: CommitTypeDocs, Title: "Documentation"},
		CommitTypeStyle:    {Type: CommitTypeStyle, Title: "Styles"},
		CommitTypeRefactor: {Type: CommitTypeRefactor, Title: "Code Refactoring"},
		CommitTypePerf:     {Type: CommitTypePerf, Title: "Performance Improvements"},
		CommitTypeTest:     {Type: CommitTypeTest, Title: "Tests"},
		CommitTypeBuild:    {Type: CommitTypeBuild, Title: "Builds"},
		CommitTypeCi:       {Type: CommitTypeCi, Title: "CI"},
		CommitTypeChore:    {Type: CommitTypeChore, Title: "Chores"},
	}

	for _, commit := range commits {
		if section, ok := sections[commit.Type]; ok {
			section.Commits = append(section.Commits, *commit)
			sections[commit.Type] = section
		}
	}

	// Convert to slice and filter empty sections
	var result []ChangelogSection
	for _, section := range sections {
		if len(section.Commits) > 0 {
			result = append(result, section)
		}
	}

	// Sort by type priority
	sort.Slice(result, func(i, j int) bool {
		priority := func(t CommitType) int {
			switch t {
			case CommitTypeFeat:
				return 0
			case CommitTypeFix:
				return 1
			case CommitTypeRefactor:
				return 2
			case CommitTypePerf:
				return 3
			case CommitTypeDocs:
				return 4
			case CommitTypeTest:
				return 5
			case CommitTypeBuild:
				return 6
			case CommitTypeCi:
				return 7
			case CommitTypeStyle:
				return 8
			case CommitTypeChore:
				return 9
			default:
				return 10
			}
		}
		return priority(result[i].Type) < priority(result[j].Type)
	})

	return result
}

// GenerateChangelog generates a changelog between two tags
func (g *ChangelogGenerator) GenerateChangelog(ctx context.Context, fromTag, toTag string) (string, error) {
	if fromTag == "" && toTag == "" {
		return "", ErrInvalidTagRange
	}

	// Get commits between tags
	var commits []*github.RepositoryCommit

	if fromTag == "" {
		// Get all commits up to toTag
		opts := &github.CommitsListOptions{ListOptions: github.ListOptions{PerPage: g.config.PerPage}}
		for {
			list, resp, err := g.client.Repositories.ListCommits(ctx, g.config.Owner, g.config.Repo, opts)
			if err != nil {
				return "", fmt.Errorf("failed to list commits: %w", err)
			}
			commits = append(commits, list...)
			if resp.NextPage == 0 {
				break
			}
			opts.Page = resp.NextPage
		}
	} else {
		// Get commits from fromTag to toTag
		opts := &github.CommitsListOptions{ListOptions: github.ListOptions{PerPage: g.config.PerPage}}
		for {
			list, resp, err := g.client.Repositories.ListCommits(ctx, g.config.Owner, g.config.Repo, opts)
			if err != nil {
				return "", fmt.Errorf("failed to list commits: %w", err)
			}
			commits = append(commits, list...)
			if resp.NextPage == 0 {
				break
			}
			opts.Page = resp.NextPage
		}
	}

	// Parse commits
	var entries []*CommitEntry
	for _, commit := range commits {
		entry, err := parseCommit(commit)
		if err != nil {
			continue // Skip non-conventional commits
		}
		entries = append(entries, entry)
	}

	if len(entries) == 0 {
		return "", ErrNoConventionalCommits
	}

	// Group commits by type
	sections := groupCommits(entries)

	// Build changelog string
	var sb strings.Builder
	for _, section := range sections {
		sb.WriteString(fmt.Sprintf("## %s\n\n", section.Title))
		for _, commit := range section.Commits {
			scope := ""
			if commit.Scope != "" {
				scope = fmt.Sprintf("**%s**: ", commit.Scope)
			}
			sb.WriteString(fmt.Sprintf("- %s%s (%s)\n", scope, commit.Description, commit.Hash[:7]))
		}
		sb.WriteString("\n")
	}

	return strings.TrimSpace(sb.String()), nil
}

// ListTags lists all tags in the repository
func (g *ChangelogGenerator) ListTags(ctx context.Context) ([]string, error) {
	var tags []string
	opts := &github.ListOptions{PerPage: g.config.PerPage}
	for {
		list, resp, err := g.client.Repositories.ListTags(ctx, g.config.Owner, g.config.Repo, opts)
		if err != nil {
			return nil, fmt.Errorf("failed to list tags: %w", err)
		}
		for _, tag := range list {
			if tag.Name != nil {
				tags = append(tags, *tag.Name)
			}
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return tags, nil
}
