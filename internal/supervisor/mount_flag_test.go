package supervisor

import (
	"testing"

	"github.com/IniZio/nexus/internal/core/domain"
)

func TestEncodeParseLiveMountRoundTrip(t *testing.T) {
	cases := []domain.LiveMount{
		{HostPath: "/home/user/dir", GuestPath: "/workspace", ReadOnly: false, IsFile: false},
		{HostPath: "/home/user/dir", GuestPath: "/workspace", ReadOnly: true, IsFile: false},
		{HostPath: "/home/user/.tmux.conf", GuestPath: "/root/.tmux.conf", ReadOnly: true, IsFile: true},
		{HostPath: "/home/user/.tmux.conf", GuestPath: "/root/.tmux.conf", ReadOnly: false, IsFile: true},
	}
	for _, want := range cases {
		encoded := EncodeLiveMount(want)
		got, err := ParseLiveMountSpec(encoded)
		if err != nil {
			t.Errorf("ParseLiveMountSpec(%q): %v", encoded, err)
			continue
		}
		if got.HostPath != want.HostPath || got.GuestPath != want.GuestPath ||
			got.ReadOnly != want.ReadOnly || got.IsFile != want.IsFile {
			t.Errorf("round-trip mismatch:\n  input:  %+v\n  encode: %q\n  parsed: %+v", want, encoded, got)
		}
	}
}

func TestEncodeLiveMount_FileFlag(t *testing.T) {
	lm := domain.LiveMount{HostPath: "/home/user/.tmux.conf", GuestPath: "/root/.tmux.conf", ReadOnly: true, IsFile: true}
	enc := EncodeLiveMount(lm)
	if enc != "/home/user/.tmux.conf:/root/.tmux.conf:ro:file" {
		t.Errorf("EncodeLiveMount file ro = %q, want /home/user/.tmux.conf:/root/.tmux.conf:ro:file", enc)
	}

	lm2 := domain.LiveMount{HostPath: "/home/user/.tmux.conf", GuestPath: "/root/.tmux.conf", ReadOnly: false, IsFile: true}
	enc2 := EncodeLiveMount(lm2)
	if enc2 != "/home/user/.tmux.conf:/root/.tmux.conf:rw:file" {
		t.Errorf("EncodeLiveMount file rw = %q, want /home/user/.tmux.conf:/root/.tmux.conf:rw:file", enc2)
	}
}

func TestParseLiveMountSpec_UnknownOption(t *testing.T) {
	_, err := ParseLiveMountSpec("/a:/b:weird")
	if err == nil {
		t.Error("expected error for unknown option, got nil")
	}
}
