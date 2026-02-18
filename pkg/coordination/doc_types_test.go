package coordination

import (
	"testing"
	"time"
)

func TestDocTypeConstants(t *testing.T) {
	tests := []struct {
		docType DocType
		want    string
	}{
		{DocTypeTutorial, "tutorial"},
		{DocTypeHowTo, "how-to"},
		{DocTypeReference, "reference"},
		{DocTypeExplanation, "explanation"},
		{DocTypeADR, "adr"},
		{DocTypeResearch, "research"},
	}

	for _, tt := range tests {
		t.Run(string(tt.docType), func(t *testing.T) {
			if string(tt.docType) != tt.want {
				t.Errorf("DocType %q = %q, want %q", tt.docType, string(tt.docType), tt.want)
			}
		})
	}
}

func TestDocTaskCreation(t *testing.T) {
	now := time.Now()
	baseTask := Task{
		ID:          "task-123",
		WorkspaceID: "ws-456",
		Title:       "Test Documentation",
		Description: "A test doc task",
		Status:      TaskStatusPending,
		Priority:    1,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	docTask := DocTask{
		Task:            baseTask,
		DocType:         DocTypeTutorial,
		TemplateVariant: "tutorial-v1",
		DraftPath:       ".nexus/workspaces/ws-456/docs/test-tutorial.md",
		PublishPath:     "docs/tutorials/test-tutorial.md",
		ADRNumber:       0,
		Metrics: DocMetrics{
			TimeToComplete:    5 * time.Minute,
			ReviewRounds:      2,
			VerificationScore: 0.95,
			TemplateVariant:   "tutorial-v1",
		},
	}

	if docTask.ID != "task-123" {
		t.Errorf("DocTask.ID = %q, want %q", docTask.ID, "task-123")
	}
	if docTask.DocType != DocTypeTutorial {
		t.Errorf("DocTask.DocType = %q, want %q", docTask.DocType, DocTypeTutorial)
	}
	if docTask.TemplateVariant != "tutorial-v1" {
		t.Errorf("DocTask.TemplateVariant = %q, want %q", docTask.TemplateVariant, "tutorial-v1")
	}
	if docTask.Metrics.ReviewRounds != 2 {
		t.Errorf("DocTask.Metrics.ReviewRounds = %d, want %d", docTask.Metrics.ReviewRounds, 2)
	}
	if docTask.Metrics.VerificationScore != 0.95 {
		t.Errorf("DocTask.Metrics.VerificationScore = %f, want %f", docTask.Metrics.VerificationScore, 0.95)
	}
}

func TestDefaultDocStandards(t *testing.T) {
	standards := DefaultDocStandards()

	if standards.MaxReadingTime != 10*time.Minute {
		t.Errorf("DefaultDocStandards.MaxReadingTime = %v, want %v", standards.MaxReadingTime, 10*time.Minute)
	}
	if standards.MaxSections != 7 {
		t.Errorf("DefaultDocStandards.MaxSections = %d, want %d", standards.MaxSections, 7)
	}
	if !standards.RequireCodeExamples {
		t.Error("DefaultDocStandards.RequireCodeExamples should be true")
	}
	if !standards.RequireTroubleshooting {
		t.Error("DefaultDocStandards.RequireTroubleshooting should be true")
	}
	if standards.MaxLineLength != 100 {
		t.Errorf("DefaultDocStandards.MaxLineLength = %d, want %d", standards.MaxLineLength, 100)
	}
	if !standards.RequireSummary {
		t.Error("DefaultDocStandards.RequireSummary should be true")
	}
	if standards.DiataxisCompliance {
		t.Error("DefaultDocStandards.DiataxisCompliance should be false by default")
	}
	if standards.RequireDiagrams {
		t.Error("DefaultDocStandards.RequireDiagrams should be false by default")
	}
}

func TestNexusDocStandards(t *testing.T) {
	standards := NexusDocStandards()

	if standards.MaxReadingTime != 10*time.Minute {
		t.Errorf("NexusDocStandards.MaxReadingTime = %v, want %v", standards.MaxReadingTime, 10*time.Minute)
	}
	if !standards.DiataxisCompliance {
		t.Error("NexusDocStandards.DiataxisCompliance should be true")
	}
	if !standards.RequireDiagrams {
		t.Error("NexusDocStandards.RequireDiagrams should be true")
	}
	if !standards.RequireCodeExamples {
		t.Error("NexusDocStandards.RequireCodeExamples should be true")
	}
}

func TestDocMetricsTracking(t *testing.T) {
	metrics := DocMetrics{
		TimeToComplete:    30 * time.Minute,
		ReviewRounds:      5,
		VerificationScore: 0.88,
		TemplateVariant:   "how-to-v2",
	}

	if metrics.TimeToComplete != 30*time.Minute {
		t.Errorf("DocMetrics.TimeToComplete = %v, want %v", metrics.TimeToComplete, 30*time.Minute)
	}
	if metrics.ReviewRounds != 5 {
		t.Errorf("DocMetrics.ReviewRounds = %d, want %d", metrics.ReviewRounds, 5)
	}
	if metrics.VerificationScore != 0.88 {
		t.Errorf("DocMetrics.VerificationScore = %f, want %f", metrics.VerificationScore, 0.88)
	}
}

func TestDocTaskWithADR(t *testing.T) {
	now := time.Now()
	docTask := DocTask{
		Task: Task{
			ID:          "adr-001",
			WorkspaceID: "ws-main",
			Title:       "ADR: Use PostgreSQL for persistence",
			Status:      TaskStatusInProgress,
			CreatedAt:   now,
			UpdatedAt:   now,
		},
		DocType:     DocTypeADR,
		PublishPath: "docs/dev/decisions/001-postgresql.md",
		ADRNumber:   1,
	}

	if docTask.DocType != DocTypeADR {
		t.Errorf("DocTask.DocType = %q, want %q", docTask.DocType, DocTypeADR)
	}
	if docTask.ADRNumber != 1 {
		t.Errorf("DocTask.ADRNumber = %d, want %d", docTask.ADRNumber, 1)
	}
}

func TestDocStandardsImmutability(t *testing.T) {
	defaults := DefaultDocStandards()
	nexus := NexusDocStandards()

	if defaults.DiataxisCompliance == nexus.DiataxisCompliance {
		t.Error("DefaultDocStandards and NexusDocStandards should have different DiataxisCompliance")
	}
	if defaults.RequireDiagrams == nexus.RequireDiagrams {
		t.Error("DefaultDocStandards and NexusDocStandards should have different RequireDiagrams")
	}
}
