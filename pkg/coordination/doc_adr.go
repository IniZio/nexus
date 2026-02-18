package coordination

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type ADRRecord struct {
	Number   int
	Filename string
	Status   string
	Content  string
}

type ADRManager struct {
	store        *SQLiteStorage
	decisionsDir string
}

func NewADRManager(store *SQLiteStorage, decisionsDir string) *ADRManager {
	return &ADRManager{
		store:        store,
		decisionsDir: decisionsDir,
	}
}

func (m *ADRManager) GetNextADRNumber() (int, error) {
	adrs, err := m.getExistingADRs()
	if err != nil {
		return 0, err
	}

	maxNum := 0
	for _, adr := range adrs {
		if adr.Number > maxNum {
			maxNum = adr.Number
		}
	}

	return maxNum + 1, nil
}

func (m *ADRManager) getExistingADRs() ([]ADRRecord, error) {
	var adrs []ADRRecord

	files, err := ioutil.ReadDir(m.decisionsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read decisions dir: %w", err)
	}

	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".md") {
			number, err := extractADRNumber(f.Name())
			if err != nil {
				continue
			}
			adrs = append(adrs, ADRRecord{
				Number:   number,
				Filename: f.Name(),
			})
		}
	}

	return adrs, nil
}

func extractADRNumber(filename string) (int, error) {
	parts := strings.Split(filename, "-")
	if len(parts) < 1 {
		return 0, fmt.Errorf("invalid ADR filename format")
	}
	return strconv.Atoi(parts[0])
}

func (m *ADRManager) getADR(number int) (*ADRRecord, error) {
	paddedNum := fmt.Sprintf("%03d", number)

	files, err := ioutil.ReadDir(m.decisionsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read decisions dir: %w", err)
	}

	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
			continue
		}
		if strings.HasPrefix(f.Name(), paddedNum+"-") {
			content, err := ioutil.ReadFile(filepath.Join(m.decisionsDir, f.Name()))
			if err != nil {
				return nil, err
			}
			return &ADRRecord{
				Number:   number,
				Filename: f.Name(),
				Status:   extractStatus(string(content)),
				Content:  string(content),
			}, nil
		}
	}

	return nil, fmt.Errorf("ADR %d not found", number)
}

func extractStatus(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "status:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "status:"))
		}
	}
	return "unknown"
}

func (m *ADRManager) CreateADR(title string, author string) (*DocTask, error) {
	number, err := m.GetNextADRNumber()
	if err != nil {
		return nil, err
	}

	paddedNum := fmt.Sprintf("%03d", number)
	shortTitle := slugify(title)
	filename := fmt.Sprintf("%s-%s.md", paddedNum, shortTitle)

	now := time.Now().UTC()

	content := fmt.Sprintf(`---
adr: %s
created: %s
modified: %s
status: draft
author: %s
---

# ADR-%s: %s

## Status
Draft (created: %s)

## Context
What is the issue that we're seeing that is motivating this decision or change?

## Decision
What is the change that we're proposing or have agreed to implement?

## Consequences

### Positive
-

### Negative
-

## Alternatives Considered

### Alternative 1: [Title]
- **Pros:**
- **Cons:**
- **Decision:** Rejected because...

## References
-
`,
		paddedNum,
		now.Format(time.RFC3339),
		now.Format(time.RFC3339),
		author,
		paddedNum,
		title,
		now.Format("2006-01-02"),
	)

	fullPath := filepath.Join(m.decisionsDir, filename)
	err = ioutil.WriteFile(fullPath, []byte(content), 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to write ADR file: %w", err)
	}

	docTask := DocTask{
		Task: Task{
			Title:       fmt.Sprintf("ADR-%s: %s", paddedNum, title),
			Status:      TaskStatusPending,
			Description: fmt.Sprintf("Architecture Decision Record %s", paddedNum),
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		DocType:   DocTypeADR,
		ADRNumber: number,
		DraftPath: filename,
	}

	return &docTask, nil
}

func (m *ADRManager) UpdateADRStatus(number int, newStatus string) error {
	adr, err := m.getADR(number)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format(time.RFC3339)
	updatedContent := updateStatusInContent(adr.Content, newStatus, now)

	fullPath := filepath.Join(m.decisionsDir, adr.Filename)
	return ioutil.WriteFile(fullPath, []byte(updatedContent), 0644)
}

func (m *ADRManager) PublishADR(number int) error {
	adr, err := m.getADR(number)
	if err != nil {
		return err
	}

	if adr.Status != "accepted" {
		return fmt.Errorf("ADR must be accepted before publishing (current: %s)", adr.Status)
	}

	sourcePath := filepath.Join(m.decisionsDir, adr.Filename)
	targetDir := filepath.Join(m.decisionsDir, "docs/dev/decisions")
	targetPath := filepath.Join(targetDir, adr.Filename)

	content, err := ioutil.ReadFile(sourcePath)
	if err != nil {
		return fmt.Errorf("failed to read ADR: %w", err)
	}

	os.MkdirAll(targetDir, 0755)
	return ioutil.WriteFile(targetPath, content, 0644)
}

func slugify(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

func updateStatusInContent(content, status, timestamp string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "status:") {
			lines[i] = fmt.Sprintf("status: %s", status)
		}
		if strings.HasPrefix(line, "modified:") {
			lines[i] = fmt.Sprintf("modified: %s", timestamp)
		}
	}
	return strings.Join(lines, "\n")
}

func (m *ADRManager) saveADRContent(number int, content string) error {
	adr, err := m.getADR(number)
	if err != nil {
		return err
	}
	fullPath := filepath.Join(m.decisionsDir, adr.Filename)
	return ioutil.WriteFile(fullPath, []byte(content), 0644)
}

func (m *ADRManager) saveADR(docTask *DocTask, content string) error {
	return nil
}
