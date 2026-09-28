// Package pin defines a single artifact descriptor shared by the hostbin
// embedding layer and the toolcache downloader: name, version, per-arch URL,
// checksums, and archive membership.
package pin

import (
	"errors"
	"fmt"
	"strings"
)

// Format of the artifact served at a pin's URL.
type Format int

const (
	FormatRaw   Format = iota // URL serves the executable itself
	FormatTarGz               // .tar.gz / .tgz
	FormatZip                 // .zip
)

// Pin describes a single versioned host executable that nexus downloads or
// embeds. All SHA256 values are lowercase hex.
type Pin struct {
	Name        string // executable name, e.g. "cloud-hypervisor"
	Version     string
	URLTemplate string            // placeholders {VERSION} {GOARCH} {ARCH}
	URLByGoArch map[string]string // per-arch override of URLTemplate (same placeholders)

	SHA256ByGoArch map[string]string

	// ArchiveMember is the path of the target executable inside the archive.
	// Empty means FormatRaw. Placeholders are expanded the same way as URL.
	ArchiveMember string

	// BinarySHA256ByGoArch is the sha256 of the extracted executable.
	// Only consulted when ArchiveMember != "".
	BinarySHA256ByGoArch map[string]string

	Note string
}

// ErrUnsupportedArch is returned when a Pin has no pinned artifact for the
// requested GOARCH.
var ErrUnsupportedArch = errors.New("pin: no pinned artifact for arch")

// Expand replaces the three standard placeholders in s:
//
//	{VERSION} → version
//	{GOARCH}  → goarch (e.g. "amd64")
//	{ARCH}    → x86_64 for amd64, aarch64 for arm64, goarch otherwise
func Expand(s, version, goarch string) string {
	arch := goarch
	switch goarch {
	case "amd64":
		arch = "x86_64"
	case "arm64":
		arch = "aarch64"
	}
	s = strings.ReplaceAll(s, "{VERSION}", version)
	s = strings.ReplaceAll(s, "{GOARCH}", goarch)
	s = strings.ReplaceAll(s, "{ARCH}", arch)
	return s
}

// URL returns the download URL for goarch.
// URLByGoArch[goarch] wins over URLTemplate; placeholders are expanded.
// Returns "" when neither is set.
func (p Pin) URL(goarch string) string {
	tmpl := p.URLByGoArch[goarch]
	if tmpl == "" {
		tmpl = p.URLTemplate
	}
	if tmpl == "" {
		return ""
	}
	return Expand(tmpl, p.Version, goarch)
}

// Member returns the archive member path for goarch with placeholders expanded.
func (p Pin) Member(goarch string) string {
	return Expand(p.ArchiveMember, p.Version, goarch)
}

// Format returns how the artifact at URL(goarch) is packaged.
// Raw when ArchiveMember is empty; otherwise inferred from the URL suffix.
func (p Pin) Format(goarch string) Format {
	if p.ArchiveMember == "" {
		return FormatRaw
	}
	u := p.URL(goarch)
	switch {
	case strings.HasSuffix(u, ".zip"):
		return FormatZip
	case strings.HasSuffix(u, ".tar.gz"), strings.HasSuffix(u, ".tgz"):
		return FormatTarGz
	default:
		return FormatTarGz
	}
}

// SourceSHA256 returns the sha256 of the bytes served at URL(goarch).
// Returns a wrapped ErrUnsupportedArch when the entry is absent or empty.
func (p Pin) SourceSHA256(goarch string) (string, error) {
	h := p.SHA256ByGoArch[goarch]
	if h == "" {
		return "", fmt.Errorf("%w: arch=%s name=%s", ErrUnsupportedArch, goarch, p.Name)
	}
	return h, nil
}

// BinarySHA256 returns the sha256 of the extracted executable.
// For raw pins (ArchiveMember=="") this is identical to SourceSHA256.
// Returns a wrapped ErrUnsupportedArch when the entry is absent or empty.
func (p Pin) BinarySHA256(goarch string) (string, error) {
	if p.ArchiveMember == "" {
		return p.SourceSHA256(goarch)
	}
	h := p.BinarySHA256ByGoArch[goarch]
	if h == "" {
		return "", fmt.Errorf("%w: arch=%s name=%s", ErrUnsupportedArch, goarch, p.Name)
	}
	return h, nil
}

// Downloadable reports whether all information required to fetch and verify
// the executable for goarch is present.
func (p Pin) Downloadable(goarch string) bool {
	if p.URL(goarch) == "" {
		return false
	}
	if _, err := p.SourceSHA256(goarch); err != nil {
		return false
	}
	if _, err := p.BinarySHA256(goarch); err != nil {
		return false
	}
	return true
}
