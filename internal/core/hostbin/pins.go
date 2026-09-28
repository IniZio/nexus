// Package hostbin delivers pinned host executables embedded in the nexus binary.
package hostbin

import "github.com/IniZio/nexus/internal/core/hostbin/pin"

const (
	// CloudHypervisor is the canonical binary name for the cloud-hypervisor VMM.
	CloudHypervisor        = "cloud-hypervisor"
	CloudHypervisorVersion = "53.0"
	VirtiofsdVersion       = "1.14.0"
	E2fsprogsVersion       = "1.47.2"
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
				"amd64": "https://gitlab.com/-/project/21523468/uploads/f505704014ae7a816e515f2a05a93d8b/virtiofsd-v1.14.0.zip",
			},
			SHA256ByGoArch: map[string]string{
				"amd64": "2e4fe9571f492b00baa34bc4e708e950039c5da05b830b31a8d179cb6ac8978e",
			},
			ArchiveMember: "target/{ARCH}-unknown-linux-musl/release/virtiofsd",
			BinarySHA256ByGoArch: map[string]string{
				"amd64": "15b2e72a78cc08a9bd8a6943e89fb69c88cb3cbeb63069efceade835342ac7d4",
			},
			Note: "upstream release zip ships x86_64 musl static only; arm64 TODO: build in release CI",
		},
		"mke2fs": {
			Name:    "mke2fs",
			Version: E2fsprogsVersion,
			Note:    "TODO(S3): no upstream static e2fsprogs binary; built by release CI (musl static), pin filled when that artifact exists",
		},
		"e2fsck": {
			Name:    "e2fsck",
			Version: E2fsprogsVersion,
			Note:    "TODO(S3): no upstream static e2fsprogs binary; built by release CI (musl static), pin filled when that artifact exists",
		},
		"resize2fs": {
			Name:    "resize2fs",
			Version: E2fsprogsVersion,
			Note:    "TODO(S3): no upstream static e2fsprogs binary; built by release CI (musl static), pin filled when that artifact exists",
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
