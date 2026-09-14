package cred

import "strconv"

type ToolRecipe struct {
	Packages []RecipePackage
	BinPath  string
}

type RecipePackageKind string

const (
	RecipeKindTarball RecipePackageKind = "tarball"
	RecipeKindNPM     RecipePackageKind = "npm"
)

// FloatingVersion ("latest") is resolved to a concrete version on the host before the recipe reaches the cache key or renderer.
const FloatingVersion = "latest"

type RecipePackage struct {
	Kind         RecipePackageKind
	Name         string
	Version      string
	URLTemplate  string
	// SHA256ByArch: empty string means hash not yet verified; renderer must refuse to build if target-arch entry is absent or empty.
	SHA256ByArch map[string]string
	InstallDir   string
	Symlinks     []RecipeSymlink
}

func (p RecipePackage) IsFloating() bool {
	return p.Version == FloatingVersion
}

type RecipeSymlink struct {
	LinkPath string
	// TargetPath must stay inside the versioned install dir; launcher resolves sibling "node" via realpath($0) (cursor-agent only).
	TargetPath string
}

// Validate checks the recipe. It does NOT require SHA256ByArch to be populated for every arch; the renderer enforces hashes at build time.
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
		if p.Version == FloatingVersion && p.Kind == RecipeKindTarball {
			return &RecipeValidationError{
				PackageIndex: i,
				PackageName:  p.Name,
				Field:        "Version",
				Reason:       "tarball packages must pin an exact version; FloatingVersion cannot be used because the SHA-256 checksum cannot verify an unknown version",
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
		strconv.Itoa(e.PackageIndex) + "] (" + name + ")." + e.Field + ": " + e.Reason
}
