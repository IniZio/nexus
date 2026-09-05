package cred

// ToolRecipe describes how to install one agent's tooling into a guest image.
// It is pure data; the renderer (internal/core/builder/recipelayer.go) consumes
// it as a pure function of these fields with no branch on the agent's name
// (D-TP-01, D-TP-02).
//
// The recipe must be deterministic: it must carry no nonces, timestamps, or
// any other value that varies per build (D-TP-01 Amendment C). A warm buildkit
// cache must produce a byte-identical layer from the same recipe across runs.
//
// Two profiles fit without special-casing:
//   - claude-code: two packages — a Node.js tarball (runtime) plus an npm
//     package (the CLI). The renderer processes them in order.
//   - cursor-agent: one package — a self-contained tarball that bundles its
//     own Node.js runtime; no separate runtime entry is needed.
//
// The renderer iterates [ToolRecipe.Packages] and emits one layer per package
// without inspecting the agent name. The asymmetry (two packages vs one) is
// expressed purely as the length of the Packages slice.
type ToolRecipe struct {
	// Packages is the ordered list of install steps. The renderer processes them
	// in sequence; a tarball install may be a prerequisite for a later npm install
	// (as it is for claude-code, where Node.js must come first).
	//
	// Must be non-empty for the recipe to be meaningful. Validate returns an
	// error if any package carries an empty Version.
	Packages []RecipePackage

	// BinPath is the absolute guest path where the agent binary is reachable
	// after all packages are installed and symlinks are created. This is the
	// path the shadow-mount diagnostic (AC-5) checks against operator mounts.
	//
	// Example: "/usr/local/bin/claude" for claude-code,
	//          "/usr/local/bin/cursor-agent" for cursor-agent.
	BinPath string
}

// RecipePackageKind is the install mechanism for a [RecipePackage].
type RecipePackageKind string

const (
	// RecipeKindTarball: download the tarball at URLTemplate (with {VERSION},
	// {OS}, {ARCH} substituted), verify against SHA256ByArch for the target
	// arch, and extract to InstallDir. Post-install symlinks from Symlinks
	// are then created.
	RecipeKindTarball RecipePackageKind = "tarball"

	// RecipeKindNPM: run "npm install -g <Name>@<Version>" after any preceding
	// tarball packages (e.g. a Node.js runtime tarball) have been installed.
	// URLTemplate, SHA256ByArch, InstallDir, and Symlinks are unused for this kind.
	RecipeKindNPM RecipePackageKind = "npm"
)

// RecipePackage is one installable unit within a [ToolRecipe].
// The renderer handles each Kind without branching on the agent name.
type RecipePackage struct {
	// Kind selects the install mechanism (tarball or npm).
	Kind RecipePackageKind

	// Name is the package identifier.
	//   - npm: the full npm package name, e.g. "@anthropic-ai/claude-code".
	//   - tarball: a human-readable label, e.g. "node" or "cursor-agent".
	Name string

	// Version is the pinned version string. Must be non-empty; Validate rejects
	// a recipe where any package has an empty Version. The exact format matches
	// the upstream release tag:
	//   - Node.js: "22.23.2" (semver, no "v" prefix, matching the nodejs.org URL)
	//   - @anthropic-ai/claude-code: "2.1.226" (npm dist-tag, no "v" prefix)
	//   - cursor-agent: "2026.08.25-3e8eec8" (date+commit, vendor format)
	Version string

	// URLTemplate is the download URL for tarball packages, with the following
	// placeholders that the renderer substitutes at render time:
	//   {VERSION} → Version
	//   {OS}      → the target OS string in the vendor's naming (e.g. "linux")
	//   {ARCH}    → the target arch string in the vendor's naming (see SHA256ByArch)
	//
	// Empty for RecipeKindNPM packages (npm installs via its own registry).
	URLTemplate string

	// SHA256ByArch maps the vendor's arch label (the string substituted for
	// {ARCH} in URLTemplate) to the expected lowercase hex SHA-256 of the
	// downloaded tarball for that architecture. The renderer verifies the
	// downloaded artifact before extraction.
	//
	// Keys use the vendor's own naming to match URLTemplate literally:
	//   - nodejs.org: "x64", "arm64"
	//   - downloads.cursor.com: "x64", "arm64"
	//
	// An empty string value is a sentinel meaning "hash not yet verified for
	// this arch". Validate does NOT reject empty values here — the arm64 hash
	// may be genuinely unknown (e.g. cursor-agent arm64 was never pulled and
	// hashed). The renderer must refuse to build if the target-arch entry is
	// absent or empty; that is a build-time constraint, not a data constraint.
	//
	// Nil or empty map means no hashes are recorded (valid for RecipeKindNPM).
	SHA256ByArch map[string]string

	// InstallDir is the absolute guest path where the tarball is extracted.
	// May contain a {VERSION} placeholder that the renderer expands.
	//
	//   - Node.js: "/usr/local" (tarball already has the bin/lib layout inside)
	//   - cursor-agent: "/usr/local/share/cursor-agent/versions/{VERSION}"
	//
	// Empty for RecipeKindNPM; npm uses its own global prefix (/usr/local).
	InstallDir string

	// Symlinks is the ordered list of filesystem symlinks to create after the
	// package is installed. The renderer creates them in slice order.
	// Symlinks is unused for RecipeKindNPM (npm manages its own bin links).
	Symlinks []RecipeSymlink
}

// RecipeSymlink describes a single symlink to create after a package install.
// Both paths may contain a {VERSION} placeholder that the renderer expands
// with [RecipePackage.Version] before creating the link.
type RecipeSymlink struct {
	// LinkPath is the absolute guest path for the new symlink.
	// Example: "/usr/local/bin/cursor-agent"
	LinkPath string

	// TargetPath is the absolute guest path the symlink points to.
	// Example: "/usr/local/share/cursor-agent/versions/{VERSION}/agent-cli"
	//
	// The vendor's launcher resolves its sibling "node" via realpath($0), so
	// the target must stay inside the versioned install dir for the bundled
	// runtime to be found correctly (cursor-agent only; claude-code has no
	// such constraint because its runtime is a separate tarball package).
	TargetPath string
}

// Validate checks the recipe for the constraints that must hold before any
// build is attempted. It returns a non-nil error if:
//   - any package carries an empty Version (AC-3)
//   - any package carries an empty Name
//   - any package carries an empty Kind
//
// It does NOT require SHA256ByArch to be populated for every arch: a hash
// may be genuinely unknown for an architecture that was never pulled and
// measured (e.g. cursor-agent arm64). The renderer enforces the per-arch
// hash at build time.
func (r ToolRecipe) Validate() error {
	for i, p := range r.Packages {
		if p.Version == "" {
			return &RecipeValidationError{
				PackageIndex: i,
				PackageName:  p.Name,
				Field:        "Version",
				Reason:       "must not be empty",
			}
		}
		if p.Name == "" {
			return &RecipeValidationError{
				PackageIndex: i,
				PackageName:  p.Name,
				Field:        "Name",
				Reason:       "must not be empty",
			}
		}
		if p.Kind == "" {
			return &RecipeValidationError{
				PackageIndex: i,
				PackageName:  p.Name,
				Field:        "Kind",
				Reason:       "must not be empty",
			}
		}
	}
	return nil
}

// RecipeValidationError is returned by [ToolRecipe.Validate] when a recipe
// field fails its constraint.
type RecipeValidationError struct {
	PackageIndex int
	PackageName  string
	Field        string
	Reason       string
}

func (e *RecipeValidationError) Error() string {
	name := e.PackageName
	if name == "" {
		name = "<unnamed>"
	}
	return "cred: ToolRecipe.Validate: package[" +
		itoa(e.PackageIndex) + "] (" + name + ")." + e.Field + ": " + e.Reason
}

// itoa converts a non-negative integer to its decimal string representation
// without importing strconv, keeping the cred package's import list unchanged.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
