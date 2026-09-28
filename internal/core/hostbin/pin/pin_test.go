package pin_test

import (
	"errors"
	"testing"

	"github.com/IniZio/nexus/internal/core/hostbin/pin"
)

func TestExpand(t *testing.T) {
	tests := []struct {
		name    string
		s       string
		version string
		goarch  string
		want    string
	}{
		{
			name:    "VERSION placeholder",
			s:       "v{VERSION}",
			version: "1.2.3",
			goarch:  "amd64",
			want:    "v1.2.3",
		},
		{
			name:    "GOARCH placeholder",
			s:       "bin-{GOARCH}",
			version: "1.0",
			goarch:  "amd64",
			want:    "bin-amd64",
		},
		{
			name:    "ARCH amd64 maps to x86_64",
			s:       "{ARCH}",
			version: "1.0",
			goarch:  "amd64",
			want:    "x86_64",
		},
		{
			name:    "ARCH arm64 maps to aarch64",
			s:       "{ARCH}",
			version: "1.0",
			goarch:  "arm64",
			want:    "aarch64",
		},
		{
			name:    "ARCH unknown passes through goarch",
			s:       "{ARCH}",
			version: "1.0",
			goarch:  "riscv64",
			want:    "riscv64",
		},
		{
			name:    "all placeholders",
			s:       "https://example.com/{VERSION}/{GOARCH}/{ARCH}/bin",
			version: "2.0",
			goarch:  "arm64",
			want:    "https://example.com/2.0/arm64/aarch64/bin",
		},
		{
			name:    "no placeholders",
			s:       "https://example.com/static/bin",
			version: "1.0",
			goarch:  "amd64",
			want:    "https://example.com/static/bin",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := pin.Expand(tc.s, tc.version, tc.goarch)
			if got != tc.want {
				t.Errorf("Expand(%q, %q, %q) = %q; want %q", tc.s, tc.version, tc.goarch, got, tc.want)
			}
		})
	}
}

func TestPinURL(t *testing.T) {
	p := pin.Pin{
		Name:        "tool",
		Version:     "1.0",
		URLTemplate: "https://example.com/{VERSION}/{GOARCH}/tool",
		URLByGoArch: map[string]string{
			"amd64": "https://override.com/{VERSION}/tool-amd64",
		},
	}

	t.Run("per-arch override wins", func(t *testing.T) {
		got := p.URL("amd64")
		want := "https://override.com/1.0/tool-amd64"
		if got != want {
			t.Errorf("got %q; want %q", got, want)
		}
	})

	t.Run("template used when no override", func(t *testing.T) {
		got := p.URL("arm64")
		want := "https://example.com/1.0/arm64/tool"
		if got != want {
			t.Errorf("got %q; want %q", got, want)
		}
	})

	t.Run("empty when neither set", func(t *testing.T) {
		empty := pin.Pin{Name: "x", Version: "1"}
		if got := empty.URL("amd64"); got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})
}

func TestPinFormat(t *testing.T) {
	base := pin.Pin{
		Name:        "tool",
		Version:     "1.0",
		URLTemplate: "https://example.com/tool.tar.gz",
	}

	t.Run("raw when no ArchiveMember", func(t *testing.T) {
		if f := base.Format("amd64"); f != pin.FormatRaw {
			t.Errorf("expected FormatRaw, got %v", f)
		}
	})

	t.Run("tar.gz suffix", func(t *testing.T) {
		p := base
		p.ArchiveMember = "tool"
		if f := p.Format("amd64"); f != pin.FormatTarGz {
			t.Errorf("expected FormatTarGz, got %v", f)
		}
	})

	t.Run("tgz suffix", func(t *testing.T) {
		p := pin.Pin{
			Name:          "tool",
			Version:       "1.0",
			URLTemplate:   "https://example.com/tool.tgz",
			ArchiveMember: "tool",
		}
		if f := p.Format("amd64"); f != pin.FormatTarGz {
			t.Errorf("expected FormatTarGz, got %v", f)
		}
	})

	t.Run("zip suffix", func(t *testing.T) {
		p := pin.Pin{
			Name:          "tool",
			Version:       "1.0",
			URLTemplate:   "https://example.com/tool.zip",
			ArchiveMember: "tool",
		}
		if f := p.Format("amd64"); f != pin.FormatZip {
			t.Errorf("expected FormatZip, got %v", f)
		}
	})

	t.Run("unknown suffix defaults to TarGz", func(t *testing.T) {
		p := pin.Pin{
			Name:          "tool",
			Version:       "1.0",
			URLTemplate:   "https://example.com/tool.bin",
			ArchiveMember: "tool",
		}
		if f := p.Format("amd64"); f != pin.FormatTarGz {
			t.Errorf("expected FormatTarGz, got %v", f)
		}
	})
}

func TestPinSourceSHA256(t *testing.T) {
	p := pin.Pin{
		Name:    "tool",
		Version: "1.0",
		SHA256ByGoArch: map[string]string{
			"amd64": "abcdef1234567890",
		},
	}

	t.Run("returns sha for known arch", func(t *testing.T) {
		got, err := p.SourceSHA256("amd64")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "abcdef1234567890" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("ErrUnsupportedArch for missing arch", func(t *testing.T) {
		_, err := p.SourceSHA256("arm64")
		if !errors.Is(err, pin.ErrUnsupportedArch) {
			t.Errorf("expected ErrUnsupportedArch, got %v", err)
		}
	})
}

func TestPinBinarySHA256(t *testing.T) {
	t.Run("raw pin delegates to SourceSHA256", func(t *testing.T) {
		p := pin.Pin{
			Name:    "tool",
			Version: "1.0",
			SHA256ByGoArch: map[string]string{
				"amd64": "sourcehash",
			},
		}
		got, err := p.BinarySHA256("amd64")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "sourcehash" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("raw pin missing arch returns ErrUnsupportedArch", func(t *testing.T) {
		p := pin.Pin{Name: "tool", Version: "1.0"}
		_, err := p.BinarySHA256("amd64")
		if !errors.Is(err, pin.ErrUnsupportedArch) {
			t.Errorf("expected ErrUnsupportedArch, got %v", err)
		}
	})

	t.Run("archive pin uses BinarySHA256ByGoArch", func(t *testing.T) {
		p := pin.Pin{
			Name:          "tool",
			Version:       "1.0",
			ArchiveMember: "bin/tool",
			SHA256ByGoArch: map[string]string{
				"amd64": "sourcehash",
			},
			BinarySHA256ByGoArch: map[string]string{
				"amd64": "binhash",
			},
		}
		got, err := p.BinarySHA256("amd64")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "binhash" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("archive pin missing binary hash returns ErrUnsupportedArch", func(t *testing.T) {
		p := pin.Pin{
			Name:          "tool",
			Version:       "1.0",
			ArchiveMember: "bin/tool",
			SHA256ByGoArch: map[string]string{
				"amd64": "sourcehash",
			},
		}
		_, err := p.BinarySHA256("amd64")
		if !errors.Is(err, pin.ErrUnsupportedArch) {
			t.Errorf("expected ErrUnsupportedArch, got %v", err)
		}
	})
}

func TestPinDownloadable(t *testing.T) {
	full := pin.Pin{
		Name:        "tool",
		Version:     "1.0",
		URLTemplate: "https://example.com/tool",
		SHA256ByGoArch: map[string]string{
			"amd64": "sourcehash",
		},
		BinarySHA256ByGoArch: map[string]string{},
	}

	t.Run("downloadable when all present (raw)", func(t *testing.T) {
		if !full.Downloadable("amd64") {
			t.Error("expected Downloadable=true")
		}
	})

	t.Run("not downloadable when URL empty", func(t *testing.T) {
		p := full
		p.URLTemplate = ""
		p.URLByGoArch = nil
		if p.Downloadable("amd64") {
			t.Error("expected Downloadable=false")
		}
	})

	t.Run("not downloadable when source sha missing", func(t *testing.T) {
		p := full
		p.SHA256ByGoArch = nil
		if p.Downloadable("amd64") {
			t.Error("expected Downloadable=false")
		}
	})

	t.Run("not downloadable when archive binary sha missing", func(t *testing.T) {
		p := pin.Pin{
			Name:          "tool",
			Version:       "1.0",
			URLTemplate:   "https://example.com/tool.tar.gz",
			ArchiveMember: "bin/tool",
			SHA256ByGoArch: map[string]string{
				"amd64": "sourcehash",
			},
		}
		if p.Downloadable("amd64") {
			t.Error("expected Downloadable=false")
		}
	})
}
