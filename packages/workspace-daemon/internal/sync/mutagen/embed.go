package mutagen

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

//go:embed bin/mutagen-darwin-amd64
var mutagenDarwinAMD64 []byte

//go:embed bin/mutagen-darwin-arm64
var mutagenDarwinARM64 []byte

//go:embed bin/mutagen-linux-amd64
var mutagenLinuxAMD64 []byte

//go:embed bin/mutagen-linux-arm64
var mutagenLinuxARM64 []byte

func getEmbeddedBinary() []byte {
	switch runtime.GOOS + "-" + runtime.GOARCH {
	case "darwin-amd64":
		return mutagenDarwinAMD64
	case "darwin-arm64":
		return mutagenDarwinARM64
	case "linux-amd64":
		return mutagenLinuxAMD64
	case "linux-arm64":
		return mutagenLinuxARM64
	default:
		return nil
	}
}

func ExtractMutagen(dataDir string) (string, error) {
	binData := getEmbeddedBinary()
	if binData == nil {
		return "", fmt.Errorf("unsupported platform: %s-%s", runtime.GOOS, runtime.GOARCH)
	}

	binPath := filepath.Join(dataDir, "bin", "mutagen")

	if info, err := os.Stat(binPath); err == nil {
		if info.Size() == int64(len(binData)) {
			return binPath, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(binPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create bin directory: %w", err)
	}

	if err := os.WriteFile(binPath, binData, 0755); err != nil {
		return "", fmt.Errorf("failed to write mutagen binary: %w", err)
	}

	return binPath, nil
}
