// Package hostbin delivers pinned host executables embedded in the nexus binary.
package hostbin

import "github.com/IniZio/nexus/internal/core/hostbin/pin"

const (
	// CloudHypervisor is the canonical binary name for the cloud-hypervisor VMM.
	CloudHypervisor        = "cloud-hypervisor"
	CloudHypervisorVersion = "53.0"
	VirtiofsdVersion       = "1.13.3-fakeowner"
	E2fsprogsVersion       = "1.47.2"

	// Mke2fs, E2fsck, and Resize2fs are canonical names for the static musl
	// e2fsprogs binaries built by scripts/e2fsprogs/build.sh and published as
	// GitHub release assets.
	Mke2fs    = "mke2fs"
	E2fsck    = "e2fsck"
	Resize2fs = "resize2fs"
)

// Pins returns a fresh map of all pinned host executables, keyed by Name.
func Pins() map[string]pin.Pin {
	return map[string]pin.Pin{
		CloudHypervisor: {
			Name:    CloudHypervisor,
			Version: CloudHypervisorVersion,
			URLByGoArch: map[string]string{
				"amd64": "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v{VERSION}/cloud-hypervisor-static",
				"arm64": "https://github.com/cloud-hypervisor/cloud-hypervisor/releases/download/v{VERSION}/cloud-hypervisor-static-aarch64",
			},
			SHA256ByGoArch: map[string]string{
				"amd64": "448af3d4e59b22c2987f7df94c213ad40fb53a10d437e42b5ee6c4fce7c29ecc",
				"arm64": "f192b510eea1c710cbc439d716bb0573c223fc463dbe3e6523788a2b7ef62850",
			},
			Note: "upstream release asset (static)",
		},
		"virtiofsd": {
			Name:    "virtiofsd",
			Version: VirtiofsdVersion,
			URLByGoArch: map[string]string{
				"amd64": "https://github.com/IniZio/nexus/releases/download/virtiofsd-v1.13.3-fakeowner/virtiofsd-amd64",
			},
			SHA256ByGoArch: map[string]string{
				"amd64": "6ec8fd513874e28825c18b0107fc43599350c4166bf6fe9dd0adc97f4bde3e6e",
			},
			BinarySHA256ByGoArch: map[string]string{
				"amd64": "6ec8fd513874e28825c18b0107fc43599350c4166bf6fe9dd0adc97f4bde3e6e",
			},
			Note: "nexus patched build (v1.13.3 + fakeowner.patch); musl static amd64; arm64 TODO: build in release CI",
		},
		Mke2fs: {
			Name:    Mke2fs,
			Version: E2fsprogsVersion,
			URLByGoArch: map[string]string{
				"amd64": "https://github.com/IniZio/nexus/releases/download/e2fsprogs-v{VERSION}/mke2fs-x86_64",
			},
			SHA256ByGoArch: map[string]string{
				"amd64": "660ed81e6b025adf8c5158f5b1157fc93aa2508508144cf9dc5fab392145f4a9",
			},
			Note: "static musl build of upstream e2fsprogs via scripts/e2fsprogs/build.sh; published as our GitHub release asset; arm64 TODO",
		},
		E2fsck: {
			Name:    E2fsck,
			Version: E2fsprogsVersion,
			URLByGoArch: map[string]string{
				"amd64": "https://github.com/IniZio/nexus/releases/download/e2fsprogs-v{VERSION}/e2fsck-x86_64",
			},
			SHA256ByGoArch: map[string]string{
				"amd64": "3941a0b8844abc31a3c66f5486958d650e3f5ae9908b1e3dad72a7393ed21a37",
			},
			Note: "static musl build of upstream e2fsprogs via scripts/e2fsprogs/build.sh; published as our GitHub release asset; arm64 TODO",
		},
		Resize2fs: {
			Name:    Resize2fs,
			Version: E2fsprogsVersion,
			URLByGoArch: map[string]string{
				"amd64": "https://github.com/IniZio/nexus/releases/download/e2fsprogs-v{VERSION}/resize2fs-x86_64",
			},
			SHA256ByGoArch: map[string]string{
				"amd64": "c260e90ef1586188704f4cb31e03ae09574079f8d3fdb6556083544e96f44911",
			},
			Note: "static musl build of upstream e2fsprogs via scripts/e2fsprogs/build.sh; published as our GitHub release asset; arm64 TODO",
		},
	}
}

// KernelPin returns the pinned guest kernel descriptor.
// It is intentionally separate from Pins() so the artifact embedder does not
// pick it up: the kernel is download-only, never embedded in the binary.
func KernelPin() pin.Pin {
	return pin.Pin{
		Name:    "vmlinux",
		Version: "0.24.0",
		URLByGoArch: map[string]string{
			"amd64": "https://github.com/IniZio/nexus/releases/download/v{VERSION}/vmlinux-x86_64",
		},
		SHA256ByGoArch: map[string]string{
			"amd64": "ebc744eded3ccc69c404b9cd3c380f28b127b04b2741cc90f2c84aad687d975d",
		},
		Note: "guest kernel (raw); arm64 TODO: no upstream asset in releases",
	}
}
