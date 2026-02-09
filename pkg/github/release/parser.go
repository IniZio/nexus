package release

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/google/go-github/v70/github"
)

// ConventionalCommitRegex matches conventional commit messages: <type>(<scope>): <description>
var ConventionalCommitRegex = regexp.MustCompile(`^([a-z]+)(?:\(([^)]+)\))?: (.+)$`)

// PRReferenceRegex matches PR references in commit messages (e.g., #123, fixes #456)
var PRReferenceRegex = regexp.MustCompile(`(?:#)(\d+)`)

// ParsedCommit represents a parsed commit with conventional format and metadata
type ParsedCommit struct {
	Type        string
	Scope       string
	Description string
	FullMessage string
	Hash        string
	Author      string
	AuthorLogin string
	PRNumber    int
	Body        string
}

// ParseCommit parses a GitHub commit and extracts conventional commit information
func ParseCommit(commit *github.RepositoryCommit) *ParsedCommit {
	if commit == nil || commit.Commit == nil || commit.Commit.Message == nil {
		return nil
	}

	message := *commit.Commit.Message
	lines := strings.SplitN(message, "\n", 2)
	firstLine := lines[0]
	description := firstLine

	var commitType, scope string

	// Try to match conventional commit format
	match := ConventionalCommitRegex.FindStringSubmatch(firstLine)
	if match != nil {
		commitType = match[1]
		scope = match[2]
		description = match[3]
	}

	parsed := &ParsedCommit{
		Type:        commitType,
		Scope:       scope,
		Description: description,
		FullMessage: message,
		Hash:        commit.GetSHA(),
	}

	// Extract author name
	if commit.Commit.Author != nil && commit.Commit.Author.Name != nil {
		parsed.Author = *commit.Commit.Author.Name
	}

	// Extract author login
	if commit.Author != nil {
		parsed.AuthorLogin = commit.Author.GetLogin()
	}

	// Extract PR number from commit message
	if len(lines) > 1 {
		parsed.Body = strings.TrimSpace(lines[1])
	}
	parsed.extractPRNumber(message)

	return parsed
}

// extractPRNumber extracts a PR number from the commit message
func (p *ParsedCommit) extractPRNumber(message string) {
	// Look for PR references in the message
	matches := PRReferenceRegex.FindAllStringSubmatch(message, -1)
	if len(matches) > 0 {
		// Take the first PR reference found
		if len(matches[0]) > 1 {
			if prNum, err := strconv.Atoi(matches[0][1]); err == nil {
				p.PRNumber = prNum
			}
		}
	}
}

// ParseCommitMessage parses a raw commit message string (not a GitHub commit object)
// Useful for parsing commits from git log output
func ParseCommitMessage(message string) *ParsedCommit {
	if message == "" {
		return nil
	}

	lines := strings.SplitN(message, "\n", 2)
	firstLine := lines[0]
	description := firstLine

	var commitType, scope string

	// Try to match conventional commit format
	match := ConventionalCommitRegex.FindStringSubmatch(firstLine)
	if match != nil {
		commitType = match[1]
		scope = match[2]
		description = match[3]
	}

	parsed := &ParsedCommit{
		Type:        commitType,
		Scope:       scope,
		Description: description,
		FullMessage: message,
	}

	var body string
	if len(lines) > 1 {
		body = strings.TrimSpace(lines[1])
	}
	parsed.Body = body
	parsed.extractPRNumber(message)

	return parsed
}

// IsConventionalCommit checks if a commit message follows conventional commit format
func IsConventionalCommit(message string) bool {
	return ConventionalCommitRegex.MatchString(message)
}

// ParseMultipleCommits parses multiple GitHub commits
func ParseMultipleCommits(commits []*github.RepositoryCommit) []*ParsedCommit {
	result := make([]*ParsedCommit, 0, len(commits))
	for _, commit := range commits {
		if parsed := ParseCommit(commit); parsed != nil {
			result = append(result, parsed)
		}
	}
	return result
}

// CommitTypeGroup groups commits by their type
func CommitTypeGroup(commits []*ParsedCommit) map[string][]*ParsedCommit {
	groups := make(map[string][]*ParsedCommit)
	for _, commit := range commits {
		if commit.Type != "" {
			groups[commit.Type] = append(groups[commit.Type], commit)
		}
	}
	return groups
}
