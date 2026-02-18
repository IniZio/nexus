package coordination

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetNextADRNumber(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "adr-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := &ADRManager{
		decisionsDir: tmpDir,
	}

	number, err := manager.GetNextADRNumber()
	if err != nil {
		t.Fatalf("GetNextADRNumber failed: %v", err)
	}
	if number != 1 {
		t.Errorf("expected first ADR to be 1, got %d", number)
	}

	err = createTestADR(tmpDir, "001-test-adr.md", "draft")
	if err != nil {
		t.Fatalf("failed to create test ADR: %v", err)
	}

	number, err = manager.GetNextADRNumber()
	if err != nil {
		t.Fatalf("GetNextADRNumber failed: %v", err)
	}
	if number != 2 {
		t.Errorf("expected next ADR to be 2, got %d", number)
	}

	err = createTestADR(tmpDir, "002-another-adr.md", "accepted")
	if err != nil {
		t.Fatalf("failed to create second test ADR: %v", err)
	}

	number, err = manager.GetNextADRNumber()
	if err != nil {
		t.Fatalf("GetNextADRNumber failed: %v", err)
	}
	if number != 3 {
		t.Errorf("expected next ADR to be 3, got %d", number)
	}
}

func TestCreateADR(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "adr-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := &ADRManager{
		decisionsDir: tmpDir,
	}

	docTask, err := manager.CreateADR("Git Worktree Isolation", "test-author")
	if err != nil {
		t.Fatalf("CreateADR failed: %v", err)
	}

	if docTask.ADRNumber != 1 {
		t.Errorf("expected ADR number 1, got %d", docTask.ADRNumber)
	}

	expectedPath := "001-git-worktree-isolation.md"
	if docTask.DraftPath != expectedPath {
		t.Errorf("expected path %s, got %s", expectedPath, docTask.DraftPath)
	}

	if docTask.DocType != DocTypeADR {
		t.Errorf("expected DocType ADR, got %s", docTask.DocType)
	}

	expectedTitle := "ADR-001: Git Worktree Isolation"
	if docTask.Title != expectedTitle {
		t.Errorf("expected title %s, got %s", expectedTitle, docTask.Title)
	}

	filePath := filepath.Join(tmpDir, docTask.DraftPath)
	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		t.Fatalf("failed to read created ADR file: %v", err)
	}

	contentStr := string(content)

	if !strings.Contains(contentStr, "adr: 001") {
		t.Error("ADR content missing adr number in header")
	}

	if !strings.Contains(contentStr, "author: test-author") {
		t.Error("ADR content missing author")
	}

	if !strings.Contains(contentStr, "status: draft") {
		t.Error("ADR content missing draft status")
	}

	if !strings.Contains(contentStr, "# ADR-001: Git Worktree Isolation") {
		t.Error("ADR content missing title in markdown")
	}

	if !strings.Contains(contentStr, "created:") {
		t.Error("ADR content missing created timestamp")
	}

	if !strings.Contains(contentStr, "modified:") {
		t.Error("ADR content missing modified timestamp")
	}
}

func TestTimestampsAreSetCorrectly(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "adr-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := &ADRManager{
		decisionsDir: tmpDir,
	}

	beforeCreate := time.Now().UTC()

	docTask, err := manager.CreateADR("Test ADR", "author")
	if err != nil {
		t.Fatalf("CreateADR failed: %v", err)
	}

	afterCreate := time.Now().UTC()

	content, err := ioutil.ReadFile(filepath.Join(tmpDir, docTask.DraftPath))
	if err != nil {
		t.Fatalf("failed to read ADR file: %v", err)
	}

	contentStr := string(content)

	if !strings.Contains(contentStr, "created:") {
		t.Error("created timestamp missing")
	}

	if !strings.Contains(contentStr, "modified:") {
		t.Error("modified timestamp missing")
	}

	lines := strings.Split(contentStr, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "created:") || strings.HasPrefix(line, "modified:") {
			ts := strings.TrimSpace(strings.TrimPrefix(line, "created:"))
			ts = strings.TrimSpace(strings.TrimPrefix(ts, "modified:"))
			parsedTime, err := time.Parse(time.RFC3339, ts)
			if err != nil {
				t.Errorf("invalid timestamp format: %s", ts)
				continue
			}
			extendedBefore := beforeCreate.Add(-2 * time.Second)
			extendedAfter := afterCreate.Add(2 * time.Second)
			if parsedTime.Before(extendedBefore) || parsedTime.After(extendedAfter) {
				t.Errorf("timestamp %s out of expected range", parsedTime)
			}
		}
	}
}

func TestUpdateADRStatus(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "adr-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := &ADRManager{
		decisionsDir: tmpDir,
	}

	_, err = manager.CreateADR("Test ADR", "author")
	if err != nil {
		t.Fatalf("CreateADR failed: %v", err)
	}

	err = manager.UpdateADRStatus(1, "accepted")
	if err != nil {
		t.Fatalf("UpdateADRStatus failed: %v", err)
	}

	content, err := ioutil.ReadFile(filepath.Join(tmpDir, "001-test-adr.md"))
	if err != nil {
		t.Fatalf("failed to read ADR file: %v", err)
	}

	contentStr := string(content)

	if !strings.Contains(contentStr, "status: accepted") {
		t.Error("ADR status was not updated to accepted")
	}

	if !strings.Contains(contentStr, "modified:") {
		t.Error("modified timestamp not updated")
	}
}

func TestPublishADRRequiresAcceptedStatus(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "adr-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := &ADRManager{
		decisionsDir: tmpDir,
	}

	_, err = manager.CreateADR("Test ADR", "author")
	if err != nil {
		t.Fatalf("CreateADR failed: %v", err)
	}

	targetDir := filepath.Join(tmpDir, "docs/dev/decisions")
	err = os.MkdirAll(targetDir, 0755)
	if err != nil {
		t.Fatalf("failed to create target dir: %v", err)
	}

	err = manager.PublishADR(1)
	if err == nil {
		t.Error("expected error when publishing non-accepted ADR")
	}

	expectedMsg := "ADR must be accepted before publishing"
	if err != nil && !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error message containing '%s', got: %v", expectedMsg, err)
	}
}

func TestPublishADRSuccess(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "adr-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := &ADRManager{
		decisionsDir: tmpDir,
	}

	_, err = manager.CreateADR("Test ADR", "author")
	if err != nil {
		t.Fatalf("CreateADR failed: %v", err)
	}

	err = manager.UpdateADRStatus(1, "accepted")
	if err != nil {
		t.Fatalf("UpdateADRStatus failed: %v", err)
	}

	targetDir := filepath.Join(tmpDir, "docs/dev/decisions")
	err = os.MkdirAll(targetDir, 0755)
	if err != nil {
		t.Fatalf("failed to create target dir: %v", err)
	}

	err = manager.PublishADR(1)
	if err != nil {
		t.Fatalf("PublishADR failed: %v", err)
	}

	sourceContent, err := ioutil.ReadFile(filepath.Join(tmpDir, "001-test-adr.md"))
	if err != nil {
		t.Fatalf("failed to read source ADR: %v", err)
	}

	targetContent, err := ioutil.ReadFile(filepath.Join(targetDir, "001-test-adr.md"))
	if err != nil {
		t.Fatalf("failed to read published ADR: %v", err)
	}

	if string(sourceContent) != string(targetContent) {
		t.Error("published ADR content differs from source")
	}
}

func TestSequentialNumbering(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "adr-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	manager := &ADRManager{
		decisionsDir: tmpDir,
	}

	titles := []string{"First Decision", "Second Decision", "Third Decision"}
	expectedNumbers := []int{1, 2, 3}

	for i, title := range titles {
		docTask, err := manager.CreateADR(title, "author")
		if err != nil {
			t.Fatalf("CreateADR failed for %s: %v", title, err)
		}

		if docTask.ADRNumber != expectedNumbers[i] {
			t.Errorf("expected ADR number %d for '%s', got %d", expectedNumbers[i], title, docTask.ADRNumber)
		}
	}

	nextNum, err := manager.GetNextADRNumber()
	if err != nil {
		t.Fatalf("GetNextADRNumber failed: %v", err)
	}
	if nextNum != 4 {
		t.Errorf("expected next number to be 4, got %d", nextNum)
	}
}

func createTestADR(dir, filename, status string) error {
	paddedNum := strings.Split(filename, "-")[0]
	content := fmt.Sprintf(`---
adr: %s
created: %s
modified: %s
status: %s
author: test
---

# ADR-%s: Test Title

## Status
%s

## Context
Test context

## Decision
Test decision

## Consequences
Test consequences
`,
		paddedNum,
		time.Now().Format(time.RFC3339),
		time.Now().Format(time.RFC3339),
		status,
		paddedNum,
		status,
	)
	return ioutil.WriteFile(filepath.Join(dir, filename), []byte(content), 0644)
}

func TestSlugify(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Git Worktree Isolation", "git-worktree-isolation"},
		{"API Design", "api-design"},
		{"Database Schema", "database-schema"},
		{"Simple Title", "simple-title"},
	}

	for _, tt := range tests {
		result := slugify(tt.input)
		if result != tt.expected {
			t.Errorf("slugify(%q) = %q, want %q", tt.input, result, tt.expected)
		}
	}
}

func TestUpdateStatusInContent(t *testing.T) {
	original := `---
adr: 001
created: 2024-01-01T00:00:00Z
modified: 2024-01-01T00:00:00Z
status: draft
author: test
---

# ADR-001: Test
`
	newTimestamp := "2024-06-15T12:00:00Z"
	updated := updateStatusInContent(original, "accepted", newTimestamp)

	if !strings.Contains(updated, "status: accepted") {
		t.Error("status not updated in content")
	}

	if !strings.Contains(updated, "modified: "+newTimestamp) {
		t.Error("modified timestamp not updated in content")
	}

	if !strings.Contains(updated, "created: 2024-01-01T00:00:00Z") {
		t.Error("created timestamp was modified unexpectedly")
	}
}
