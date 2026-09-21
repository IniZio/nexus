package image

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/IniZio/nexus/internal/openshell"
)

func mkfsAvailable() bool {
	if _, err := exec.LookPath("mkfs.ext4"); err == nil {
		return true
	}
	_, err := exec.LookPath("mke2fs")
	return err == nil
}

func debugfsAvailable() bool {
	_, err := exec.LookPath("debugfs")
	return err == nil
}

func fakeExtractor(wantDigest string) ociExtractor {
	return func(_ context.Context, _ string, destDir string) (string, error) {
		if err := os.WriteFile(filepath.Join(destDir, "hello.txt"), []byte("hello"), 0o644); err != nil {
			return "", fmt.Errorf("fake extractor: write hello.txt: %w", err)
		}
		return wantDigest, nil
	}
}

func TestPrepareRootfs_CacheHit(t *testing.T) {
	if !mkfsAvailable() {
		t.Skip("mkfs.ext4/mke2fs not found in PATH")
	}

	dir := t.TempDir()
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	calls := 0

	countingExtractor := func(ctx context.Context, ref string, destDir string) (string, error) {
		calls++
		return fakeExtractor(digest)(ctx, ref, destDir)
	}

	b := New(dir, withExtractor(countingExtractor))
	ctx := context.Background()

	path1, got1, err := b.PrepareRootfs(ctx, "fake/image:v1")
	if err != nil {
		t.Fatalf("PrepareRootfs first call: %v", err)
	}
	if got1 != digest {
		t.Errorf("first call digest = %q, want %q", got1, digest)
	}
	if calls != 1 {
		t.Errorf("extractor called %d times after first PrepareRootfs, want 1", calls)
	}

	path2, got2, err := b.PrepareRootfs(ctx, "fake/image:v1")
	if err != nil {
		t.Fatalf("PrepareRootfs second call: %v", err)
	}
	if calls != 1 {
		t.Errorf("extractor called %d times after second PrepareRootfs, want 1 (cache hit)", calls)
	}
	if path1 != path2 {
		t.Errorf("path changed between calls: %q vs %q", path1, path2)
	}
	if got2 != digest {
		t.Errorf("second call digest = %q, want %q", got2, digest)
	}
	if _, err := os.Stat(path1); err != nil {
		t.Errorf("cached ext4 not found at %s: %v", path1, err)
	}
}

func TestBuildOverlay_ContentAndModes(t *testing.T) {
	if !mkfsAvailable() {
		t.Skip("mkfs.ext4/mke2fs not found in PATH")
	}
	if !debugfsAvailable() {
		t.Skip("debugfs not found in PATH")
	}

	dir := t.TempDir()
	b := New(dir)
	ctx := context.Background()

	gen := uint64(7)
	bundle := openshell.BootstrapBundle{
		Generation: gen,
		ConfigJSON: []byte(`{"boundary_id":"test"}`),
		CertPEM:    []byte("CERTPEM"),
		KeyPEM:     []byte("KEYPEM"),
	}
	envContent := []byte("export OPENSHELL_VM_SANDBOX_BOOTSTRAP=/.openshell/state/bootstrap-7.json\n")
	extra := map[string][]byte{"/srv/openshell-env.sh": envContent}

	overlayPath, err := b.BuildOverlay(ctx, dir, bundle, extra)
	if err != nil {
		t.Fatalf("BuildOverlay: %v", err)
	}
	if _, err := os.Stat(overlayPath); err != nil {
		t.Fatalf("overlay.ext4 not found at %s: %v", overlayPath, err)
	}

	type fileCheck struct {
		guestPath   string
		wantContent []byte
		wantMode    string
	}
	checks := []fileCheck{
		{fmt.Sprintf("/.openshell/state/bootstrap-%d.json", gen), bundle.ConfigJSON, "0600"},
		{fmt.Sprintf("/.openshell/state/sandbox-%d.crt", gen), bundle.CertPEM, "0600"},
		{fmt.Sprintf("/.openshell/state/sandbox-%d.key", gen), bundle.KeyPEM, "0600"},
		{"/srv/openshell-env.sh", envContent, "0755"},
	}

	for _, c := range checks {
		c := c
		t.Run(c.guestPath, func(t *testing.T) {
			upperPath := "upper" + c.guestPath

			catOut, err := exec.Command("debugfs", "-R", "cat "+upperPath, overlayPath).Output()
			if err != nil {
				t.Fatalf("debugfs cat %s: %v", upperPath, err)
			}
			if string(catOut) != string(c.wantContent) {
				t.Errorf("content mismatch for %s:\ngot  %q\nwant %q", c.guestPath, catOut, c.wantContent)
			}

			statOut, err := exec.Command("debugfs", "-R", "stat "+upperPath, overlayPath).CombinedOutput()
			if err != nil {
				t.Fatalf("debugfs stat %s: %v\n%s", upperPath, err, statOut)
			}
			if !strings.Contains(string(statOut), "Mode:  "+c.wantMode) {
				t.Errorf("mode mismatch for %s: want Mode: %s in:\n%s", c.guestPath, c.wantMode, statOut)
			}
		})
	}
}

func TestBoundaryConfigGoldenFields(t *testing.T) {
	cfg := BoundaryConfig{
		ResourceClaimFiles: map[string]string{},
		ResourceClaims:     map[string]string{},
		ChildEnv:           map[string]string{},
		VerificationKeys:   []VerificationKey{},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal BoundaryConfig: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	got := make([]string, 0, len(m))
	for k := range m {
		got = append(got, k)
	}
	sort.Strings(got)

	want := make([]string, len(GoldenBoundaryConfigFields))
	copy(want, GoldenBoundaryConfigFields)
	sort.Strings(want)

	if len(got) != len(want) {
		t.Errorf("field count: got %d (%v), want %d (%v)", len(got), got, len(want), want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("field[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBootstrapMaterial_TLSParsesAndVerifies(t *testing.T) {
	params := BootstrapParams{
		SandboxID:        "aaaabbbb-0000-0000-0000-ccccddddeeee",
		Generation:       1,
		GenerationStr:    "1",
		SessionID:        "11112222-3333-4444-5555-666677778888",
		SessionRotation:  0,
		AuthEpoch:        42,
		GatewayID:        "openshell",
		VerificationKeys: []VerificationKey{{KeyID: "k1", PublicKeyPEM: "PUBPEM"}},
		ImageIdentity:    "sha256:deadbeef",
	}

	bundle, err := BootstrapMaterial(params)
	if err != nil {
		t.Fatalf("BootstrapMaterial: %v", err)
	}

	blk, _ := pem.Decode(bundle.CertPEM)
	if blk == nil {
		t.Fatal("CertPEM: no PEM block found")
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatalf("x509.ParseCertificate: %v", err)
	}

	kblk, _ := pem.Decode(bundle.KeyPEM)
	if kblk == nil {
		t.Fatal("KeyPEM: no PEM block found")
	}
	if _, err := x509.ParsePKCS8PrivateKey(kblk.Bytes); err != nil {
		t.Fatalf("x509.ParsePKCS8PrivateKey: %v", err)
	}

	wantCN := "sandbox." + params.SessionID + ".openshell.internal"
	if cert.Subject.CommonName != wantCN {
		t.Errorf("cert CN = %q, want %q", cert.Subject.CommonName, wantCN)
	}
	if cert.IsCA {
		t.Error("cert.IsCA = true, want false")
	}

	var cfg BoundaryConfig
	if err := json.Unmarshal(bundle.ConfigJSON, &cfg); err != nil {
		t.Fatalf("unmarshal ConfigJSON: %v", err)
	}
	if cfg.BoundaryID != params.SandboxID {
		t.Errorf("cfg.BoundaryID = %q, want %q", cfg.BoundaryID, params.SandboxID)
	}
	if cfg.Listener.Kind != "vsock" {
		t.Errorf("cfg.Listener.Kind = %q, want vsock", cfg.Listener.Kind)
	}
	if cfg.Listener.ControlPort != 5500 {
		t.Errorf("cfg.Listener.ControlPort = %d, want 5500", cfg.Listener.ControlPort)
	}
	wantCertPath := fmt.Sprintf("/.openshell/state/sandbox-%d.crt", params.Generation)
	if cfg.Listener.TLS.CertificateChainPath != wantCertPath {
		t.Errorf("cfg.Listener.TLS.CertificateChainPath = %q, want %q", cfg.Listener.TLS.CertificateChainPath, wantCertPath)
	}
	if cfg.WorkloadIdentity.UID != 998 || cfg.WorkloadIdentity.GID != 998 {
		t.Errorf("workload identity = {%d,%d}, want {998,998}", cfg.WorkloadIdentity.UID, cfg.WorkloadIdentity.GID)
	}
	if cfg.DriverFence.Kind != "vm" {
		t.Errorf("driver_fence.kind = %q, want vm", cfg.DriverFence.Kind)
	}
	if cfg.ResourceClaimFiles == nil {
		t.Error("resource_claim_files is nil, want empty map")
	}
	if cfg.ChildEnv == nil {
		t.Error("child_env is nil, want empty map")
	}
	if bundle.Generation != params.Generation {
		t.Errorf("bundle.Generation = %d, want %d", bundle.Generation, params.Generation)
	}
}

func TestBootstrapBundle_Files(t *testing.T) {
	gen := uint64(3)
	bundle := openshell.BootstrapBundle{
		Generation: gen,
		ConfigJSON: []byte("cfg"),
		CertPEM:    []byte("cert"),
		KeyPEM:     []byte("key"),
	}
	files := bundle.Files()
	wantKeys := []string{
		fmt.Sprintf("/.openshell/state/bootstrap-%d.json", gen),
		fmt.Sprintf("/.openshell/state/sandbox-%d.crt", gen),
		fmt.Sprintf("/.openshell/state/sandbox-%d.key", gen),
	}
	for _, k := range wantKeys {
		if _, ok := files[k]; !ok {
			t.Errorf("Files() missing key %q", k)
		}
	}
	if len(files) != 3 {
		t.Errorf("Files() len = %d, want 3", len(files))
	}
}
