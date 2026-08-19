package domain

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Digest is the content-addressed identity of an Image. It is always in the
// canonical form "<algo>:<hex>", e.g. "sha256:<64 lowercase hex characters>".
//
// The zero value ("") is invalid. Use ParseDigest to construct a validated Digest.
// Two Images with the same Digest are identical by content (content-addressing);
// this is deliberately opposite to SandboxID, which is randomly unique.
type Digest string

const (
	// digestSHA256Prefix is the canonical algorithm prefix for SHA-256 digests.
	digestSHA256Prefix = "sha256:"
	// sha256HexLen is the number of hex characters in a SHA-256 digest (32 bytes × 2).
	sha256HexLen = 64
)

// ParseDigest validates and returns a Digest from its canonical "<algo>:<hex>" form.
// Only "sha256:<64-hex>" is currently accepted.
func ParseDigest(s string) (Digest, error) {
	if err := validateDigest(s); err != nil {
		return "", err
	}
	return Digest(s), nil
}

// MustDigest returns ParseDigest(s) and panics on error.
// For use in tests and package-level init code only.
func MustDigest(s string) Digest {
	d, err := ParseDigest(s)
	if err != nil {
		panic("domain: MustDigest: " + err.Error())
	}
	return d
}

// String returns the canonical "<algo>:<hex>" form.
func (d Digest) String() string { return string(d) }

// Valid reports whether d is a well-formed, non-empty digest.
func (d Digest) Valid() bool { return validateDigest(string(d)) == nil }

// Algo returns the hash algorithm portion, e.g. "sha256".
func (d Digest) Algo() string {
	if i := strings.IndexByte(string(d), ':'); i >= 0 {
		return string(d)[:i]
	}
	return ""
}

// Hex returns the lowercase hex portion of the digest.
func (d Digest) Hex() string {
	if i := strings.IndexByte(string(d), ':'); i >= 0 {
		return string(d)[i+1:]
	}
	return ""
}

func validateDigest(s string) error {
	if !strings.HasPrefix(s, digestSHA256Prefix) {
		return fmt.Errorf("digest must start with %q: %q", digestSHA256Prefix, s)
	}
	hexPart := s[len(digestSHA256Prefix):]
	if len(hexPart) != sha256HexLen {
		return fmt.Errorf("digest: sha256 hex must be %d characters, got %d: %q",
			sha256HexLen, len(hexPart), s)
	}
	// hex.DecodeString validates that every character is [0-9a-fA-F].
	if _, err := hex.DecodeString(hexPart); err != nil {
		return fmt.Errorf("digest: invalid hex in %q: %w", s, err)
	}
	return nil
}

// MarshalJSON encodes the Digest as its canonical string.
// Returns an error for the zero value or any invalid digest.
func (d Digest) MarshalJSON() ([]byte, error) {
	if !d.Valid() {
		return nil, fmt.Errorf("marshal Digest: invalid digest %q", string(d))
	}
	return json.Marshal(string(d))
}

// UnmarshalJSON decodes a Digest from its canonical string.
func (d *Digest) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := ParseDigest(s)
	if err != nil {
		return fmt.Errorf("unmarshal Digest: %w", err)
	}
	*d = parsed
	return nil
}

// ImageKind distinguishes the two images nexus3 ships (ticket 14):
// a default glibc base and a stock buildkitd builder.
type ImageKind int

const (
	// KindBase is the default glibc base rootfs.
	KindBase ImageKind = iota + 1
	// KindBuilder is the stock buildkitd builder rootfs.
	KindBuilder
)

// String returns the lowercase wire name of the kind.
func (k ImageKind) String() string {
	switch k {
	case KindBase:
		return "base"
	case KindBuilder:
		return "builder"
	default:
		return fmt.Sprintf("ImageKind(%d)", int(k))
	}
}

// Valid reports whether k is one of the two defined image kinds.
// The zero value of ImageKind is not valid.
func (k ImageKind) Valid() bool {
	return k == KindBase || k == KindBuilder
}

// ParseImageKind parses an ImageKind from its lowercase wire name.
func ParseImageKind(s string) (ImageKind, error) {
	switch s {
	case "base":
		return KindBase, nil
	case "builder":
		return KindBuilder, nil
	default:
		return 0, fmt.Errorf("unknown image kind %q", s)
	}
}

// MarshalJSON encodes the kind as its lowercase wire string.
// Returns an error for the zero value or any invalid kind.
func (k ImageKind) MarshalJSON() ([]byte, error) {
	if !k.Valid() {
		return nil, fmt.Errorf("marshal ImageKind: invalid kind %d", int(k))
	}
	return json.Marshal(k.String())
}

// UnmarshalJSON decodes an ImageKind from its lowercase wire string.
func (k *ImageKind) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := ParseImageKind(s)
	if err != nil {
		return fmt.Errorf("unmarshal ImageKind: %w", err)
	}
	*k = parsed
	return nil
}

// Image is a content-addressed rootfs artifact produced by the image pipeline.
// Its primary key is Digest; two Images with the same Digest are identical
// by content and share exactly one cache entry.
//
// Unlike Sandbox (which is randomly identity-addressed so that N sandboxes may
// be created from identical inputs), Image is content-addressed on purpose:
// building the same Containerfile from the same inputs twice must deduplicate
// to one cache entry, not two (ticket 10 ruling, ticket 14 design).
type Image struct {
	// Digest is the SHA-256 content hash of the artifact. It is the primary
	// identity key; all other fields are metadata that annotate the same content.
	Digest Digest

	// Ref is the optional human-readable tag, e.g. "nexus3-base:20260807".
	// It is not part of image identity — equality and digest lookup ignore it
	// entirely — but it IS a lookup key: `--image <ref>` resolves through it.
	//
	// A ref therefore names at most one image. The cache enforces this by
	// transferring a ref to the newest artifact stored under it, so an older
	// entry keeps its content and its digest but loses the name. Callers
	// resolving by ref must still refuse a ref held by more than one entry
	// (a cache written before that rule existed), never pick one.
	Ref string

	// Kind is whether this is a base or builder image (ticket 14).
	Kind ImageKind

	// Size is the artifact size in bytes.
	Size int64

	// CreatedAt is the wall-clock time the artifact was first stored in the cache.
	CreatedAt time.Time
}
