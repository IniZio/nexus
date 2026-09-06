package service

// seed_usermount_farm_test.go — unit tests for the curated PATH-dir symlink
// farm step of SeedGuestUserMounts (S6-AC2, S6-AC3, S6-AC4).
//
// These tests are in package service (internal) to access buildUserMountScript.
// They render the script, substitute test-local paths via strings.ReplaceAll,
// then run it with exec.Command("/bin/sh", ...) against a real fixture tree —
// so the assertions exercise the rendered shell code, not a hand-built stand-in.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fixtureCuratedManifest builds a UserMountManifest with one curated row:
// stagingDir → guestDir.
func fixtureCuratedManifest(stagingDir, guestDir string) UserMountManifest {
	return UserMountManifest{
		HostHome: "/root",
		Mounts: []ResolvedUserMount{
			{
				HostPath:         stagingDir, // not used by seed script itself
				GuestPath:        guestDir,
				Overlay:          false,
				Curated:          true,
				StagingGuestPath: stagingDir,
			},
		},
	}
}

// runScript renders the script for manifest, substitutes test-local paths for
// all guest-absolute paths that need permissions, then runs it under /bin/sh.
// Substitutions: GuestNativePATH, GuestUserMountsFarmReport,
// GuestUserMountsProfilePath (to avoid writing to /etc/profile.d/).
func runScript(t *testing.T, manifest UserMountManifest, nativePath, reportPath string) (string, error) {
	t.Helper()
	tmpProfile := filepath.Join(t.TempDir(), "nexus3-usermounts.sh")
	script := buildUserMountScript(manifest)
	script = strings.ReplaceAll(script, GuestNativePATH, nativePath)
	script = strings.ReplaceAll(script, GuestUserMountsFarmReport, reportPath)
	script = strings.ReplaceAll(script, GuestUserMountsProfilePath, tmpProfile)
	out, err := exec.Command("/bin/sh", "-c", script).CombinedOutput()
	return string(out), err
}

// TestSeedUserMountFarm_SymlinkTargetIsStaging is the mutation guard for S6-AC2.
//
// Asserts that every symlink created in the guest dir points INTO the staging
// dir (StagingGuestPath), NOT back into the guest dir itself. A hard-link farm
// or a farm that links into GuestPath would fail this test.
//
// Mutation applied: temporarily change buildUserMountScript to link into the
// guest dir instead of the staging dir, verify RED, restore.
func TestSeedUserMountFarm_SymlinkTargetIsStaging(t *testing.T) {
	dir := t.TempDir()
	stagingDir := filepath.Join(dir, "staging")
	guestDir := filepath.Join(dir, "farm")
	nativeDir := filepath.Join(dir, "native")
	reportPath := filepath.Join(dir, "hostbin.report")

	// Set up staging: one real file.
	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "herdr"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Native PATH dir: empty (herdr is not native).
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := fixtureCuratedManifest(stagingDir, guestDir)
	if _, err := runScript(t, manifest, nativeDir, reportPath); err != nil {
		t.Fatalf("script failed: %v", err)
	}

	// Check that the symlink target is under stagingDir, not guestDir.
	link := filepath.Join(guestDir, "herdr")
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("herdr symlink not created: %v", err)
	}
	if !strings.HasPrefix(target, stagingDir) {
		t.Errorf("symlink target %q does not point into staging dir %q — "+
			"link target must be StagingGuestPath/<name>, not GuestPath/<name>; "+
			"a hard link or wrong-target link cannot pass the AC7 atomic-rename proof",
			target, stagingDir)
	}
}

// TestSeedUserMountFarm_NoWriteOnceGuard verifies S6-AC3:
// running the script a second time still rebuilds the farm (no if-[ ! -f ] guard
// on the curated step). The PATH drop-in (Step 2) DOES have a write-once guard —
// this test confirms that guard does NOT apply to the curated Step 4.
func TestSeedUserMountFarm_NoWriteOnceGuard(t *testing.T) {
	dir := t.TempDir()
	stagingDir := filepath.Join(dir, "staging")
	guestDir := filepath.Join(dir, "farm")
	nativeDir := filepath.Join(dir, "native")
	reportPath := filepath.Join(dir, "hostbin.report")

	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "tool-a"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := fixtureCuratedManifest(stagingDir, guestDir)

	// First run.
	if _, err := runScript(t, manifest, nativeDir, reportPath); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	link1 := filepath.Join(guestDir, "tool-a")
	if _, err := os.Lstat(link1); err != nil {
		t.Fatalf("tool-a not created after first run: %v", err)
	}

	// Remove the link to simulate a stale state.
	if err := os.Remove(link1); err != nil {
		t.Fatal(err)
	}

	// Second run — must recreate the link without a write-once guard.
	if _, err := runScript(t, manifest, nativeDir, reportPath); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if _, err := os.Lstat(link1); err != nil {
		t.Fatalf("tool-a not recreated on second run — curated farm has a write-once guard; "+
			"the step must run unconditionally on every boot (S6-AC3): %v", err)
	}

	// Verify the PATH drop-in DOES have a write-once guard (if profile exists,
	// a second run must not overwrite it). runScript redirects the profile path
	// to a fresh temp file on each call. To test the guard, pre-create the
	// profile with a canary and confirm it survives the second run.
	canary := "# canary"
	// Write the canary directly into a temp profile, then run the script with
	// that profile path already present — the if-[ ! -f ] guard must skip it.
	tmpProfile2 := filepath.Join(t.TempDir(), "nexus3-usermounts.sh")
	if err := os.WriteFile(tmpProfile2, []byte(canary), 0o644); err != nil {
		t.Fatal(err)
	}
	script := buildUserMountScript(manifest)
	script = strings.ReplaceAll(script, GuestNativePATH, nativeDir)
	script = strings.ReplaceAll(script, GuestUserMountsFarmReport, reportPath)
	script = strings.ReplaceAll(script, GuestUserMountsProfilePath, tmpProfile2)
	out2, err2 := exec.Command("/bin/sh", "-c", script).CombinedOutput()
	if err2 != nil {
		t.Fatalf("guard-check run failed: %v\noutput: %s", err2, out2)
	}
	data, _ := os.ReadFile(tmpProfile2)
	if string(data) != canary {
		t.Errorf("PATH drop-in was overwritten on second run (should be write-once); "+
			"got %q, want %q", string(data), canary)
	}
}

// TestSeedUserMountFarm_ContainmentSubPath verifies the containment farm path:
// when CuratedSubPath is non-empty the farm is built at GuestPath/CuratedSubPath
// (not at GuestPath), the symlink targets point into StagingGuestPath/CuratedSubPath,
// and non-farm siblings from staging are linked idempotently into GuestPath so
// the rest of the mount's data (e.g. mise/installs/) is accessible at the
// expected path without going through the staging path directly.
//
// Without the containment branch in buildUserMountScript step 4, CuratedSubPath
// is ignored and the script falls through to the exact-match branch, which would
// rm -rf GuestPath and build the farm there — destroying the parent and the
// compatibility symlinks.
func TestSeedUserMountFarm_ContainmentSubPath(t *testing.T) {
	dir := t.TempDir()
	stagingDir := filepath.Join(dir, "staging")  // = /run/nexus3/usermount/bin-mise
	guestDir := filepath.Join(dir, "mise")       // = /root/.local/share/mise
	nativeDir := filepath.Join(dir, "native")
	reportPath := filepath.Join(dir, "hostbin.report")

	// staging/shims/mise-tool — real shim script in the curated subdir.
	shimsStaging := filepath.Join(stagingDir, "shims")
	if err := os.MkdirAll(shimsStaging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shimsStaging, "mise-tool"), []byte("#!/bin/sh\necho ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// staging/installs — non-farm sibling; must be accessible at guestDir/installs.
	installsStaging := filepath.Join(stagingDir, "installs")
	if err := os.MkdirAll(installsStaging, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	manifest := UserMountManifest{
		HostHome: "/root",
		Mounts: []ResolvedUserMount{
			{
				HostPath:         stagingDir,
				GuestPath:        guestDir,
				Curated:          true,
				CuratedSubPath:   "shims",
				StagingGuestPath: stagingDir,
			},
		},
	}
	out, err := runScript(t, manifest, nativeDir, reportPath)
	if err != nil {
		t.Fatalf("containment script failed: %v\noutput: %s", err, out)
	}

	// Farm must be at guestDir/shims, not at guestDir.
	farmDir := filepath.Join(guestDir, "shims")
	link := filepath.Join(farmDir, "mise-tool")
	target, lerr := os.Readlink(link)
	if lerr != nil {
		t.Fatalf("mise-tool symlink not created in farm dir %s: %v", farmDir, lerr)
	}
	if !strings.HasPrefix(target, shimsStaging) {
		t.Errorf("symlink target %q must point into staging shims dir %q", target, shimsStaging)
	}

	// Non-farm sibling (installs) must be linked idempotently into guestDir.
	sibling := filepath.Join(guestDir, "installs")
	sibTarget, serr := os.Readlink(sibling)
	if serr != nil {
		t.Fatalf("non-farm sibling 'installs' not linked into guest dir %s: %v", guestDir, serr)
	}
	if sibTarget != installsStaging {
		t.Errorf("sibling 'installs' target = %q, want %q", sibTarget, installsStaging)
	}

	// Report must record the linked shim.
	reportData, rerr := os.ReadFile(reportPath)
	if rerr != nil {
		t.Fatalf("hostbin.report not created: %v", rerr)
	}
	if !strings.Contains(string(reportData), "linked mise-tool") {
		t.Errorf("report must contain 'linked mise-tool'; got:\n%s", reportData)
	}
}

// TestSeedUserMountFarm_ExclusionRules verifies S6-AC4:
// - a name resolving on the guest-native PATH is excluded (shadowed)
// - a name that does not resolve inside the guest is excluded (dangling)
// - every other name is linked
// Driven through the rendered script against a fixture tree.
func TestSeedUserMountFarm_ExclusionRules(t *testing.T) {
	dir := t.TempDir()
	stagingDir := filepath.Join(dir, "staging")
	guestDir := filepath.Join(dir, "farm")
	nativeDir := filepath.Join(dir, "native") // plays the role of GuestNativePATH
	reportPath := filepath.Join(dir, "hostbin.report")

	if err := os.MkdirAll(stagingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(nativeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// "herdr" — real file in staging, not in native → should be linked.
	if err := os.WriteFile(filepath.Join(stagingDir, "herdr"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// "nexus3" — real file in staging AND in native → should be shadowed.
	if err := os.WriteFile(filepath.Join(stagingDir, "nexus3"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nativeDir, "nexus3"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// "cursor-agent" — dangling symlink in staging (target absent) → dangling.
	if err := os.Symlink("/nonexistent/cursor-agent-binary", filepath.Join(stagingDir, "cursor-agent")); err != nil {
		t.Fatal(err)
	}

	manifest := fixtureCuratedManifest(stagingDir, guestDir)
	out, err := runScript(t, manifest, nativeDir, reportPath)
	if err != nil {
		t.Fatalf("script failed: %v\noutput: %s", err, out)
	}

	// herdr must be linked.
	herdrLink := filepath.Join(guestDir, "herdr")
	if _, err := os.Lstat(herdrLink); err != nil {
		t.Errorf("herdr not linked: %v", err)
	}
	if target, err := os.Readlink(herdrLink); err != nil || !strings.HasPrefix(target, stagingDir) {
		t.Errorf("herdr target %q not under staging %q", target, stagingDir)
	}

	// nexus3 must NOT be in the farm (shadowed).
	if _, err := os.Lstat(filepath.Join(guestDir, "nexus3")); err == nil {
		t.Error("nexus3 should be shadowed (resolves on native PATH) but is present in farm")
	}

	// cursor-agent must NOT be in the farm (dangling).
	if _, err := os.Lstat(filepath.Join(guestDir, "cursor-agent")); err == nil {
		t.Error("cursor-agent should be dangling (broken host symlink) but is present in farm")
	}

	// Report file must exist and contain the three labels.
	reportData, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("hostbin.report not created: %v", err)
	}
	report := string(reportData)
	if !strings.Contains(report, "linked herdr") {
		t.Errorf("report missing 'linked herdr'; got:\n%s", report)
	}
	if !strings.Contains(report, "shadowed nexus3") {
		t.Errorf("report missing 'shadowed nexus3'; got:\n%s", report)
	}
	if !strings.Contains(report, "dangling cursor-agent") {
		t.Errorf("report missing 'dangling cursor-agent'; got:\n%s", report)
	}
}
