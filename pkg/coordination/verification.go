package coordination

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
)

type WorkspaceConfig struct {
	Type         string
	RootPath     string
	TestCmd      string
	LintCmd      string
	TypeCheckCmd string
	DocsPaths    []string
}

var (
	ErrTestsFailed      = errors.New("tests did not pass")
	ErrLintFailed       = errors.New("linting did not pass")
	ErrTypeCheckFailed  = errors.New("type check did not pass")
	ErrReviewIncomplete = errors.New("code review not completed")
	ErrDocsIncomplete   = errors.New("documentation not complete")
	ErrCriteriaNotMet   = errors.New("verification criteria not met")
)

func DetectWorkspaceConfig(workspacePath string) (*WorkspaceConfig, error) {
	config := &WorkspaceConfig{
		RootPath: workspacePath,
	}

	packageJSONPath := filepath.Join(workspacePath, "package.json")
	if _, err := os.Stat(packageJSONPath); err == nil {
		config.Type = "node"
		config.TestCmd = "npm test"
		config.LintCmd = "npm run lint"
		config.TypeCheckCmd = "npm run typecheck"
		config.DocsPaths = []string{"docs", "README.md"}
		return config, nil
	}

	goModPath := filepath.Join(workspacePath, "go.mod")
	if _, err := os.Stat(goModPath); err == nil {
		config.Type = "go"
		config.TestCmd = "go test ./..."
		config.LintCmd = "golangci-lint run"
		config.TypeCheckCmd = "go build ./..."
		config.DocsPaths = []string{"docs", "README.md"}
		return config, nil
	}

	cargoTomlPath := filepath.Join(workspacePath, "Cargo.toml")
	if _, err := os.Stat(cargoTomlPath); err == nil {
		config.Type = "rust"
		config.TestCmd = "cargo test"
		config.LintCmd = "cargo clippy"
		config.TypeCheckCmd = "cargo check"
		config.DocsPaths = []string{"docs", "README.md"}
		return config, nil
	}

	pomXmlPath := filepath.Join(workspacePath, "pom.xml")
	if _, err := os.Stat(pomXmlPath); err == nil {
		config.Type = "java"
		config.TestCmd = "mvn test"
		config.LintCmd = "mvn checkstyle:check"
		config.TypeCheckCmd = "mvn compile"
		config.DocsPaths = []string{"docs", "README.md"}
		return config, nil
	}

	config.Type = "generic"
	config.TestCmd = "make test"
	config.LintCmd = "make lint"
	config.TypeCheckCmd = "make typecheck"
	config.DocsPaths = []string{"docs", "README.md"}

	return config, nil
}

func RunCommand(ctx context.Context, dir, name string, args ...string) (bool, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = nil
	cmd.Stderr = nil

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return false, nil
		}
		return false, err
	}

	return true, nil
}
