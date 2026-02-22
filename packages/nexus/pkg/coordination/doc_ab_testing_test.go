package coordination

import (
	"testing"
	"time"
)

func TestCompletionRate(t *testing.T) {
	tests := []struct {
		name     string
		metrics  TemplateMetrics
		expected float64
	}{
		{
			name:     "zero tasks",
			metrics:  TemplateMetrics{TasksCreated: 0, CompletedCount: 0},
			expected: 0,
		},
		{
			name:     "all completed",
			metrics:  TemplateMetrics{TasksCreated: 10, CompletedCount: 10},
			expected: 1.0,
		},
		{
			name:     "half completed",
			metrics:  TemplateMetrics{TasksCreated: 10, CompletedCount: 5},
			expected: 0.5,
		},
		{
			name:     "none completed",
			metrics:  TemplateMetrics{TasksCreated: 10, CompletedCount: 0},
			expected: 0,
		},
		{
			name:     "partial with failures",
			metrics:  TemplateMetrics{TasksCreated: 20, CompletedCount: 12, FailedCount: 3},
			expected: 0.6,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.metrics.CompletionRate()
			if result != tt.expected {
				t.Errorf("CompletionRate() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetVariants(t *testing.T) {
	registry := NewDocTemplateRegistry()

	tests := []struct {
		docType     DocType
		expectedLen int
		expectNil   bool
	}{
		{DocTypeTutorial, 2, false},
		{DocTypeHowTo, 2, false},
		{DocTypeExplanation, 2, false},
		{DocTypeReference, 2, false},
		{DocTypeADR, 0, true},
		{DocTypeResearch, 0, true},
	}

	for _, tt := range tests {
		t.Run(string(tt.docType), func(t *testing.T) {
			variants := registry.GetVariants(tt.docType)
			if tt.expectNil {
				if variants != nil {
					t.Errorf("GetVariants(%s) = %v, want nil", tt.docType, variants)
				}
			} else if len(variants) != tt.expectedLen {
				t.Errorf("GetVariants(%s) returned %d variants, want %d", tt.docType, len(variants), tt.expectedLen)
			}
		})
	}
}

func TestSelectVariant_NoVariants(t *testing.T) {
	registry := &DocTemplateRegistry{
		Variants: map[DocType][]TemplateVariant{},
	}

	result := registry.SelectVariant(DocTypeADR)
	if result != "" {
		t.Errorf("SelectVariant() = %q, want empty string", result)
	}
}

func TestSelectVariant_RandomSelection_UnderThreshold(t *testing.T) {
	registry := &DocTemplateRegistry{
		Variants: map[DocType][]TemplateVariant{
			DocTypeTutorial: {
				{ID: "v1", Metrics: TemplateMetrics{TasksCreated: 5}},
				{ID: "v2", Metrics: TemplateMetrics{TasksCreated: 4}},
			},
		},
	}

	selected := registry.SelectVariant(DocTypeTutorial)
	if selected == "" {
		t.Error("SelectVariant() returned empty string, expected a variant ID")
	}

	found := false
	for _, v := range registry.Variants[DocTypeTutorial] {
		if v.ID == selected {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("SelectVariant() returned unknown variant ID: %s", selected)
	}
}

func TestSelectVariant_BestCompletionRate(t *testing.T) {
	registry := &DocTemplateRegistry{
		Variants: map[DocType][]TemplateVariant{
			DocTypeHowTo: {
				{ID: "slow", Metrics: TemplateMetrics{TasksCreated: 10, CompletedCount: 3}},
				{ID: "fast", Metrics: TemplateMetrics{TasksCreated: 15, CompletedCount: 12}},
				{ID: "medium", Metrics: TemplateMetrics{TasksCreated: 20, CompletedCount: 10}},
			},
		},
	}

	selected := registry.SelectVariant(DocTypeHowTo)
	if selected != "fast" {
		t.Errorf("SelectVariant() = %q, want 'fast' (highest completion rate)", selected)
	}
}

func TestSelectVariant_TieBreaker(t *testing.T) {
	registry := &DocTemplateRegistry{
		Variants: map[DocType][]TemplateVariant{
			DocTypeReference: {
				{ID: "first", Metrics: TemplateMetrics{TasksCreated: 20, CompletedCount: 15}},
				{ID: "second", Metrics: TemplateMetrics{TasksCreated: 20, CompletedCount: 15}},
			},
		},
	}

	selected := registry.SelectVariant(DocTypeReference)
	if selected != "first" && selected != "second" {
		t.Errorf("SelectVariant() = %q, want first or second", selected)
	}
}

func TestSelectVariant_AllZeroMetrics(t *testing.T) {
	registry := &DocTemplateRegistry{
		Variants: map[DocType][]TemplateVariant{
			DocTypeExplanation: {
				{ID: "a", Metrics: TemplateMetrics{TasksCreated: 0}},
				{ID: "b", Metrics: TemplateMetrics{TasksCreated: 0}},
			},
		},
	}

	selected := registry.SelectVariant(DocTypeExplanation)
	if selected != "a" && selected != "b" {
		t.Errorf("SelectVariant() = %q, want 'a' or 'b'", selected)
	}
}

func TestSelectVariant_ExactThreshold(t *testing.T) {
	registry := &DocTemplateRegistry{
		Variants: map[DocType][]TemplateVariant{
			DocTypeTutorial: {
				{ID: "v1", Metrics: TemplateMetrics{TasksCreated: 10}},
				{ID: "v2", Metrics: TemplateMetrics{TasksCreated: 0}},
			},
		},
	}

	selected := registry.SelectVariant(DocTypeTutorial)
	if selected != "v1" {
		t.Errorf("SelectVariant() = %q, want 'v1' (threshold met, selects best)", selected)
	}
}

func TestTemplateVariantIDs(t *testing.T) {
	registry := NewDocTemplateRegistry()

	expectedIDs := map[DocType][]string{
		DocTypeTutorial:    {"tutorial-v1", "tutorial-v2"},
		DocTypeHowTo:       {"howto-v1", "howto-v2"},
		DocTypeExplanation: {"explanation-v1", "explanation-v2"},
		DocTypeReference:   {"reference-v1", "reference-v2"},
	}

	for docType, expectedIDsList := range expectedIDs {
		variants := registry.GetVariants(docType)
		if len(variants) != len(expectedIDsList) {
			t.Errorf("%s: got %d variants, want %d", docType, len(variants), len(expectedIDsList))
			continue
		}

		for i, v := range variants {
			if v.ID != expectedIDsList[i] {
				t.Errorf("%s variant %d: ID = %q, want %q", docType, i, v.ID, expectedIDsList[i])
			}
		}
	}
}

func TestTemplateVariantNames(t *testing.T) {
	registry := NewDocTemplateRegistry()

	expectedNames := map[DocType][]string{
		DocTypeTutorial:    {"Step-by-Step", "Goal-First"},
		DocTypeHowTo:       {"Problem-Solution", "Scenario-First"},
		DocTypeExplanation: {"Concept-First", "Analogy-First"},
		DocTypeReference:   {"Structured-Reference", "Quick-Reference"},
	}

	for docType, expectedNamesList := range expectedNames {
		variants := registry.GetVariants(docType)
		for i, v := range variants {
			if v.Name != expectedNamesList[i] {
				t.Errorf("%s variant %d: Name = %q, want %q", docType, i, v.Name, expectedNamesList[i])
			}
		}
	}
}

func TestTemplateVariantContentNotEmpty(t *testing.T) {
	registry := NewDocTemplateRegistry()

	docTypes := []DocType{DocTypeTutorial, DocTypeHowTo, DocTypeExplanation, DocTypeReference}
	for _, docType := range docTypes {
		variants := registry.GetVariants(docType)
		for _, v := range variants {
			if v.Content == "" {
				t.Errorf("%s variant %s has empty Content", docType, v.ID)
			}
		}
	}
}

func TestNewDocTemplateRegistry(t *testing.T) {
	registry := NewDocTemplateRegistry()
	if registry == nil {
		t.Fatal("NewDocTemplateRegistry() returned nil")
	}
	if registry.Variants == nil {
		t.Fatal("Registry.Variants is nil")
	}

	expectedTypes := []DocType{DocTypeTutorial, DocTypeHowTo, DocTypeExplanation, DocTypeReference}
	for _, docType := range expectedTypes {
		if _, ok := registry.Variants[docType]; !ok {
			t.Errorf("Registry missing variants for %s", docType)
		}
	}
}

func TestSelectVariant_PreservesOriginalRegistry(t *testing.T) {
	registry := NewDocTemplateRegistry()
	originalLen := len(registry.Variants)

	for i := 0; i < 100; i++ {
		registry.SelectVariant(DocTypeTutorial)
	}

	if len(registry.Variants) != originalLen {
		t.Error("SelectVariant() modified registry structure")
	}

	for _, variants := range registry.Variants {
		for _, v := range variants {
			if v.Metrics.TasksCreated != 0 {
				t.Error("SelectVariant() modified variant metrics")
			}
		}
	}
}

func TestCompletionRate_AllZero(t *testing.T) {
	m := TemplateMetrics{
		TasksCreated:   0,
		CompletedCount: 0,
		FailedCount:    0,
	}

	rate := m.CompletionRate()
	if rate != 0 {
		t.Errorf("CompletionRate() = %v, want 0", rate)
	}
}

func TestCompletionRate_SingleTask(t *testing.T) {
	m := TemplateMetrics{
		TasksCreated:   1,
		CompletedCount: 1,
	}

	rate := m.CompletionRate()
	if rate != 1.0 {
		t.Errorf("CompletionRate() = %v, want 1.0", rate)
	}
}

func TestSelectVariant_MixedThreshold(t *testing.T) {
	registry := &DocTemplateRegistry{
		Variants: map[DocType][]TemplateVariant{
			DocTypeReference: {
				{ID: "low-data", Metrics: TemplateMetrics{TasksCreated: 5, CompletedCount: 5}},
				{ID: "high-data", Metrics: TemplateMetrics{TasksCreated: 10, CompletedCount: 6}},
			},
		},
	}

	selected := registry.SelectVariant(DocTypeReference)
	if selected != "low-data" {
		t.Errorf("SelectVariant() = %q, want 'low-data' (highest completion rate: 100%% vs 60%%)", selected)
	}
}

func TestTemplateMetrics_AvgTimeToComplete(t *testing.T) {
	m := TemplateMetrics{
		TasksCreated:      10,
		CompletedCount:    8,
		FailedCount:       2,
		AvgTimeToComplete: 5 * time.Minute,
		AvgReviewRounds:   1.5,
	}

	if m.AvgTimeToComplete != 5*time.Minute {
		t.Errorf("AvgTimeToComplete = %v, want 5m", m.AvgTimeToComplete)
	}
	if m.AvgReviewRounds != 1.5 {
		t.Errorf("AvgReviewRounds = %v, want 1.5", m.AvgReviewRounds)
	}
}

func TestSelectVariant_BestWithWorseCompletionRate(t *testing.T) {
	registry := &DocTemplateRegistry{
		Variants: map[DocType][]TemplateVariant{
			DocTypeTutorial: {
				{ID: "poor", Metrics: TemplateMetrics{TasksCreated: 50, CompletedCount: 10}},
				{ID: "good", Metrics: TemplateMetrics{TasksCreated: 30, CompletedCount: 20}},
				{ID: "better", Metrics: TemplateMetrics{TasksCreated: 15, CompletedCount: 12}},
			},
		},
	}

	selected := registry.SelectVariant(DocTypeTutorial)
	if selected != "better" {
		t.Errorf("SelectVariant() = %q, want 'better' (highest completion rate: %v)", selected, 12.0/15.0)
	}
}
