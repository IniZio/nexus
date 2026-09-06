package builder

import (
	"fmt"
	"strings"

	"github.com/IniZio/nexus3/internal/core/perimeter/cred"
)

// RenderRecipeLayer converts a [cred.ToolRecipe] into a deterministic sequence
// of Containerfile RUN instructions for the given target architecture (arch).
//
// # Determinism (D-TP-01 Amendment C)
//
// Two calls with identical recipe and arch return byte-identical output. The
// renderer introduces no nonces, timestamps, or any per-call variation.
// SHA256ByArch is accessed via a direct key lookup — Go's randomised map
// range order cannot leak into the output.
//
// # Unknown hash
//
// If a tarball package's SHA256ByArch entry for arch is absent or empty,
// RenderRecipeLayer returns an error naming the package and arch. Silently
// omitting the verification step is never acceptable (AC-2). The missing hash
// must be measured and filled in before building for that arch.
//
// # No agent branching (D-TP-02)
//
// The renderer walks [cred.ToolRecipe.Packages] in order and dispatches on
// [cred.RecipePackageKind] only. It never inspects the agent name or profile.
// The asymmetry between agents — some have multiple packages, others one — is
// expressed purely as the length of the Packages slice.
func RenderRecipeLayer(recipe cred.ToolRecipe, arch string) ([]byte, error) {
	var sb strings.Builder
	for _, pkg := range recipe.Packages {
		switch pkg.Kind {
		case cred.RecipeKindTarball:
			block, err := renderTarball(pkg, arch)
			if err != nil {
				return nil, err
			}
			sb.WriteString(block)
		case cred.RecipeKindNPM:
			sb.WriteString(renderNPM(pkg))
		default:
			return nil, fmt.Errorf("recipelayer: unknown package kind %q for package %q", pkg.Kind, pkg.Name)
		}
	}
	return []byte(sb.String()), nil
}

// renderTarball emits one RUN instruction that:
//  1. Creates the install directory.
//  2. Downloads the tarball via curl.
//  3. Verifies the SHA-256 checksum.
//  4. Extracts the tarball with --strip-components=1 (all supported tarballs
//     have a single top-level wrapper directory).
//  5. Removes the downloaded file.
//  6. Creates any symlinks declared in pkg.Symlinks.
//
// Returns an error if pkg.SHA256ByArch does not carry a non-empty hash for arch.
func renderTarball(pkg cred.RecipePackage, arch string) (string, error) {
	hash, ok := pkg.SHA256ByArch[arch]
	if !ok || hash == "" {
		return "", fmt.Errorf(
			"recipelayer: %s %s: no SHA-256 recorded for arch %q"+
				" — measure the tarball and set SHA256ByArch[%q] before building",
			pkg.Name, pkg.Version, arch, arch,
		)
	}

	url := expandPlaceholders(pkg.URLTemplate, pkg.Version, arch)
	installDir := expandPlaceholders(pkg.InstallDir, pkg.Version, arch)
	tmpFile := "/tmp/" + recipeTmpName(pkg.Name) + ".tar.gz"

	var sb strings.Builder
	sb.WriteString("RUN mkdir -p ")
	sb.WriteString(installDir)
	sb.WriteString(" && \\\n    curl -fsSL \"")
	sb.WriteString(url)
	sb.WriteString("\" -o ")
	sb.WriteString(tmpFile)
	sb.WriteString(" && \\\n    echo \"")
	sb.WriteString(hash)
	sb.WriteString("  ")
	sb.WriteString(tmpFile)
	sb.WriteString("\" | sha256sum -c - && \\\n    tar -C ")
	sb.WriteString(installDir)
	sb.WriteString(" -xzf ")
	sb.WriteString(tmpFile)
	sb.WriteString(" --strip-components=1 && \\\n    rm ")
	sb.WriteString(tmpFile)

	for _, sl := range pkg.Symlinks {
		link := expandPlaceholders(sl.LinkPath, pkg.Version, arch)
		target := expandPlaceholders(sl.TargetPath, pkg.Version, arch)
		sb.WriteString(" && \\\n    ln -sf ")
		sb.WriteString(target)
		sb.WriteString(" ")
		sb.WriteString(link)
	}
	sb.WriteString("\n")
	return sb.String(), nil
}

// renderNPM emits one RUN instruction that installs an npm package globally at
// the pinned version. npm manages its own bin symlinks; no explicit link step
// is needed.
func renderNPM(pkg cred.RecipePackage) string {
	return "RUN npm install -g " + pkg.Name + "@" + pkg.Version + "\n"
}

// expandPlaceholders substitutes {VERSION} and {ARCH} in s with the given
// values. {OS} is intentionally not substituted: every current profile
// hardcodes "linux" in the URL template, so {OS} has zero call sites. A
// future recipe that introduces {OS} will produce a literal "{OS}" in the
// output, which causes a 404 at build time — a visible, diagnosable failure
// rather than a silent wrong substitution.
func expandPlaceholders(s, version, arch string) string {
	s = strings.ReplaceAll(s, "{VERSION}", version)
	s = strings.ReplaceAll(s, "{ARCH}", arch)
	return s
}

// recipeTmpName converts a package name into a safe /tmp filename stem by
// removing characters that are syntactically meaningful in shell contexts.
// Only tarball packages use this; npm packages manage their own download path.
func recipeTmpName(name string) string {
	return strings.NewReplacer("@", "", "/", "-", " ", "-").Replace(name)
}
