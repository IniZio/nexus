package repro

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// SnapshotLogs copies all files from logsDir into logsDir/<timestamp>/
// where timestamp is formatted as "20060102-150405" (Go time format).
// If logsDir is empty or does not exist, SnapshotLogs returns nil (nothing to snapshot).
// Returns the path of the created snapshot directory, or "" if nothing was copied.
// Returns an error if copy fails after files exist.
func SnapshotLogs(logsDir string) (snapshotDir string, err error) {
	// Check if logsDir exists
	info, err := os.Stat(logsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	// Ensure it's a directory
	if !info.IsDir() {
		return "", fmt.Errorf("logsDir is not a directory: %s", logsDir)
	}

	// Read entries in logsDir (non-recursive)
	entries, err := os.ReadDir(logsDir)
	if err != nil {
		return "", err
	}

	// Filter for regular files only (skip subdirectories)
	var files []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry)
		}
	}

	// If no files, return empty
	if len(files) == 0 {
		return "", nil
	}

	// Create snapshot directory with timestamp
	timestamp := time.Now().Format("20060102-150405")
	snapshotPath := filepath.Join(logsDir, timestamp)

	err = os.MkdirAll(snapshotPath, 0755)
	if err != nil {
		return "", err
	}

	// Copy each file
	for _, file := range files {
		srcPath := filepath.Join(logsDir, file.Name())
		dstPath := filepath.Join(snapshotPath, file.Name())

		// Open source file
		src, err := os.Open(srcPath)
		if err != nil {
			return snapshotPath, err
		}

		// Create destination file
		dst, err := os.Create(dstPath)
		if err != nil {
			src.Close()
			return snapshotPath, err
		}

		// Copy contents
		_, err = io.Copy(dst, src)
		dst.Close()
		src.Close()
		if err != nil {
			return snapshotPath, err
		}
	}

	return snapshotPath, nil
}
