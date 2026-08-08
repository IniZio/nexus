package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// TestCrockfordRoundTrip verifies that decode(encode(x)) == x for 1000 random UUIDs.
func TestCrockfordRoundTrip(t *testing.T) {
	for i := range 1000 {
		id := NewSandboxID()
		encoded := crockfordEncode([16]byte(id))
		decoded, err := crockfordDecode(encoded)
		if err != nil {
			t.Fatalf("iter %d: decode error: %v", i, err)
		}
		if decoded != [16]byte(id) {
			t.Fatalf("iter %d: round-trip mismatch\n  encoded: %s\n  decoded: %v\n  want:    %v",
				i, encoded, decoded, [16]byte(id))
		}
	}
}

// TestSandboxIDRoundTrip verifies ParseSandboxID(id.String()) == id for 1000 IDs.
func TestSandboxIDRoundTrip(t *testing.T) {
	for i := range 1000 {
		id := NewSandboxID()
		s := id.String()
		parsed, err := ParseSandboxID(s)
		if err != nil {
			t.Fatalf("iter %d: ParseSandboxID(%q): %v", i, s, err)
		}
		if parsed != id {
			t.Fatalf("iter %d: round-trip mismatch: got %s, want %s", i, parsed.String(), s)
		}
	}
}

// TestCrockfordConfusables verifies case-insensitivity and the I/L→1, O→0 substitutions.
func TestCrockfordConfusables(t *testing.T) {
	cases := []struct {
		name string
		// confusable is a 26-char string containing a confusable character;
		// canonical is what it should decode to (using actual alphabet chars).
		confusable string
		canonical  string
	}{
		{
			name:       "I->1",
			confusable: "IIIIIIIIIIIIIIIIIIIIIIIIII",
			canonical:  "11111111111111111111111111",
		},
		{
			name:       "i->1",
			confusable: "iiiiiiiiiiiiiiiiiiiiiiiiii",
			canonical:  "11111111111111111111111111",
		},
		{
			name:       "L->1",
			confusable: "LLLLLLLLLLLLLLLLLLLLLLLLLL",
			canonical:  "11111111111111111111111111",
		},
		{
			name:       "l->1",
			confusable: "llllllllllllllllllllllllll",
			canonical:  "11111111111111111111111111",
		},
		{
			name:       "O->0",
			confusable: "OOOOOOOOOOOOOOOOOOOOOOOOOO",
			canonical:  "00000000000000000000000000",
		},
		{
			name:       "o->0",
			confusable: "oooooooooooooooooooooooooo",
			canonical:  "00000000000000000000000000",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := crockfordDecode(tc.confusable)
			if err != nil {
				t.Fatalf("decode %q: %v", tc.confusable, err)
			}
			want, err := crockfordDecode(tc.canonical)
			if err != nil {
				t.Fatalf("decode canonical %q: %v", tc.canonical, err)
			}
			if got != want {
				t.Errorf("confusable %q decoded to %v, want %v", tc.confusable, got, want)
			}
		})
	}
	// Mixed-case round-trip: encode then decode the lowercase version.
	id := NewSandboxID()
	encoded := crockfordEncode([16]byte(id))
	lower := strings.ToLower(encoded)
	decoded, err := crockfordDecode(lower)
	if err != nil {
		t.Fatalf("decode lowercase: %v", err)
	}
	if decoded != [16]byte(id) {
		t.Errorf("lowercase round-trip failed")
	}
}

// TestUUIDv7Bits verifies that the version and variant bit fields are set correctly.
func TestUUIDv7Bits(t *testing.T) {
	for i := range 100 {
		id := NewSandboxID()
		// Version: top nibble of byte 6 must be 0x7 (0b0111).
		if id[6]&0xF0 != 0x70 {
			t.Errorf("iter %d: version bits wrong: byte[6]=0x%02X (want 0x7x)", i, id[6])
		}
		// Variant: top 2 bits of byte 8 must be 0b10.
		if id[8]&0xC0 != 0x80 {
			t.Errorf("iter %d: variant bits wrong: byte[8]=0x%02X (want 0x8x–0xBx)", i, id[8])
		}
	}
}

// TestUUIDv7TimestampOrder verifies that IDs generated at different milliseconds
// sort lexicographically in time order. This is the primary purpose of UUIDv7.
func TestUUIDv7TimestampOrder(t *testing.T) {
	// Generate three IDs ensuring at least 1ms between each.
	id1 := NewSandboxID()
	time.Sleep(2 * time.Millisecond)
	id2 := NewSandboxID()
	time.Sleep(2 * time.Millisecond)
	id3 := NewSandboxID()

	s1, s2, s3 := id1.String(), id2.String(), id3.String()
	if s1 >= s2 {
		t.Errorf("id1 (%s) should sort before id2 (%s)", s1, s2)
	}
	if s2 >= s3 {
		t.Errorf("id2 (%s) should sort before id3 (%s)", s2, s3)
	}
}

// TestResolvePrefixExact covers the exact-match and no-match cases.
func TestResolvePrefixExact(t *testing.T) {
	id := NewSandboxID()
	candidates := []SandboxID{id}

	// Exact match with sb- prefix.
	got, err := ResolvePrefix(id.String(), candidates)
	if err != nil {
		t.Fatalf("exact match: %v", err)
	}
	if got != id {
		t.Errorf("exact match: got %s, want %s", got, id)
	}

	// Exact match without sb- prefix.
	got, err = ResolvePrefix(id.String()[3:], candidates)
	if err != nil {
		t.Fatalf("exact match (no prefix): %v", err)
	}
	if got != id {
		t.Errorf("exact match (no prefix): got %s, want %s", got, id)
	}

	// Short prefix match.
	got, err = ResolvePrefix("sb-"+id.String()[3:8], candidates)
	if err != nil {
		t.Fatalf("prefix match: %v", err)
	}
	if got != id {
		t.Errorf("prefix match: got %s, want %s", got, id)
	}

	// No match.
	_, err = ResolvePrefix("sb-ZZZZZZZZZZZZZZZZZZZZZZZZZZ", candidates)
	var noMatch *ErrNoMatch
	if !errors.As(err, &noMatch) {
		t.Errorf("expected ErrNoMatch, got %v", err)
	}
}

// TestResolvePrefixAmbiguous covers the case where multiple IDs share a prefix.
// UUIDv7 IDs created in the same millisecond share their first ~10 encoded
// characters (48-bit timestamp occupies the first 9.6 chars).
func TestResolvePrefixAmbiguous(t *testing.T) {
	// Manually construct two IDs that differ only in random bits but share the
	// same timestamp — guaranteed prefix collision.
	var a, b [16]byte
	// Use the current timestamp for both.
	ms := uint64(time.Now().UnixMilli())
	// Write timestamp into bytes[0:6].
	for i := 5; i >= 0; i-- {
		a[i] = byte(ms)
		b[i] = byte(ms)
		ms >>= 8
	}
	// Version + same rand_a nibble so they share even more prefix.
	a[6] = 0x70
	b[6] = 0x70
	a[7] = 0xAB
	b[7] = 0xAB
	// Variant same.
	a[8] = 0x80
	b[8] = 0x80
	// Differ only in bytes[9:].
	a[9] = 0x01
	b[9] = 0x02

	idA := SandboxID(a)
	idB := SandboxID(b)
	candidates := []SandboxID{idA, idB}

	// Find a common prefix (first 5 chars of encoded, after "sb-").
	prefixLen := 5
	prefix := idA.String()[:3+prefixLen] // "sb-" + first 5 encoded chars

	_, err := ResolvePrefix(prefix, candidates)
	var ambig *ErrAmbiguous
	if !errors.As(err, &ambig) {
		t.Fatalf("expected ErrAmbiguous for prefix %q, got %v", prefix, err)
	}
	if len(ambig.Candidates) != 2 {
		t.Errorf("ErrAmbiguous.Candidates: got %d, want 2", len(ambig.Candidates))
	}
}

// TestParseSandboxIDInvalid verifies that malformed inputs are rejected.
func TestParseSandboxIDInvalid(t *testing.T) {
	cases := []string{
		"",
		"sb-",
		"not-an-id",
		"sb-TOOSHORT",
		"sb-TOOLONGXXXXXXXXXXXXXXXXXXXXXXX",
		"sb-INVALID!CHARS!!!!!!!!!!!!!!!!", // contains non-alphabet chars
	}
	for _, s := range cases {
		_, err := ParseSandboxID(s)
		if err == nil {
			t.Errorf("ParseSandboxID(%q): expected error, got nil", s)
		}
	}
}
