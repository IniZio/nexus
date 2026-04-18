package firecracker

import (
	"crypto/sha256"
	"encoding/hex"
)

func tapNameForWorkspace(workspaceID string) string {
	sum := sha256.Sum256([]byte(workspaceID))
	return "nx-" + hex.EncodeToString(sum[:])[:12]
}
