package builder

import (
	"fmt"
	"strings"

	"github.com/IniZio/nexus/internal/core/perimeter/cred"
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
		case cred.RecipeKindOCI:
			block, err := renderOCI(pkg)
			if err != nil {
				return nil, err
			}
			sb.WriteString(block)
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
	sb.WriteString("RUN ")
	if pkg.VersionCmd != "" {
		if !isDottedNumeric(pkg.Version) {
			return "", fmt.Errorf(
				"recipelayer: %s: VersionCmd requires a dotted-numeric Version, got %q",
				pkg.Name, pkg.Version,
			)
		}
		sb.WriteString(versionGuardPrefix(pkg.Name, pkg.Version, pkg.VersionCmd))
	}
	sb.WriteString("mkdir -p ")
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
	if pkg.VersionCmd != "" {
		sb.WriteString("; \\\n    fi")
	}
	sb.WriteString("\n")
	return sb.String(), nil
}

// versionGuardPrefix opens a POSIX sh `if` (no bash-isms; caller closes with `fi`) that skips the install chain when versionCmd reports a version >= pinned.
func versionGuardPrefix(name, pinned, versionCmd string) string {
	return `recipe_ver_ge() { a="$1"; b="$2"; while [ -n "$a" ] || [ -n "$b" ]; do ah="${a%%.*}"; bh="${b%%.*}"; if [ "$a" = "$ah" ]; then a=""; else a="${a#*.}"; fi; if [ "$b" = "$bh" ]; then b=""; else b="${b#*.}"; fi; if [ "${ah:-0}" -gt "${bh:-0}" ]; then return 0; fi; if [ "${ah:-0}" -lt "${bh:-0}" ]; then return 1; fi; done; return 0; }; \
    existing="$(` + versionCmd + ` 2>/dev/null || true)"; existing="${existing#v}"; existing="${existing%%[!0-9.]*}"; \
    if [ -n "$existing" ] && recipe_ver_ge "$existing" "` + pinned + `"; then \
    echo "recipe ` + name + `: existing $existing >= ` + pinned + `, skipped"; \
    else \
    `
}

func isDottedNumeric(v string) bool {
	if v == "" {
		return false
	}
	for _, part := range strings.Split(v, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func renderOCI(pkg cred.RecipePackage) (string, error) {
	if pkg.Version == cred.FloatingVersion || !strings.HasPrefix(pkg.Version, "sha256:") {
		return "", fmt.Errorf(
			"recipelayer: %s: OCI digest unresolved (Version %q) — ResolveFloatingVersions must run before render",
			pkg.Name, pkg.Version,
		)
	}
	if len(pkg.Symlinks) == 0 {
		return "", fmt.Errorf("recipelayer: %s: OCI package has no symlinks — at least one PATH entry required", pkg.Name)
	}

	repo := ociRepo(pkg.Image)
	var sb strings.Builder

	sb.WriteString("COPY --chown=0:0 --from=")
	sb.WriteString(repo)
	sb.WriteString("@")
	sb.WriteString(pkg.Version)
	sb.WriteString(" ")
	sb.WriteString(pkg.SrcPath)
	sb.WriteString(" ")
	sb.WriteString(pkg.InstallDir)
	sb.WriteString("/\n")

	sb.WriteString("RUN set -e; d=\"$(ls -1d ")
	sb.WriteString(pkg.InstallDir)
	sb.WriteString("/* | head -n 1)\"; [ -n \"$d\" ] || { echo \"recipe ")
	sb.WriteString(pkg.Name)
	sb.WriteString(": nothing copied into ")
	sb.WriteString(pkg.InstallDir)
	sb.WriteString("\" >&2; exit 1; }; \\\n")

	var linkTarget string
	if pkg.BinRel == "" {
		linkTarget = "\"$d\""
	} else {
		linkTarget = "\"$d/" + pkg.BinRel + "\""
	}
	for i, sl := range pkg.Symlinks {
		if i == 0 {
			sb.WriteString("    ln -sf ")
		} else {
			sb.WriteString(" && ln -sf ")
		}
		sb.WriteString(linkTarget)
		sb.WriteString(" ")
		sb.WriteString(sl.LinkPath)
	}
	sb.WriteString("\n")
	return sb.String(), nil
}

func ociRepo(image string) string {
	if i := strings.Index(image, "@"); i >= 0 {
		image = image[:i]
	}
	lastSlash := strings.LastIndex(image, "/")
	if lastSlash < 0 {
		if i := strings.Index(image, ":"); i >= 0 {
			return image[:i]
		}
		return image
	}
	prefix := image[:lastSlash]
	lastSeg := image[lastSlash+1:]
	if i := strings.Index(lastSeg, ":"); i >= 0 {
		lastSeg = lastSeg[:i]
	}
	return prefix + "/" + lastSeg
}

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
