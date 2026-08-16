// Package diskname provides pure-string predicates for nexus3 disk filename
// conventions. It has no dependencies beyond the standard library and may be
// imported by any package in the stack without creating import cycles.
package diskname

import "strings"

// IsShadowDisk reports whether basename is a shadow disk image in either the
// B1 format or the legacy pre-B1 format.
//
// Both forms share two structural properties: the ".ext4" suffix and the
// ".shadow." infix. A file that satisfies both is a shadow disk; anything
// else (ULID .raw, workspace -workspace.ext4, intent .json) is not.
//
// This is the canonical implementation. cli.IsShadowDisk and any service-layer
// copies must delegate here or be pinned to this contract by a test.
func IsShadowDisk(basename string) bool {
	return strings.HasSuffix(basename, ".ext4") && strings.Contains(basename, ".shadow.")
}

// ShadowDiskSafeHandle returns the safeHandle component of a B1-format shadow
// disk filename.
//
// The safeHandle is the sandbox handle with "/" replaced by "_"; callers
// correlate it against a live sandbox by comparing it to
// strings.ReplaceAll(sb.Handle(), "/", "_").
//
// Returns ("", false) for:
//   - legacy (pre-B1) files that end in ".shadow.ext4" — these match no
//     live sandbox and are unconditionally reclaimable by the reaper
//   - files that are not shadow disks at all
//
// B1 safeHandle extraction: the token before the first ".shadow." infix.
// Examples:
//
//	"example-app_parallel-a.shadow.node_modules.ext4" → "example-app_parallel-a", true
//	"node_modules.shadow.ext4"                        → "", false  (legacy)
//	"dist.shadow.ext4"                                → "", false  (legacy)
func ShadowDiskSafeHandle(basename string) (safeHandle string, ok bool) {
	if !IsShadowDisk(basename) {
		return "", false
	}
	// Legacy: ends in ".shadow.ext4" — no embedded safeHandle.
	if strings.HasSuffix(basename, ".shadow.ext4") {
		return "", false
	}
	idx := strings.Index(basename, ".shadow.")
	if idx <= 0 {
		return "", false
	}
	return basename[:idx], true
}
