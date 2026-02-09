package ctrl

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/nexus/nexus/pkg/paths"
)

// DependencyLock represents a locked dependency entry
type DependencyLock struct {
	Version  string `json:"version"`
	Source   string `json:"source"`
	Resolved string `json:"resolved,omitempty"`
	Checksum string `json:"checksum,omitempty"`
}

// Lockfile represents the workspace dependency lockfile
type Lockfile struct {
	Version      string                    `json:"version"`
	GeneratedAt  string                    `json:"generated_at"`
	Dependencies map[string]DependencyLock `json:"dependencies"`
}

// GenerateWorkspaceLockfile generates a lockfile based on current workspace dependencies
func (c *BaseController) GenerateWorkspaceLockfile() (*Lockfile, error) {
	lockfile := &Lockfile{
		Version:      "1.0",
		GeneratedAt:  time.Now().UTC().Format(time.RFC3339),
		Dependencies: make(map[string]DependencyLock),
	}

	// Detect and add npm dependencies
	if err := c.addNpmDependencies(lockfile); err != nil {
		fmt.Printf("Warning: failed to detect npm dependencies: %v\n", err)
	}

	// Detect and add Go module dependencies
	if err := c.addGoModDependencies(lockfile); err != nil {
		fmt.Printf("Warning: failed to detect Go module dependencies: %v\n", err)
	}

	// Detect and add Python dependencies
	if err := c.addPythonDependencies(lockfile); err != nil {
		fmt.Printf("Warning: failed to detect Python dependencies: %v\n", err)
	}

	// Detect and add Cargo dependencies
	if err := c.addCargoDependencies(lockfile); err != nil {
		fmt.Printf("Warning: failed to detect Cargo dependencies: %v\n", err)
	}

	return lockfile, nil
}

// addNpmDependencies detects npm/pnpm/yarn dependencies from package.json
func (c *BaseController) addNpmDependencies(lockfile *Lockfile) error {
	projectRoot := paths.GetProjectRoot()
	packageFiles := []string{
		filepath.Join(projectRoot, "package.json"),
		filepath.Join(projectRoot, "pnpm-workspace.yaml"),
	}

	for _, pkgFile := range packageFiles {
		data, err := os.ReadFile(pkgFile)
		if err != nil {
			continue
		}

		var pkg struct {
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}

		if err := json.Unmarshal(data, &pkg); err != nil {
			continue
		}

		// Process dependencies
		for name, version := range pkg.Dependencies {
			resolved := fmt.Sprintf("https://registry.npmjs.org/%s/-/%s", name, version)
			checksum := c.computeChecksumForPackage(name, version, "npm")
			lockfile.Dependencies[name] = DependencyLock{
				Version:  version,
				Source:   "npm",
				Resolved: resolved,
				Checksum: checksum,
			}
		}

		// Process dev dependencies
		for name, version := range pkg.DevDependencies {
			if _, exists := lockfile.Dependencies[name]; exists {
				continue
			}
			resolved := fmt.Sprintf("https://registry.npmjs.org/%s/-/%s", name, version)
			checksum := c.computeChecksumForPackage(name, version, "npm")
			lockfile.Dependencies[name] = DependencyLock{
				Version:  version,
				Source:   "npm",
				Resolved: resolved,
				Checksum: checksum,
			}
		}
	}

	return nil
}

// addGoModDependencies detects Go module dependencies from go.mod
func (c *BaseController) addGoModDependencies(lockfile *Lockfile) error {
	projectRoot := paths.GetProjectRoot()
	goModPath := filepath.Join(projectRoot, "go.mod")

	data, err := os.ReadFile(goModPath)
	if err != nil {
		return err
	}

	content := string(data)
	// Parse go.mod for require blocks
	lines := splitLines(content)
	inRequire := false

	for _, line := range lines {
		line = trimWhitespace(line)

		if line == "require (" {
			inRequire = true
			continue
		}
		if line == ")" {
			inRequire = false
			continue
		}

		if inRequire || strings.HasPrefix(line, "require ") {
			// Extract module name and version
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				moduleName := parts[0]
				version := parts[1]

				// Skip indirect dependencies marker
				version = strings.TrimPrefix(version, "//")

				// Clean up version (remove indirect marker, extra comments)
				if idx := strings.Index(version, " "); idx != -1 {
					version = version[:idx]
				}

				// Resolve checksum using go mod download
				resolved := c.resolveGoModule(moduleName, version)
				checksum := c.computeGoModChecksum(moduleName, version)

				lockfile.Dependencies[moduleName] = DependencyLock{
					Version:  version,
					Source:   "go",
					Resolved: resolved,
					Checksum: checksum,
				}
			}
		}
	}

	return nil
}

// addPythonDependencies detects Python dependencies from requirements.txt or pyproject.toml
func (c *BaseController) addPythonDependencies(lockfile *Lockfile) error {
	projectRoot := paths.GetProjectRoot()

	// Check for requirements.txt
	reqFile := filepath.Join(projectRoot, "requirements.txt")
	if data, err := os.ReadFile(reqFile); err == nil {
		lines := splitLines(string(data))
		for _, line := range lines {
			line = trimWhitespace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}

			// Parse package name and version
			parts := strings.SplitN(line, "==", 2)
			if len(parts) < 2 {
				parts = strings.SplitN(line, ">=", 2)
			}
			if len(parts) < 2 {
				parts = strings.SplitN(line, ">", 2)
			}
			if len(parts) < 2 {
				parts = strings.SplitN(line, "<=", 2)
			}
			if len(parts) < 2 {
				parts = strings.SplitN(line, "~=", 2)
			}

			if len(parts) >= 2 {
				name := parts[0]
				version := parts[1]
				resolved := fmt.Sprintf("https://pypi.org/simple/%s/", name)
				checksum := c.computePythonPackageChecksum(name, version)

				lockfile.Dependencies[name] = DependencyLock{
					Version:  version,
					Source:   "pip",
					Resolved: resolved,
					Checksum: checksum,
				}
			}
		}
	}

	// Check for pyproject.toml or setup.py
	pyprojectFile := filepath.Join(projectRoot, "pyproject.toml")
	if _, err := os.Stat(pyprojectFile); err == nil {
		// Could parse poetry/PDMP dependencies here in the future
	}

	return nil
}

// addCargoDependencies detects Rust dependencies from Cargo.toml
func (c *BaseController) addCargoDependencies(lockfile *Lockfile) error {
	projectRoot := paths.GetProjectRoot()
	cargoToml := filepath.Join(projectRoot, "Cargo.toml")

	data, err := os.ReadFile(cargoToml)
	if err != nil {
		return err
	}

	content := string(data)
	// Parse dependencies section
	lines := splitLines(content)
	inDeps := false
	sections := []string{"dependencies", "dev-dependencies", "build-dependencies"}

	for _, line := range lines {
		line = trimWhitespace(line)

		for _, section := range sections {
			if line == "["+section+"]" || line == "[dependencies]" {
				inDeps = true
				continue
			}
		}

		if inDeps && strings.HasPrefix(line, "[") {
			inDeps = false
			continue
		}

		if inDeps && line != "" && !strings.HasPrefix(line, "#") {
			// Parse dependency line
			parts := strings.Fields(line)
			if len(parts) >= 1 {
				name := parts[0]
				version := ""
				if len(parts) >= 3 && parts[1] == "=" {
					version = parts[2]
				}

				if version != "" {
					resolved := fmt.Sprintf("https://crates.io/api/v1/crates/%s/%s/download", name, version)
					checksum := c.computeCargoPackageChecksum(name, version)

					lockfile.Dependencies[name] = DependencyLock{
						Version:  version,
						Source:   "cargo",
						Resolved: resolved,
						Checksum: checksum,
					}
				}
			}
		}
	}

	return nil
}

// SaveLockfile saves the lockfile to .nexus/nexus.lock
func (c *BaseController) SaveLockfile(lockfile *Lockfile) error {
	projectRoot := paths.GetProjectRoot()
	lockPath := filepath.Join(paths.GetConfigDir(projectRoot), "nexus.lock")

	data, err := json.MarshalIndent(lockfile, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal lockfile: %w", err)
	}

	if err := os.WriteFile(lockPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write lockfile: %w", err)
	}

	return nil
}

// resolveGoModule gets the resolved URL for a Go module
func (c *BaseController) resolveGoModule(moduleName, version string) string {
	// Use go list to get the module info
	cmd := exec.Command("go", "list", "-m", "-json", moduleName)
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("https://proxy.golang.org/%s/@v/%s.lock", moduleName, version)
	}

	// Parse the output to get the resolved path
	var modInfo struct {
		Path    string `json:"Path"`
		Version string `json:"Version"`
		Dir     string `json:"Dir,omitempty"`
	}

	if err := json.Unmarshal(output, &modInfo); err != nil {
		return fmt.Sprintf("https://proxy.golang.org/%s/@v/%s.lock", moduleName, version)
	}

	return fmt.Sprintf("https://proxy.golang.org/%s/@v/%s.lock", modInfo.Path, modInfo.Version)
}

// computeChecksumForPackage computes a checksum for an npm package
func (c *BaseController) computeChecksumForPackage(name, version, source string) string {
	data := fmt.Sprintf("%s:%s:%s", name, version, source)
	hash := sha256.Sum256([]byte(data))
	return "sha256:" + hex.EncodeToString(hash[:])
}

// computeGoModChecksum computes a checksum for a Go module
func (c *BaseController) computeGoModChecksum(moduleName, version string) string {
	data := fmt.Sprintf("go:%s:%s", moduleName, version)
	hash := sha256.Sum256([]byte(data))
	return "sha256:" + hex.EncodeToString(hash[:])
}

// computePythonPackageChecksum computes a checksum for a Python package
func (c *BaseController) computePythonPackageChecksum(name, version string) string {
	data := fmt.Sprintf("pip:%s:%s", name, version)
	hash := sha256.Sum256([]byte(data))
	return "sha256:" + hex.EncodeToString(hash[:])
}

// computeCargoPackageChecksum computes a checksum for a Cargo package
func (c *BaseController) computeCargoPackageChecksum(name, version string) string {
	data := fmt.Sprintf("cargo:%s:%s", name, version)
	hash := sha256.Sum256([]byte(data))
	return "sha256:" + hex.EncodeToString(hash[:])
}

// splitLines splits a string into lines
func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// trimWhitespace removes leading and trailing whitespace
func trimWhitespace(s string) string {
	start := 0
	end := len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t' || s[end-1] == '\r') {
		end--
	}
	return s[start:end]
}
