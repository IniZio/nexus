package image

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"time"

	"github.com/IniZio/nexus/internal/openshell"
)

// BoundaryConfig mirrors OpenShell 484f076 boundary_protocol.rs (deny_unknown_fields).
type BoundaryConfig struct {
	BoundaryID         string            `json:"boundary_id"`
	Generation         string            `json:"generation"`
	SessionID          string            `json:"session_id"`
	SessionRotation    uint64            `json:"session_rotation"`
	AuthEpoch          uint64            `json:"auth_epoch"`
	GatewayID          string            `json:"gateway_id"`
	VerificationKeys   []VerificationKey `json:"verification_keys"`
	Listener           BoundaryListener  `json:"listener"`
	ResourceClaims     map[string]string `json:"resource_claims"`
	ResourceClaimFiles map[string]string `json:"resource_claim_files"`
	WorkloadIdentity   WorkloadIdentity  `json:"workload_identity"`
	DriverFence        DriverFence       `json:"driver_fence"`
	ChildEnv           map[string]string `json:"child_env"`
}

type VerificationKey struct {
	KeyID        string `json:"key_id"`
	PublicKeyPEM string `json:"public_key_pem"`
}

type BoundaryListener struct {
	Kind        string      `json:"kind"`
	ControlPort uint32      `json:"control_port"`
	TLS         ListenerTLS `json:"tls"`
}

type ListenerTLS struct {
	CertificateChainPath string `json:"certificate_chain_path"`
	PrivateKeyPath       string `json:"private_key_path"`
}

type WorkloadIdentity struct {
	UID uint32 `json:"uid"`
	GID uint32 `json:"gid"`
}

type DriverFence struct {
	Kind               string `json:"kind"`
	Generation         string `json:"generation"`
	NetworkDeviceCount int    `json:"network_device_count"`
}

// BootstrapParams are inputs for BootstrapMaterial; AuthEpoch and VerificationKeys must come from the gateway.
type BootstrapParams struct {
	SandboxID        string
	Generation       uint64
	GenerationStr    string
	SessionID        string
	SessionRotation  uint64
	AuthEpoch        uint64
	GatewayID        string
	VerificationKeys []VerificationKey
	ImageIdentity    string
}

// GoldenBoundaryConfigFields lists expected top-level JSON keys; a mismatch signals an upstream schema change.
var GoldenBoundaryConfigFields = []string{
	"boundary_id", "generation", "session_id", "session_rotation",
	"auth_epoch", "gateway_id", "verification_keys", "listener",
	"resource_claims", "resource_claim_files", "workload_identity",
	"driver_fence", "child_env",
}

// BootstrapMaterial generates the TLS pair and BoundaryConfig JSON for a sandbox.
func BootstrapMaterial(params BootstrapParams) (openshell.BootstrapBundle, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return openshell.BootstrapBundle{}, fmt.Errorf("openshell/image: generate ed25519 key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return openshell.BootstrapBundle{}, fmt.Errorf("openshell/image: generate serial: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName: fmt.Sprintf("sandbox.%s.openshell.internal", params.SessionID),
		},
		NotBefore:             now,
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IsCA:                  false,
		BasicConstraintsValid: true,
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return openshell.BootstrapBundle{}, fmt.Errorf("openshell/image: create certificate: %w", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return openshell.BootstrapBundle{}, fmt.Errorf("openshell/image: marshal private key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})

	certPath := fmt.Sprintf("/.openshell/state/sandbox-%d.crt", params.Generation)
	keyPath := fmt.Sprintf("/.openshell/state/sandbox-%d.key", params.Generation)

	cfg := BoundaryConfig{
		BoundaryID:       params.SandboxID,
		Generation:       params.GenerationStr,
		SessionID:        params.SessionID,
		SessionRotation:  params.SessionRotation,
		AuthEpoch:        params.AuthEpoch,
		GatewayID:        params.GatewayID,
		VerificationKeys: params.VerificationKeys,
		Listener: BoundaryListener{
			Kind:        "vsock",
			ControlPort: 5500,
			TLS: ListenerTLS{
				CertificateChainPath: certPath,
				PrivateKeyPath:       keyPath,
			},
		},
		ResourceClaims: map[string]string{
			"vm.generation":     params.GenerationStr,
			"vm.image_identity": params.ImageIdentity,
		},
		ResourceClaimFiles: map[string]string{},
		WorkloadIdentity: WorkloadIdentity{
			UID: 998,
			GID: 998,
		},
		DriverFence: DriverFence{
			Kind:               "vm",
			Generation:         params.GenerationStr,
			NetworkDeviceCount: 0,
		},
		ChildEnv: map[string]string{},
	}

	cfgJSON, err := json.Marshal(cfg)
	if err != nil {
		return openshell.BootstrapBundle{}, fmt.Errorf("openshell/image: marshal BoundaryConfig: %w", err)
	}

	return openshell.BootstrapBundle{
		Generation: params.Generation,
		ConfigJSON: cfgJSON,
		CertPEM:    certPEM,
		KeyPEM:     keyPEM,
	}, nil
}
