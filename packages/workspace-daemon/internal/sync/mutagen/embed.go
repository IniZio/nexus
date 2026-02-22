package mutagen

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

//go:embed bin/mutagen-darwin_amd64
var mutagenDarwinAMD64 []byte

//go:embed bin/mutagen-darwin_arm64
var mutagenDarwinARM64 []byte

//go:embed bin/mutagen-linux_amd64
var mutagenLinuxAMD64 []byte

//go:embed bin/mutagen-linux_arm64
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
	if binData == nil || len(binData) == 0 {
		return findSystemMutagen()
	}

	binPath := filepath.Join(dataDir, "bin", "mutagen")

	if info, err := os.Stat(binPath); err == nil {
		if info.Size() == int64(len(binData)) && info.Size() > 0 {
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

func findSystemMutagen() (string, error) {
	path, err := exec.LookPath("mutagen")
	if err != nil {
		return "", fmt.Errorf("mutagen not found: embedded binary empty and system mutagen not available")
	}
	return path, nil
}
