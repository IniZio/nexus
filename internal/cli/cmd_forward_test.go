package cli

import (
	"context"
	"strings"
	"testing"
)

// ── parsePortPair ─────────────────────────────────────────────────────────────

func TestParsePortPair_Valid(t *testing.T) {
	cases := []struct {
		in        string
		wantHost  uint32
		wantGuest uint32
	}{
		{"8080:3000", 8080, 3000},
		{"1:65535", 1, 65535},
		{"65535:1", 65535, 1},
		{"443:443", 443, 443},
	}
	for _, tc := range cases {
		h, g, err := parsePortPair(tc.in)
		if err != nil {
			t.Errorf("parsePortPair(%q): unexpected error: %v", tc.in, err)
			continue
		}
		if h != tc.wantHost || g != tc.wantGuest {
			t.Errorf("parsePortPair(%q) = (%d, %d), want (%d, %d)",
				tc.in, h, g, tc.wantHost, tc.wantGuest)
		}
	}
}

func TestParsePortPair_MissingColon(t *testing.T) {
	_, _, err := parsePortPair("8080")
	if err == nil {
		t.Fatal("expected error for missing colon, got nil")
	}
	if !strings.Contains(err.Error(), "expected <hostPort>:<guestPort>") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestParsePortPair_OutOfRange(t *testing.T) {
	cases := []struct {
		in      string
		wantSub string
	}{
		{"0:3000", "out of range"},
		{"8080:0", "out of range"},
		{"65536:3000", "out of range"},
		{"8080:65536", "out of range"},
	}
	for _, tc := range cases {
		_, _, err := parsePortPair(tc.in)
		if err == nil {
			t.Errorf("parsePortPair(%q): expected error, got nil", tc.in)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantSub) {
			t.Errorf("parsePortPair(%q) error = %q, want substring %q", tc.in, err, tc.wantSub)
		}
	}
}

func TestParsePortPair_NonNumeric(t *testing.T) {
	cases := []string{
		"abc:3000",
		"8080:xyz",
		":3000",
		"8080:",
	}
	for _, in := range cases {
		_, _, err := parsePortPair(in)
		if err == nil {
			t.Errorf("parsePortPair(%q): expected error, got nil", in)
		}
	}
}

// ── runForward argument validation (no VM required) ──────────────────────────

func TestRunForward_MissingArgs(t *testing.T) {
	err := runForward(context.Background(), []string{}, nil)
	var ue *UsageError
	if !asUsageError(err, &ue) {
		t.Fatalf("expected *UsageError, got %T: %v", err, err)
	}
	if !strings.Contains(ue.Msg, "forward:") {
		t.Errorf("usage error message missing command prefix: %q", ue.Msg)
	}
}

func TestRunForward_RefOnly(t *testing.T) {
	err := runForward(context.Background(), []string{"myref"}, nil)
	var ue *UsageError
	if !asUsageError(err, &ue) {
		t.Fatalf("expected *UsageError, got %T: %v", err, err)
	}
}

func TestRunForward_BadPortPair(t *testing.T) {
	err := runForward(context.Background(), []string{"myref", "notaport"}, nil)
	var ue *UsageError
	if !asUsageError(err, &ue) {
		t.Fatalf("expected *UsageError for bad port pair, got %T: %v", err, err)
	}
	if !strings.Contains(ue.Msg, "forward:") {
		t.Errorf("usage error message missing 'forward:' prefix: %q", ue.Msg)
	}
}

// asUsageError attempts a type assertion from err to *UsageError via errors.As.
func asUsageError(err error, target **UsageError) bool {
	if err == nil {
		return false
	}
	ue, ok := err.(*UsageError)
	if ok {
		*target = ue
	}
	return ok
}
