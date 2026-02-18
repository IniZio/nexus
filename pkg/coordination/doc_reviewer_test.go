package coordination

import (
	"context"
	"testing"
	"time"
)

type mockReviewerStorage struct {
	agents        map[string]*Agent
	reviewerStats map[string]*ReviewerStats
	reviews       []reviewRecord
}

type reviewRecord struct {
	agentID  string
	approved bool
	duration time.Duration
}

func newMockReviewerStorage() *mockReviewerStorage {
	return &mockReviewerStorage{
		agents:        make(map[string]*Agent),
		reviewerStats: make(map[string]*ReviewerStats),
		reviews:       []reviewRecord{},
	}
}

func (m *mockReviewerStorage) GetAgent(ctx context.Context, id string) (*Agent, error) {
	return m.agents[id], nil
}

func (m *mockReviewerStorage) ListAgents(ctx context.Context, workspaceID string) ([]*Agent, error) {
	var result []*Agent
	for _, agent := range m.agents {
		if agent.WorkspaceID == workspaceID {
			result = append(result, agent)
		}
	}
	return result, nil
}

func (m *mockReviewerStorage) GetReviewerStats(ctx context.Context, agentID string) (*ReviewerStats, error) {
	return m.reviewerStats[agentID], nil
}

func (m *mockReviewerStorage) SaveReviewerStats(ctx context.Context, stats ReviewerStats) error {
	m.reviewerStats[stats.AgentID] = &stats
	return nil
}

func (m *mockReviewerStorage) RecordReview(ctx context.Context, agentID string, approved bool, duration time.Duration) error {
	m.reviews = append(m.reviews, reviewRecord{agentID, approved, duration})
	if stats, exists := m.reviewerStats[agentID]; exists {
		stats.TotalReviews++
		if approved {
			stats.Approvals++
		} else {
			stats.Rejections++
		}
		stats.ApprovalRate = float64(stats.Approvals) / float64(stats.TotalReviews)
	}
	return nil
}

func (m *mockReviewerStorage) addAgent(id, name, workspaceID string, capabilities []string) {
	m.agents[id] = &Agent{
		ID:           id,
		Name:         name,
		WorkspaceID:  workspaceID,
		Capabilities: capabilities,
		Status:       AgentStatusIdle,
	}
}

func (m *mockReviewerStorage) setReviewerStats(agentID string, total, approvals, rejections int) {
	m.reviewerStats[agentID] = &ReviewerStats{
		AgentID:      agentID,
		TotalReviews: total,
		Approvals:    approvals,
		Rejections:   rejections,
		ApprovalRate: float64(approvals) / float64(total),
	}
}

func TestAssignReviewer_NeverReturnsImplementer(t *testing.T) {
	store := newMockReviewerStorage()
	store.addAgent("agent-1", "Alice", "ws-1", []string{"doc-reviewer"})
	store.addAgent("agent-2", "Bob", "ws-1", []string{"doc-reviewer"})
	store.addAgent("agent-3", "Charlie", "ws-1", []string{"doc-reviewer"})

	assigner := NewDocReviewerAssigner(store)

	docTask := DocTask{
		Task: Task{
			ID:          "doc-1",
			WorkspaceID: "ws-1",
			Assignee:    "agent-1",
			Title:       "Test Documentation",
		},
		DocType: DocTypeTutorial,
	}

	for i := 0; i < 100; i++ {
		reviewer, err := assigner.AssignReviewer(docTask)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if reviewer == "agent-1" {
			t.Error("AssignReviewer returned the implementer")
		}
	}
}

func TestAssignReviewer_SelectsMostSkeptical(t *testing.T) {
	store := newMockReviewerStorage()
	store.addAgent("skeptic", "Skeptic", "ws-1", []string{"doc-reviewer"})
	store.addAgent("moderate", "Moderate", "ws-1", []string{"doc-reviewer"})
	store.addAgent("approver", "Approver", "ws-1", []string{"doc-reviewer"})

	store.setReviewerStats("skeptic", 10, 3, 7)
	store.setReviewerStats("moderate", 10, 5, 5)
	store.setReviewerStats("approver", 10, 9, 1)

	assigner := NewDocReviewerAssigner(store)

	docTask := DocTask{
		Task: Task{
			ID:          "doc-1",
			WorkspaceID: "ws-1",
			Assignee:    "implementer",
			Title:       "Test Documentation",
		},
		DocType: DocTypeHowTo,
	}

	reviewer, err := assigner.AssignReviewer(docTask)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reviewer != "skeptic" {
		t.Errorf("expected 'skeptic' but got '%s'", reviewer)
	}
}

func TestAssignReviewer_NoEligibleReviewers(t *testing.T) {
	store := newMockReviewerStorage()
	store.addAgent("implementer", "Implementer", "ws-1", []string{"doc-reviewer"})

	assigner := NewDocReviewerAssigner(store)

	docTask := DocTask{
		Task: Task{
			ID:          "doc-1",
			WorkspaceID: "ws-1",
			Assignee:    "implementer",
			Title:       "Test Documentation",
		},
		DocType: DocTypeReference,
	}

	_, err := assigner.AssignReviewer(docTask)
	if err == nil {
		t.Error("expected error when no eligible reviewers")
	}
}

func TestValidateReviewerPrecedence_BlocksSelfApproval(t *testing.T) {
	store := newMockReviewerStorage()
	store.addAgent("author", "Author", "ws-1", []string{"doc-reviewer"})
	store.addAgent("reviewer", "Reviewer", "ws-1", []string{"doc-reviewer"})

	assigner := NewDocReviewerAssigner(store)

	docTask := DocTask{
		Task: Task{
			ID:          "doc-1",
			WorkspaceID: "ws-1",
			Assignee:    "author",
			VerifiedBy:  "author",
			Title:       "Test Documentation",
		},
		DocType: DocTypeExplanation,
	}

	err := assigner.ValidateReviewerPrecedence(docTask)
	if err == nil {
		t.Error("expected error when author approves own document")
	}
	if err.Error() != "author cannot approve their own document (reviewer required)" {
		t.Errorf("unexpected error message: %s", err.Error())
	}
}

func TestValidateReviewerPrecedence_AllowsReviewerApproval(t *testing.T) {
	store := newMockReviewerStorage()
	store.addAgent("author", "Author", "ws-1", []string{"doc-reviewer"})
	store.addAgent("reviewer", "Reviewer", "ws-1", []string{"doc-reviewer"})

	assigner := NewDocReviewerAssigner(store)

	docTask := DocTask{
		Task: Task{
			ID:          "doc-1",
			WorkspaceID: "ws-1",
			Assignee:    "author",
			VerifiedBy:  "reviewer",
			Title:       "Test Documentation",
		},
		DocType: DocTypeADR,
	}

	err := assigner.ValidateReviewerPrecedence(docTask)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateReviewerPrecedence_RequiresReviewerCapability(t *testing.T) {
	store := newMockReviewerStorage()
	store.addAgent("author", "Author", "ws-1", []string{"doc-reviewer"})
	store.addAgent("no-capability", "NoCapability", "ws-1", []string{})

	assigner := NewDocReviewerAssigner(store)

	docTask := DocTask{
		Task: Task{
			ID:          "doc-1",
			WorkspaceID: "ws-1",
			Assignee:    "author",
			VerifiedBy:  "no-capability",
			Title:       "Test Documentation",
		},
		DocType: DocTypeResearch,
	}

	err := assigner.ValidateReviewerPrecedence(docTask)
	if err == nil {
		t.Error("expected error when approver lacks doc-reviewer capability")
	}
}

func TestValidateReviewerPrecedence_RequiresVerification(t *testing.T) {
	store := newMockReviewerStorage()
	store.addAgent("author", "Author", "ws-1", []string{"doc-reviewer"})

	assigner := NewDocReviewerAssigner(store)

	docTask := DocTask{
		Task: Task{
			ID:          "doc-1",
			WorkspaceID: "ws-1",
			Assignee:    "author",
			VerifiedBy:  "",
			Title:       "Test Documentation",
		},
		DocType: DocTypeTutorial,
	}

	err := assigner.ValidateReviewerPrecedence(docTask)
	if err == nil {
		t.Error("expected error when document not verified")
	}
}

func TestReviewerStats_GetApprovalRate(t *testing.T) {
	tests := []struct {
		name     string
		stats    ReviewerStats
		expected float64
	}{
		{
			name:     "new reviewer",
			stats:    ReviewerStats{TotalReviews: 0},
			expected: 1.0,
		},
		{
			name:     "50% approval",
			stats:    ReviewerStats{TotalReviews: 10, Approvals: 5, Rejections: 5},
			expected: 0.5,
		},
		{
			name:     "100% approval",
			stats:    ReviewerStats{TotalReviews: 10, Approvals: 10, Rejections: 0},
			expected: 1.0,
		},
		{
			name:     "0% approval",
			stats:    ReviewerStats{TotalReviews: 10, Approvals: 0, Rejections: 10},
			expected: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.stats.GetApprovalRate()
			if result != tt.expected {
				t.Errorf("expected %f but got %f", tt.expected, result)
			}
		})
	}
}

func TestGetReviewerInstructions_ContainsRequiredInfo(t *testing.T) {
	store := newMockReviewerStorage()
	assigner := NewDocReviewerAssigner(store)

	docTask := DocTask{
		Task: Task{
			ID:          "doc-test-123",
			WorkspaceID: "ws-1",
			Assignee:    "test-author",
			Title:       "How to Test Documentation",
		},
		DocType: DocTypeHowTo,
	}

	instructions := assigner.GetReviewerInstructions(docTask)

	if !stringContains(instructions, "REVIEWER ASSIGNMENT: doc-test-123") {
		t.Error("instructions missing assignment ID")
	}
	if !stringContains(instructions, "How to Test Documentation") {
		t.Error("instructions missing document title")
	}
	if !stringContains(instructions, "how-to") {
		t.Error("instructions missing document type")
	}
	if !stringContains(instructions, "test-author") {
		t.Error("instructions missing author")
	}
	if !stringContains(instructions, "SKEPTICAL") {
		t.Error("instructions missing skeptical directive")
	}
}

func TestAssignReviewer_NewReviewerDefaultsToHighApproval(t *testing.T) {
	store := newMockReviewerStorage()
	store.addAgent("implementer", "Implementer", "ws-1", []string{"doc-reviewer"})
	store.addAgent("new-reviewer", "NewReviewer", "ws-1", []string{"doc-reviewer"})

	assigner := NewDocReviewerAssigner(store)

	docTask := DocTask{
		Task: Task{
			ID:          "doc-1",
			WorkspaceID: "ws-1",
			Assignee:    "implementer",
			Title:       "Test Documentation",
		},
		DocType: DocTypeTutorial,
	}

	reviewer, err := assigner.AssignReviewer(docTask)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reviewer != "new-reviewer" {
		t.Errorf("expected new reviewer (no stats) to be selected but got '%s'", reviewer)
	}
}

func TestAssignReviewer_FiltersNonReviewers(t *testing.T) {
	store := newMockReviewerStorage()
	store.addAgent("implementer", "Implementer", "ws-1", []string{})
	store.addAgent("reviewer", "Reviewer", "ws-1", []string{"doc-reviewer"})
	store.addAgent("other", "Other", "ws-1", []string{"testing"})

	assigner := NewDocReviewerAssigner(store)

	docTask := DocTask{
		Task: Task{
			ID:          "doc-1",
			WorkspaceID: "ws-1",
			Assignee:    "implementer",
			Title:       "Test Documentation",
		},
		DocType: DocTypeTutorial,
	}

	reviewer, err := assigner.AssignReviewer(docTask)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reviewer != "reviewer" {
		t.Errorf("expected 'reviewer' but got '%s'", reviewer)
	}
}

func TestRecordReviewOutcome_UpdatesStats(t *testing.T) {
	store := newMockReviewerStorage()
	store.addAgent("reviewer", "Reviewer", "ws-1", []string{"doc-reviewer"})
	store.setReviewerStats("reviewer", 5, 3, 2)

	assigner := NewDocReviewerAssigner(store)

	err := assigner.RecordReviewOutcome("reviewer", true, 5*time.Minute)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stats, err := store.GetReviewerStats(context.Background(), "reviewer")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.TotalReviews != 6 {
		t.Errorf("expected 6 total reviews but got %d", stats.TotalReviews)
	}
	if stats.Approvals != 4 {
		t.Errorf("expected 4 approvals but got %d", stats.Approvals)
	}
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
