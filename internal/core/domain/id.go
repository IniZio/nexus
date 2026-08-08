package domain

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// SandboxID is a UUIDv7 encoded as a Crockford base32 string with an "sb-"
// prefix. It is a distinct named type so it cannot be confused with other
// identifier types.
type SandboxID [16]byte

// crockfordAlphabet is the 32-character Crockford base32 alphabet.
// Letters I, L, O, U are intentionally excluded to avoid visual ambiguity.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// crockfordDecodeTable maps ASCII bytes to their 5-bit Crockford values.
// -1 means invalid. Initialised once by init() below.
var crockfordDecodeTable [256]int8

func init() {
	for i := range crockfordDecodeTable {
		crockfordDecodeTable[i] = -1
	}
	for i, c := range crockfordAlphabet {
		crockfordDecodeTable[byte(c)] = int8(i)
		if c >= 'A' {
			crockfordDecodeTable[byte(c)+32] = int8(i) // lowercase variant
		}
	}
	// Crockford confusable substitutions (applied after case normalisation).
	// I and L are visually similar to 1; O is visually similar to 0.
	crockfordDecodeTable['I'] = 1
	crockfordDecodeTable['i'] = 1
	crockfordDecodeTable['L'] = 1
	crockfordDecodeTable['l'] = 1
	crockfordDecodeTable['O'] = 0
	crockfordDecodeTable['o'] = 0
}

// crockfordEncode encodes a 128-bit UUID as a 26-character Crockford base32
// string.
//
// Padding note: 128 bits is not a multiple of 5. We need ⌈128/5⌉ = 26
// characters (26×5 = 130 bits). The two least-significant padding bits in the
// final character are always zero during encoding and are discarded during
// decoding, so decode(encode(x)) == x for all inputs.
func crockfordEncode(b [16]byte) string {
	buf := make([]byte, 26)
	for i := range 26 {
		bitStart := uint(i) * 5
		byteIdx := bitStart / 8
		bitOff := bitStart % 8

		// Assemble a 16-bit window starting at byteIdx so we can extract the
		// 5-bit group that spans a byte boundary.
		var w uint16
		if byteIdx < 16 {
			w = uint16(b[byteIdx]) << 8
		}
		if byteIdx+1 < 16 {
			w |= uint16(b[byteIdx+1])
		}
		// Shift so our 5-bit group lands in the least-significant position.
		shift := 16 - 5 - bitOff
		buf[i] = crockfordAlphabet[(w>>shift)&0x1F]
	}
	return string(buf)
}

// crockfordDecode decodes a 26-character Crockford base32 string into a
// 128-bit UUID. It is the exact inverse of crockfordEncode.
func crockfordDecode(s string) ([16]byte, error) {
	if len(s) != 26 {
		return [16]byte{}, fmt.Errorf("crockford decode: expected 26 chars, got %d", len(s))
	}
	var b [16]byte
	for i := range 26 {
		val := crockfordDecodeTable[s[i]]
		if val < 0 {
			return [16]byte{}, fmt.Errorf("crockford decode: invalid character %q at position %d", s[i], i)
		}

		bitStart := uint(i) * 5
		byteIdx := bitStart / 8
		bitOff := bitStart % 8

		// Place the 5-bit value into the 16-bit window and distribute into b.
		// shift is the same as in encode: 16 - 5 - bitOff.
		placed := uint16(val) << (16 - 5 - bitOff)
		if byteIdx < 16 {
			b[byteIdx] |= byte(placed >> 8)
		}
		if byteIdx+1 < 16 {
			b[byteIdx+1] |= byte(placed & 0xFF)
		}
	}
	return b, nil
}

// newUUIDv7 constructs a UUIDv7 per RFC 9562 §5.7.
//
// Layout (128 bits, big-endian):
//
//	bits  0–47  : 48-bit Unix timestamp in milliseconds
//	bits 48–51  : version = 0b0111 (7)
//	bits 52–63  : 12 random bits (rand_a)
//	bits 64–65  : variant = 0b10
//	bits 66–127 : 62 random bits (rand_b)
func newUUIDv7() [16]byte {
	var uuid [16]byte

	// 48-bit millisecond timestamp, big-endian into bytes[0:6].
	ms := uint64(time.Now().UnixMilli())
	binary.BigEndian.PutUint64(uuid[0:8], ms<<16) // occupies bits 0-47 of the 64-bit word
	// After PutUint64 the top 6 bytes (48 bits) carry the timestamp; bytes[6]
	// and [7] currently hold the 16 low bits of (ms<<16), which are zero — we
	// overwrite them below with version + rand_a.

	// 10 random bytes supply rand_a (12 bits) and rand_b (62 bits + 2 spare).
	var rnd [10]byte
	if _, err := io.ReadFull(rand.Reader, rnd[:]); err != nil {
		panic("crypto/rand: " + err.Error())
	}

	// bytes[6]: version 0111 in top nibble; top 4 random bits in bottom nibble.
	uuid[6] = 0x70 | (rnd[0] & 0x0F)
	// bytes[7]: lower 8 bits of rand_a.
	uuid[7] = rnd[1]
	// bytes[8]: variant 10 in top 2 bits; top 6 bits of rand_b.
	uuid[8] = 0x80 | (rnd[2] & 0x3F)
	// bytes[9–15]: remaining 56 bits of rand_b.
	copy(uuid[9:], rnd[3:10])

	return uuid
}

// NewSandboxID generates a new, globally unique SandboxID backed by UUIDv7.
func NewSandboxID() SandboxID {
	return newUUIDv7()
}

// String returns the canonical string representation: "sb-" followed by 26
// uppercase Crockford base32 characters.
func (id SandboxID) String() string {
	return "sb-" + crockfordEncode([16]byte(id))
}

// ParseSandboxID parses a SandboxID from its canonical string form.
// The "sb-" prefix is required.
func ParseSandboxID(s string) (SandboxID, error) {
	if !strings.HasPrefix(s, "sb-") {
		return SandboxID{}, fmt.Errorf("sandbox ID must start with \"sb-\": %q", s)
	}
	encoded := s[3:]
	b, err := crockfordDecode(encoded)
	if err != nil {
		return SandboxID{}, fmt.Errorf("parse sandbox ID %q: %w", s, err)
	}
	return SandboxID(b), nil
}

// MarshalJSON encodes the SandboxID as its canonical string.
func (id SandboxID) MarshalJSON() ([]byte, error) {
	return json.Marshal(id.String())
}

// UnmarshalJSON decodes a SandboxID from its canonical string.
func (id *SandboxID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	parsed, err := ParseSandboxID(s)
	if err != nil {
		return fmt.Errorf("unmarshal SandboxID: %w", err)
	}
	*id = parsed
	return nil
}

// ErrNoMatch is returned by ResolvePrefix when no candidate matches the prefix.
type ErrNoMatch struct {
	Prefix string
}

func (e *ErrNoMatch) Error() string {
	return fmt.Sprintf("no sandbox matching prefix %q", e.Prefix)
}

// ErrAmbiguous is returned by ResolvePrefix when multiple candidates match.
type ErrAmbiguous struct {
	Prefix     string
	Candidates []SandboxID
}

func (e *ErrAmbiguous) Error() string {
	strs := make([]string, len(e.Candidates))
	for i, id := range e.Candidates {
		strs[i] = id.String()
	}
	return fmt.Sprintf("prefix %q is ambiguous: matches %s", e.Prefix, strings.Join(strs, ", "))
}

// ResolvePrefix finds the unique SandboxID whose encoded form starts with
// prefix. The "sb-" prefix is optional in the user-supplied input. Returns
// ErrNoMatch if nothing matches and ErrAmbiguous (listing candidates) if
// more than one ID matches.
//
// Because SandboxIDs are UUIDv7 (time-ordered), sandboxes created in the same
// millisecond share long common prefixes in their encoded form. Callers should
// always handle ErrAmbiguous.
func ResolvePrefix(prefix string, candidates []SandboxID) (SandboxID, error) {
	// Strip the optional sb- prefix from user input, then uppercase for
	// case-insensitive comparison against the stored uppercase encoding.
	search := strings.ToUpper(strings.TrimPrefix(prefix, "sb-"))

	var matches []SandboxID
	for _, id := range candidates {
		encoded := id.String()[3:] // strip "sb-"
		if strings.HasPrefix(encoded, search) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return SandboxID{}, &ErrNoMatch{Prefix: prefix}
	case 1:
		return matches[0], nil
	default:
		return SandboxID{}, &ErrAmbiguous{Prefix: prefix, Candidates: matches}
	}
}
