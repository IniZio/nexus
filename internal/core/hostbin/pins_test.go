package hostbin

import (
	"encoding/hex"
	"strings"
	"testing"
)

func TestPinsKeyMatchesName(t *testing.T) {
	for key, p := range Pins() {
		if key != p.Name {
			t.Errorf("key %q != pin.Name %q", key, p.Name)
		}
	}
}

func TestPinsSHA256Format(t *testing.T) {
	for _, p := range Pins() {
		for arch, h := range p.SHA256ByGoArch {
			if len(h) != 64 {
				t.Errorf("%s[%s] SHA256ByGoArch: len=%d want 64", p.Name, arch, len(h))
			}
			if h != strings.ToLower(h) {
				t.Errorf("%s[%s] SHA256ByGoArch: not lowercase", p.Name, arch)
			}
			if _, err := hex.DecodeString(h); err != nil {
				t.Errorf("%s[%s] SHA256ByGoArch: invalid hex: %v", p.Name, arch, err)
			}
		}
		for arch, h := range p.BinarySHA256ByGoArch {
			if len(h) != 64 {
				t.Errorf("%s[%s] BinarySHA256ByGoArch: len=%d want 64", p.Name, arch, len(h))
			}
			if h != strings.ToLower(h) {
				t.Errorf("%s[%s] BinarySHA256ByGoArch: not lowercase", p.Name, arch)
			}
			if _, err := hex.DecodeString(h); err != nil {
				t.Errorf("%s[%s] BinarySHA256ByGoArch: invalid hex: %v", p.Name, arch, err)
			}
		}
	}
}

func TestDownloadableCloudHypervisor(t *testing.T) {
	pins := Pins()
	ch := pins["cloud-hypervisor"]
	if !ch.Downloadable("amd64") {
		t.Error("cloud-hypervisor amd64 should be downloadable")
	}
	if !ch.Downloadable("arm64") {
		t.Error("cloud-hypervisor arm64 should be downloadable")
	}
}

func TestDownloadableVirtiofsd(t *testing.T) {
	pins := Pins()
	v := pins["virtiofsd"]
	if !v.Downloadable("amd64") {
		t.Error("virtiofsd amd64 should be downloadable")
	}
	if v.Downloadable("arm64") {
		t.Error("virtiofsd arm64 should not be downloadable")
	}
}

func TestE2fsprogsDownloadable(t *testing.T) {
	pins := Pins()
	for _, name := range []string{Mke2fs, E2fsck, Resize2fs} {
		p, ok := pins[name]
		if !ok {
			t.Errorf("pin %q missing", name)
			continue
		}
		if !p.Downloadable("amd64") {
			t.Errorf("%s[amd64] should be downloadable", name)
		}
		if p.Downloadable("arm64") {
			t.Errorf("%s[arm64] should not be downloadable", name)
		}
		u := p.URL("amd64")
		if !strings.Contains(u, "e2fsprogs-v1.47.2") {
			t.Errorf("%s[amd64] URL %q does not contain e2fsprogs-v1.47.2", name, u)
		}
	}
}

func TestPinsNoKernel(t *testing.T) {
	if _, ok := Pins()["vmlinux"]; ok {
		t.Error("Pins() must not contain vmlinux: kernel is download-only, use KernelPin()")
	}
}

func TestKernelPinDownloadable(t *testing.T) {
	p := KernelPin()
	if !p.Downloadable("amd64") {
		t.Error("KernelPin amd64 should be downloadable")
	}
	if p.Downloadable("arm64") {
		t.Error("KernelPin arm64 should not be downloadable (no upstream asset)")
	}
}

func TestPinsFreshMap(t *testing.T) {
	m1 := Pins()
	m2 := Pins()
	// Mutating m1 must not affect m2.
	delete(m1, "cloud-hypervisor")
	if _, ok := m2["cloud-hypervisor"]; !ok {
		t.Error("Pins() did not return a fresh map: delete in one affected another")
	}
}
