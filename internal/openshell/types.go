package openshell

import (
	"context"
	"errors"
	"fmt"
	"net"
)

type Mount struct {
	HostPath  string `json:"host_path"`
	GuestPath string `json:"guest_path"`
	ReadOnly  bool   `json:"read_only"`
}

type Resources struct {
	BootMemBytes    int64 `json:"boot_mem_bytes"`
	CeilingMemBytes int64 `json:"ceiling_mem_bytes"`
	VCPUs           int32 `json:"vcpus"`
}

type SandboxSpec struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	ImageRef    string            `json:"image_ref"`
	Command     []string          `json:"command,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	Mounts      []Mount           `json:"mounts,omitempty"`
	Resources   Resources         `json:"resources"`
	GatewayAddr string            `json:"gateway_addr"`
	Generation  uint64            `json:"generation"`
}

type ImageArtifacts struct {
	RootfsExt4  string `json:"rootfs_ext4"`
	OverlayExt4 string `json:"overlay_ext4"`
	ImageExt4   string `json:"image_ext4,omitempty"`
}

type BootstrapBundle struct {
	Generation uint64 `json:"generation"`
	ConfigJSON []byte `json:"config_json"`
	CertPEM    []byte `json:"cert_pem"`
	KeyPEM     []byte `json:"key_pem"`
}

func (b BootstrapBundle) Files() map[string][]byte {
	gen := b.Generation
	return map[string][]byte{
		fmt.Sprintf("/.openshell/state/bootstrap-%d.json", gen): b.ConfigJSON,
		fmt.Sprintf("/.openshell/state/sandbox-%d.crt", gen):    b.CertPEM,
		fmt.Sprintf("/.openshell/state/sandbox-%d.key", gen):    b.KeyPEM,
	}
}

// ImageBuilder converts an OCI image into bootable ext4 images. Consumed by S1.
type ImageBuilder interface {
	PrepareRootfs(ctx context.Context, imageRef string) (rootfsExt4 string, digest string, err error)
	BuildOverlay(ctx context.Context, dir string, bundle BootstrapBundle, extra map[string][]byte) (overlayExt4 string, err error)
}

type RuntimeIdentity struct {
	PID         int    `json:"pid"`
	APISocket   string `json:"api_socket"`
	VsockSocket string `json:"vsock_socket"`
	StateDir    string `json:"state_dir"`
}

// VMHandle is the live handle to a running VM. Consumed by S2, S4, S6, S7.
type VMHandle interface {
	ID() string
	CID() uint32
	ControlSocket() string
	DialGuest(ctx context.Context, port uint32) (net.Conn, error)
	Stop(ctx context.Context) error
	Kill() error
	Wait() <-chan error
	Runtime() RuntimeIdentity
}

// Backend manages the cloud-hypervisor process lifecycle. Consumed by S2.
type Backend interface {
	Boot(ctx context.Context, spec SandboxSpec, images ImageArtifacts) (VMHandle, error)
	Adopt(ctx context.Context, id string) (VMHandle, error)
}

// RelayListener multiplexes AF_VSOCK connections. Consumed by S3, S6.
type RelayListener interface {
	Listen(ctx context.Context, port uint32, handler func(net.Conn)) error
}

// ErrUnsupported is returned when the platform lacks the requested capability. Consumed by S2, S4.
type ErrUnsupported struct {
	Capability string
}

func (e ErrUnsupported) Error() string {
	return fmt.Sprintf("openshell: capability not supported: %s", e.Capability)
}

func IsUnsupported(err error) bool {
	var e ErrUnsupported
	return errors.As(err, &e)
}
