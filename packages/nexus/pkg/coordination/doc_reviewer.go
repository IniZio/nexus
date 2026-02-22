package coordination

import (
	"context"
	"fmt"
	"sort"
	"time"
)

type ReviewerStats struct {
	AgentID       string
	TotalReviews  int
	Approvals     int
	Rejections    int
	ApprovalRate  float64
	AvgReviewTime time.Duration
}

func (r ReviewerStats) GetApprovalRate() float64 {
	if r.TotalReviews == 0 {
		return 1.0
	}
	return float64(r.Approvals) / float64(r.TotalReviews)
}

type ReviewerStorage interface {
	GetAgent(ctx context.Context, id string) (*Agent, error)
	ListAgents(ctx context.Context, workspaceID string) ([]*Agent, error)
	GetReviewerStats(ctx context.Context, agentID string) (*ReviewerStats, error)
	SaveReviewerStats(ctx context.Context, stats ReviewerStats) error
	RecordReview(ctx context.Context, agentID string, approved bool, duration time.Duration) error
}

type DocReviewerAssigner struct {
	store ReviewerStorage
}

func NewDocReviewerAssigner(store ReviewerStorage) *DocReviewerAssigner {
	return &DocReviewerAssigner{store: store}
}

func (a *DocReviewerAssigner) AssignReviewer(docTask DocTask) (string, error) {
	implementer := docTask.Assignee

	agents, err := a.store.ListAgents(context.Background(), docTask.WorkspaceID)
	if err != nil {
		return "", fmt.Errorf("failed to list agents: %w", err)
	}

	var eligible []Agent
	for _, agent := range agents {
		if agent != nil && agent.ID != implementer && hasDocReviewerCapability(agent) {
			eligible = append(eligible, *agent)
		}
	}

	if len(eligible) == 0 {
		return "", fmt.Errorf("no eligible reviewers found (implementer excluded)")
	}

	var reviewerStats []ReviewerStats
	for _, agent := range eligible {
		stats, err := a.store.GetReviewerStats(context.Background(), agent.ID)
		if err != nil || stats == nil {
			stats = &ReviewerStats{
				AgentID:      agent.ID,
				TotalReviews: 0,
				Approvals:    0,
				Rejections:   0,
				ApprovalRate: 1.0,
			}
		}
		reviewerStats = append(reviewerStats, *stats)
	}

	sort.Slice(reviewerStats, func(i, j int) bool {
		return reviewerStats[i].GetApprovalRate() < reviewerStats[j].GetApprovalRate()
	})

	return reviewerStats[0].AgentID, nil
}

func (a *DocReviewerAssigner) GetReviewerInstructions(docTask DocTask) string {
	return fmt.Sprintf(`REVIEWER ASSIGNMENT: %s

Document: %s
Type: %s
Author: %s

INSTRUCTIONS:
1. Be SKEPTICAL. Do not compromise on quality.
2. Your approval carries MORE WEIGHT than the author's opinion.
3. Reject if ANY section is unclear, incomplete, or untested.
4. Verify all code examples work.
5. Check that prerequisites are accurate.
6. Ensure troubleshooting section covers common failures.

REMEMBER: You are the final quality gate. Poor documentation hurts users.
`,
		docTask.ID,
		docTask.Title,
		docTask.DocType,
		docTask.Assignee,
	)
}

func (a *DocReviewerAssigner) ValidateReviewerPrecedence(docTask DocTask) error {
	if docTask.VerifiedBy == docTask.Assignee {
		return fmt.Errorf("author cannot approve their own document (reviewer required)")
	}

	if docTask.VerifiedBy == "" {
		return fmt.Errorf("document must be verified by a reviewer")
	}

	verifier, err := a.store.GetAgent(context.Background(), docTask.VerifiedBy)
	if err != nil || verifier == nil {
		return fmt.Errorf("verifier not found: %s", docTask.VerifiedBy)
	}

	if !hasDocReviewerCapability(verifier) {
		return fmt.Errorf("approver must have doc-reviewer capability")
	}

	return nil
}

func (a *DocReviewerAssigner) RecordReviewOutcome(agentID string, approved bool, duration time.Duration) error {
	return a.store.RecordReview(context.Background(), agentID, approved, duration)
}

func hasDocReviewerCapability(agent *Agent) bool {
	for _, cap := range agent.Capabilities {
		if cap == "doc-reviewer" {
			return true
		}
	}
	return false
}
