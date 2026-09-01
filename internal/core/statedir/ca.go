package statedir

// ca.go persists the per-sandbox MITM CA (certificate + private key) inside
// the supervisor state dir, so a supervisor that replaces a CRASHED one can
// keep signing leaf certificates the guest already trusts (D-HSH-18,
// ticket 13 / slice s15-ca-persistence).
//
// # Why on disk at all
//
// Before this, the CA existed only in the supervisor process's memory and
// travelled only inside handoff.Payload.CA — which a crashed process never
// sent. So crash recovery restored plain networking but broke every in-guest
// TLS session until the guest re-imported the fresh CA. Re-import was measured
// and rejected: a long-running Node process reads NODE_EXTRA_CA_CERTS ONCE at
// process startup, so no re-import can reach the already-running in-guest
// agent. Persistence is the only mechanism that is transparent to a RUNNING
// guest.
//
// # Why NOT encrypted
//
// Deliberate, per D-HSH-18. TPM2 support on the reference host measured
// PARTIAL, and any wrapping whose unwrap can fail introduces a new way for
// RECOVERY ITSELF to fail — precisely the property being bought. Host-root can
// already read this CA out of the live supervisor's memory, so a wrapped file
// buys nothing against the attacker who matters. [FileMode] (0600) inside
// [DirMode] (0700) is the real boundary, and the file's lifetime is bound to
// the sandbox: Service.Remove deletes the whole state dir.
//
// # Why one file, not two
//
// A cert file and a key file can diverge — one written, the other not, or one
// truncated — and a half pair is exactly what mitm.New refuses. Holding both
// PEM blocks in a single file written by write-temp-then-rename makes the pair
// atomic: a reader sees either the whole previous CA or the whole new one.
// [LoadCA] still re-checks the pair, because atomicity of the write says
// nothing about a file corrupted afterwards.

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// CAFileName is the file inside the per-sandbox state dir that holds the MITM
// CA certificate and private key, in that order, as PEM blocks.
const CAFileName = "mitm-ca.pem"

// ErrCAAbsent reports that no CA has been persisted for this sandbox. It is a
// distinct error from corruption so callers can name the cause in their log
// line: "never persisted" and "persisted then damaged" are different faults
// with different follow-ups.
var ErrCAAbsent = errors.New("statedir: no persisted MITM CA")

// CAPath returns the path [SaveCA] writes and [LoadCA] reads.
func CAPath(dir string) string { return filepath.Join(dir, CAFileName) }

// SaveCA persists certPEM and keyPEM for the sandbox whose state dir is dir.
//
// The write is atomic (temp file in the same directory, then rename) so a
// crash mid-write can never leave a half pair behind for the crash-recovery
// path to trip over — which would be the worst possible timing, since that
// path only runs after a crash.
//
// The temp file is created with [FileMode] AND explicitly chmodded: O_CREATE's
// mode is masked by the process umask, so on a host with a permissive umask
// the private key would land at 0644 without the Chmod. dir is created (and
// tightened) with [Ensure].
func SaveCA(dir string, certPEM, keyPEM []byte) error {
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		// Refuse rather than write a half pair: a file that LoadCA will
		// always reject is worse than no file, because it turns "no CA
		// persisted" into "CA corrupt" and hides the real bug.
		return fmt.Errorf("statedir: save CA: refusing to write a partial pair (cert %d bytes, key %d bytes)", len(certPEM), len(keyPEM))
	}
	if err := Ensure(dir); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, CAFileName+".tmp*")
	if err != nil {
		return fmt.Errorf("statedir: save CA: create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename succeeded

	if err := tmp.Chmod(FileMode); err != nil {
		tmp.Close()
		return fmt.Errorf("statedir: save CA: chmod %s: %w", tmpName, err)
	}
	var buf bytes.Buffer
	buf.Write(certPEM)
	if !bytes.HasSuffix(certPEM, []byte("\n")) {
		buf.WriteByte('\n')
	}
	buf.Write(keyPEM)
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return fmt.Errorf("statedir: save CA: write %s: %w", tmpName, err)
	}
	// fsync before rename: the whole point of this file is to survive a crash,
	// and a rename of unflushed data can survive as a zero-length file.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("statedir: save CA: sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("statedir: save CA: close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, CAPath(dir)); err != nil {
		return fmt.Errorf("statedir: save CA: rename into %s: %w", CAPath(dir), err)
	}
	return nil
}

// LoadCA reads the persisted CA out of dir and returns it as the PEM pair
// mitm.Config.SeedCACertPEM / SeedCAKeyPEM expects.
//
// # Fail closed, and validate HERE
//
// Every rejection returns a non-nil error naming the cause, and the caller's
// contract is to mint a FRESH CA and report the loss loudly — never to
// continue with a half-trusted perimeter.
//
// Validation deliberately duplicates what mitm.New does with a seed
// (tls.X509KeyPair, then x509.ParseCertificate of the leaf). That is not
// redundancy for its own sake: it guarantees that anything this function
// returns is a seed mitm.New will ACCEPT, so a damaged file can never turn
// into a supervisor start-up failure. A crash-recovery path that dies because
// its CA file was truncated would convert a recoverable sandbox into an
// unrecoverable one — the exact outcome D-HSH-18 refused encryption to avoid.
//
// Expiry is checked for the same reason: seeding an expired CA would produce a
// perimeter that signs leaves nothing can validate, which looks like a network
// fault rather than an expired anchor.
func LoadCA(dir string) (certPEM, keyPEM []byte, err error) {
	path := CAPath(dir)
	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, nil, fmt.Errorf("%w at %s", ErrCAAbsent, path)
	case err != nil:
		return nil, nil, fmt.Errorf("statedir: load CA %s: %w", path, err)
	}

	var certBlocks, keyBlocks [][]byte
	rest := raw
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			break
		}
		switch blk.Type {
		case "CERTIFICATE":
			certBlocks = append(certBlocks, pem.EncodeToMemory(blk))
		case "EC PRIVATE KEY", "PRIVATE KEY", "RSA PRIVATE KEY":
			keyBlocks = append(keyBlocks, pem.EncodeToMemory(blk))
		}
	}
	if len(certBlocks) != 1 || len(keyBlocks) != 1 {
		return nil, nil, fmt.Errorf("statedir: load CA %s: corrupt: want exactly 1 CERTIFICATE and 1 private-key PEM block, got %d and %d",
			path, len(certBlocks), len(keyBlocks))
	}
	certPEM, keyPEM = certBlocks[0], keyBlocks[0]

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("statedir: load CA %s: corrupt: cert and key do not form a usable pair: %w", path, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, fmt.Errorf("statedir: load CA %s: corrupt: parse certificate: %w", path, err)
	}
	if !leaf.IsCA {
		return nil, nil, fmt.Errorf("statedir: load CA %s: corrupt: certificate is not a CA", path)
	}
	now := time.Now()
	if now.After(leaf.NotAfter) {
		return nil, nil, fmt.Errorf("statedir: load CA %s: expired at %s (now %s)", path, leaf.NotAfter.UTC(), now.UTC())
	}
	if now.Before(leaf.NotBefore) {
		return nil, nil, fmt.Errorf("statedir: load CA %s: not valid until %s (now %s)", path, leaf.NotBefore.UTC(), now.UTC())
	}
	return certPEM, keyPEM, nil
}
