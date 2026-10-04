package cloudhypervisor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReapHibernateTmp_KeepsCommittedSnap(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{hibernateSnapDir, hibernateTmpPrefix + "x", hibernateOldPrefix + "y"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	reapHibernateTmp(dir)
	if _, err := os.Stat(filepath.Join(dir, hibernateSnapDir)); err != nil {
		t.Fatalf("committed snap removed: %v", err)
	}
	for _, d := range []string{hibernateTmpPrefix + "x", hibernateOldPrefix + "y"} {
		if _, err := os.Stat(filepath.Join(dir, d)); !os.IsNotExist(err) {
			t.Fatalf("%s not reaped: %v", d, err)
		}
	}
}
