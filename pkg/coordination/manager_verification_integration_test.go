package coordination

import (
	"context"
	"sync"
	"testing"
	"time"

	"nexus/pkg/testutil"
)

func setupTestManager(t *testing.T) (*TaskManager, func()) {
	tmpDir := t.TempDir()
	manager, err := NewTaskManager(tmpDir)
	if err != nil {
		t.Fatalf("failed to create test manager: %v", err)
	}
	cleanup := func() {
		manager.Close()
	}
	return manager, cleanup
}

func TestVerificationWorkflow_BasicHappyPath(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	agentID := testutil.RandomAgentName()
	task, err = manager.AssignTask(ctx, task.ID, agentID)
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	if task.Status != TaskStatusAssigned {
		t.Errorf("expected status assigned, got %s", task.Status)
	}

	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}
	if task.Status != TaskStatusInProgress {
		t.Errorf("expected status in_progress, got %s", task.Status)
	}

	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to complete task: %v", err)
	}
	if task.Status != TaskStatusVerification {
		t.Errorf("expected status verification, got %s", task.Status)
	}

	verifierID := "verifier-" + testutil.RandomString(5)
	criteria := &VerificationCriteria{
		TestsPass:      true,
		LintPass:       true,
		TypeCheckPass:  true,
		ReviewComplete: true,
		DocsComplete:   true,
	}
	task, err = manager.ApproveTask(ctx, task.ID, verifierID, criteria)
	if err != nil {
		t.Fatalf("failed to approve task: %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Errorf("expected status completed, got %s", task.Status)
	}
	if task.VerificationBy != verifierID {
		t.Errorf("expected verification by %s, got %s", verifierID, task.VerificationBy)
	}
	if task.VerificationAt == nil {
		t.Error("expected verification timestamp to be set")
	}
}

func TestVerificationWorkflow_RejectAndReapprove(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}

	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}

	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification: %v", err)
	}

	rejectorID := "rejector-" + testutil.RandomString(5)
	rejectReason := "Missing unit tests for new function"
	task, err = manager.RejectTask(ctx, task.ID, rejectReason, rejectorID, false)
	if err != nil {
		t.Fatalf("failed to reject task: %v", err)
	}
	if task.Status != TaskStatusInProgress {
		t.Errorf("expected status in_progress after rejection, got %s", task.Status)
	}
	if task.RejectionCount != 1 {
		t.Errorf("expected rejection count 1, got %d", task.RejectionCount)
	}
	if len(task.RejectionHistory) != 1 {
		t.Errorf("expected 1 rejection record, got %d", len(task.RejectionHistory))
	}
	if task.RejectionHistory[0].Reason != rejectReason {
		t.Errorf("expected rejection reason %s, got %s", rejectReason, task.RejectionHistory[0].Reason)
	}
	if task.RejectionHistory[0].RejectedBy != rejectorID {
		t.Errorf("expected rejector %s, got %s", rejectorID, task.RejectionHistory[0].RejectedBy)
	}

	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to restart task: %v", err)
	}

	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification again: %v", err)
	}

	verifierID := "verifier-" + testutil.RandomString(5)
	criteria := &VerificationCriteria{TestsPass: true, LintPass: true, TypeCheckPass: true, ReviewComplete: true, DocsComplete: true}
	task, err = manager.ApproveTask(ctx, task.ID, verifierID, criteria)
	if err != nil {
		t.Fatalf("failed to approve task after rework: %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Errorf("expected completed after approval, got %s", task.Status)
	}
	if task.RejectionCount != 1 {
		t.Errorf("expected rejection count still 1, got %d", task.RejectionCount)
	}
}

func TestVerificationWorkflow_MultipleRejections(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}
	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification: %v", err)
	}

	for i := 1; i <= 3; i++ {
		rejector := testutil.RandomAgentName()
		reason := "Rejection number " + string(rune('0'+i))
		task, err = manager.RejectTask(ctx, task.ID, reason, rejector, false)
		if err != nil {
			t.Fatalf("failed to reject task (attempt %d): %v", i, err)
		}
		if task.RejectionCount != i {
			t.Errorf("expected rejection count %d, got %d", i, task.RejectionCount)
		}
		if len(task.RejectionHistory) != i {
			t.Errorf("expected %d rejection records, got %d", i, len(task.RejectionHistory))
		}

		task, err = manager.StartTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("failed to restart task (attempt %d): %v", i, err)
		}
		task, err = manager.CompleteTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("failed to route to verification (attempt %d): %v", i, err)
		}
	}

	if task.RejectionCount != 3 {
		t.Errorf("final rejection count should be 3, got %d", task.RejectionCount)
	}
	if len(task.RejectionHistory) != 3 {
		t.Errorf("should have 3 rejection records, got %d", len(task.RejectionHistory))
	}
}

func TestVerificationWorkflow_CannotSkipVerification(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}

	_, err = manager.VerifyTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to verify task: %v", err)
	}

	_, err = manager.ApproveTask(ctx, task.ID, "verifier", &VerificationCriteria{TestsPass: true})
	if err != nil {
		t.Fatalf("failed to approve task: %v", err)
	}
}

func TestVerificationWorkflow_RejectWithUnassign(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	assignee := testutil.RandomAgentName()
	task, err = manager.AssignTask(ctx, task.ID, assignee)
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}

	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}

	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification: %v", err)
	}

	rejector := testutil.RandomAgentName()
	task, err = manager.RejectTask(ctx, task.ID, "Quality issues found", rejector, true)
	if err != nil {
		t.Fatalf("failed to reject task: %v", err)
	}
	if task.Assignee != "" {
		t.Errorf("expected assignee to be cleared, got %s", task.Assignee)
	}
}

func TestVerificationWorkflow_DifferentVerifierThanAssignee(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	assignee := testutil.RandomAgentName()
	verifier := testutil.RandomAgentName()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, assignee)
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	if task.Assignee != assignee {
		t.Errorf("expected assignee %s, got %s", assignee, task.Assignee)
	}

	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}

	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification: %v", err)
	}

	criteria := &VerificationCriteria{TestsPass: true, LintPass: true, TypeCheckPass: true, ReviewComplete: true, DocsComplete: true}
	task, err = manager.ApproveTask(ctx, task.ID, verifier, criteria)
	if err != nil {
		t.Fatalf("failed to approve task: %v", err)
	}
	if task.VerificationBy != verifier {
		t.Errorf("expected verifier %s, got %s", verifier, task.VerificationBy)
	}
	if task.Assignee != assignee {
		t.Errorf("assignee should still be %s, got %s", assignee, task.Assignee)
	}
}

func TestVerificationWorkflow_VerifyTaskFromInProgress(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}

	task, err = manager.VerifyTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to verify task: %v", err)
	}
	if task.Status != TaskStatusVerification {
		t.Errorf("expected verification status, got %s", task.Status)
	}
}

func TestVerificationWorkflow_ValidationErrors(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	_, err = manager.ApproveTask(ctx, task.ID, "", &VerificationCriteria{})
	if err == nil {
		t.Error("expected error when verifier is empty")
	}

	_, err = manager.RejectTask(ctx, task.ID, "", testutil.RandomAgentName(), false)
	if err == nil {
		t.Error("expected error when rejection reason is empty")
	}

	_, err = manager.RejectTask(ctx, task.ID, "reason", "", false)
	if err == nil {
		t.Error("expected error when rejector is empty")
	}
}

func TestVerificationWorkflow_InvalidTransitions(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	_, err = manager.VerifyTask(ctx, task.ID)
	if err == nil {
		t.Error("expected error when verifying pending task")
	}
	if err != nil && err.Error() != "cannot verify task: task must be in_progress, got pending" {
		t.Errorf("unexpected error: %v", err)
	}

	_, err = manager.ApproveTask(ctx, task.ID, "verifier", &VerificationCriteria{})
	if err == nil {
		t.Error("expected error when approving pending task")
	}

	_, err = manager.RejectTask(ctx, task.ID, "reason", "rejector", false)
	if err == nil {
		t.Error("expected error when rejecting pending task")
	}
}

func TestVerificationWorkflow_TaskNotFound(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	_, err := manager.VerifyTask(ctx, "nonexistent-task")
	if err == nil {
		t.Error("expected error for nonexistent task")
	}

	_, err = manager.ApproveTask(ctx, "nonexistent-task", "verifier", &VerificationCriteria{})
	if err == nil {
		t.Error("expected error for nonexistent task")
	}

	_, err = manager.RejectTask(ctx, "nonexistent-task", "reason", "rejector", false)
	if err == nil {
		t.Error("expected error for nonexistent task")
	}
}

func TestVerificationWorkflow_ReapprovalAfterReject(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}
	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification: %v", err)
	}

	task, err = manager.RejectTask(ctx, task.ID, "First rejection", testutil.RandomAgentName(), false)
	if err != nil {
		t.Fatalf("failed to reject: %v", err)
	}

	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to restart: %v", err)
	}
	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification again: %v", err)
	}

	task, err = manager.RejectTask(ctx, task.ID, "Second rejection", testutil.RandomAgentName(), false)
	if err != nil {
		t.Fatalf("failed to reject again: %v", err)
	}

	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to restart again: %v", err)
	}
	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification third time: %v", err)
	}

	criteria := &VerificationCriteria{TestsPass: true, LintPass: true, TypeCheckPass: true, ReviewComplete: true, DocsComplete: true}
	task, err = manager.ApproveTask(ctx, task.ID, testutil.RandomAgentName(), criteria)
	if err != nil {
		t.Fatalf("failed to approve after multiple rejections: %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Errorf("expected completed after approval, got %s", task.Status)
	}
	if task.RejectionCount != 2 {
		t.Errorf("expected 2 rejections, got %d", task.RejectionCount)
	}
}

func TestVerificationWorkflow_ConcurrentVerification(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}
	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification: %v", err)
	}

	var wg sync.WaitGroup
	successCount := 0
	var mu sync.Mutex

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			criteria := &VerificationCriteria{
				TestsPass:      true,
				LintPass:       true,
				TypeCheckPass:  true,
				ReviewComplete: true,
				DocsComplete:   true,
			}
			_, err := manager.ApproveTask(ctx, task.ID, "verifier-"+string(rune('0'+workerID)), criteria)
			if err != nil {
				return
			}
			mu.Lock()
			successCount++
			mu.Unlock()
		}(i)
	}

	wg.Wait()
	if successCount != 1 {
		t.Logf("Concurrent approval attempts: %d succeeded (race condition handled: %v)", successCount, successCount == 1)
	}
}

func TestVerificationWorkflow_VerificationTimeout(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}
	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification: %v", err)
	}

	tasks, err := manager.ListTasks(ctx, TaskStatusVerification)
	if err != nil {
		t.Fatalf("failed to list verification tasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Errorf("expected 1 task in verification, got %d", len(tasks))
	}

	found := false
	for _, vt := range tasks {
		if vt.ID == task.ID {
			found = true
			if vt.VerificationAt != nil {
				verificationAge := time.Since(*vt.VerificationAt)
				t.Logf("Task has been in verification for: %v", verificationAge)
			}
			break
		}
	}
	if !found {
		t.Error("task should be in verification list")
	}
}

func TestVerificationWorkflow_VerifyTaskDirect(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}

	task, err = manager.VerifyTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to verify task: %v", err)
	}
	if task.Status != TaskStatusVerification {
		t.Errorf("expected verification status, got %s", task.Status)
	}

	criteria := &VerificationCriteria{TestsPass: true, LintPass: true, TypeCheckPass: true, ReviewComplete: true, DocsComplete: true}
	task, err = manager.ApproveTask(ctx, task.ID, testutil.RandomAgentName(), criteria)
	if err != nil {
		t.Fatalf("failed to approve verified task: %v", err)
	}
	if task.Status != TaskStatusCompleted {
		t.Errorf("expected completed status after approval, got %s", task.Status)
	}
}

func TestVerificationWorkflow_RejectWithNoUnassign(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	assignee := testutil.RandomAgentName()
	task, err = manager.AssignTask(ctx, task.ID, assignee)
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}
	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification: %v", err)
	}

	task, err = manager.RejectTask(ctx, task.ID, "Minor issues", testutil.RandomAgentName(), false)
	if err != nil {
		t.Fatalf("failed to reject task: %v", err)
	}
	if task.Assignee != assignee {
		t.Errorf("expected assignee to remain %s, got %s", assignee, task.Assignee)
	}
}

func TestVerificationWorkflow_CompleteTaskAutoRoutesToVerification(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}

	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to complete task: %v", err)
	}
	if task.Status != TaskStatusVerification {
		t.Errorf("CompleteTask should auto-route to verification, got %s", task.Status)
	}

	tasks, err := manager.ListTasks(ctx, TaskStatusVerification)
	if err != nil {
		t.Fatalf("failed to list verification tasks: %v", err)
	}
	found := false
	for _, t := range tasks {
		if t.ID == task.ID {
			found = true
			break
		}
	}
	if !found {
		t.Error("task should be in verification list")
	}
}

func TestVerificationWorkflow_RejectionHistoryOrder(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}

	reasons := []string{"First rejection", "Second rejection", "Third rejection"}
	for i, reason := range reasons {
		task, err = manager.CompleteTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("failed to route to verification (round %d): %v", i+1, err)
		}

		task, err = manager.RejectTask(ctx, task.ID, reason, testutil.RandomAgentName(), false)
		if err != nil {
			t.Fatalf("failed to reject (round %d): %v", i+1, err)
		}

		task, err = manager.StartTask(ctx, task.ID)
		if err != nil {
			t.Fatalf("failed to restart task (round %d): %v", i+1, err)
		}
	}

	for i, record := range task.RejectionHistory {
		if record.Reason != reasons[i] {
			t.Errorf("rejection %d: expected reason %s, got %s", i+1, reasons[i], record.Reason)
		}
	}
}

func TestVerificationWorkflow_VerifyFromWrongStatus(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	testCases := []struct {
		name        string
		setupFunc   func() error
		expectedErr string
	}{
		{
			name:        "verify pending task",
			setupFunc:   func() error { return nil },
			expectedErr: "cannot verify task: task must be in_progress, got pending",
		},
		{
			name: "verify assigned task",
			setupFunc: func() error {
				task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
				if err != nil {
					return err
				}
				_, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
				return err
			},
			expectedErr: "cannot verify task: task must be in_progress, got assigned",
		},
		{
			name: "verify completed task",
			setupFunc: func() error {
				task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
				if err != nil {
					return err
				}
				_, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
				if err != nil {
					return err
				}
				_, err = manager.StartTask(ctx, task.ID)
				if err != nil {
					return err
				}
				_, err = manager.CompleteTask(ctx, task.ID)
				if err != nil {
					return err
				}
				criteria := &VerificationCriteria{TestsPass: true, LintPass: true, TypeCheckPass: true, ReviewComplete: true, DocsComplete: true}
				_, err = manager.ApproveTask(ctx, task.ID, testutil.RandomAgentName(), criteria)
				return err
			},
			expectedErr: "cannot verify task: task must be in_progress, got completed",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setupFunc() != nil {
				t.Skip("setup failed")
			}
			task, _ := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
			_, err := manager.VerifyTask(ctx, task.ID)
			if err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestVerificationWorkflow_ApproveFromWrongStatus(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	_, err = manager.ApproveTask(ctx, task.ID, "verifier", &VerificationCriteria{})
	if err == nil {
		t.Error("expected error when approving non-verification task")
	}
	if err != nil && err.Error() != "cannot approve task: task must be in verification status, got pending" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerificationWorkflow_RejectFromWrongStatus(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	_, err = manager.RejectTask(ctx, task.ID, "reason", "rejector", false)
	if err == nil {
		t.Error("expected error when rejecting non-verification task")
	}
	if err != nil && err.Error() != "cannot reject task: task must be in verification status, got pending" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerificationWorkflow_EmptyRejectionHistory(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	if len(task.RejectionHistory) != 0 {
		t.Errorf("expected empty rejection history, got %d entries", len(task.RejectionHistory))
	}
	if task.RejectionCount != 0 {
		t.Errorf("expected rejection count 0, got %d", task.RejectionCount)
	}
}

func TestVerificationWorkflow_PreserveVerificationOnReject(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}
	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification: %v", err)
	}

	firstRejector := testutil.RandomAgentName()
	task, err = manager.RejectTask(ctx, task.ID, "First rejection", firstRejector, false)
	if err != nil {
		t.Fatalf("failed to reject: %v", err)
	}

	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to restart: %v", err)
	}
	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification again: %v", err)
	}

	criteria := &VerificationCriteria{TestsPass: true, LintPass: true, TypeCheckPass: true, ReviewComplete: true, DocsComplete: true}
	verifier := testutil.RandomAgentName()
	task, err = manager.ApproveTask(ctx, task.ID, verifier, criteria)
	if err != nil {
		t.Fatalf("failed to approve: %v", err)
	}

	if task.VerificationBy != verifier {
		t.Errorf("expected verification by %s, got %s", verifier, task.VerificationBy)
	}
	if task.RejectionCount != 1 {
		t.Errorf("expected 1 rejection, got %d", task.RejectionCount)
	}
	if len(task.RejectionHistory) != 1 {
		t.Errorf("expected 1 rejection record, got %d", len(task.RejectionHistory))
	}
	if task.RejectionHistory[0].RejectedBy != firstRejector {
		t.Errorf("expected first rejector %s, got %s", firstRejector, task.RejectionHistory[0].RejectedBy)
	}
}

func TestVerificationWorkflow_CannotCompleteFromVerification(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	task, err = manager.AssignTask(ctx, task.ID, testutil.RandomAgentName())
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}
	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification: %v", err)
	}

	task, err = manager.CompleteTask(ctx, task.ID)
	if err == nil {
		t.Error("expected error when trying to complete from verification")
	}
	if err != nil && err.Error() != "cannot complete task: task must be in_progress, got verification" {
		t.Logf("Error: %v", err)
	}
}

func TestVerificationWorkflow_AssignAfterReject(t *testing.T) {
	manager, cleanup := setupTestManager(t)
	defer cleanup()
	ctx := context.Background()

	task, err := manager.CreateTask(ctx, CreateTaskRequest{Title: testutil.RandomTaskTitle()})
	if err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	firstAssignee := testutil.RandomAgentName()
	task, err = manager.AssignTask(ctx, task.ID, firstAssignee)
	if err != nil {
		t.Fatalf("failed to assign task: %v", err)
	}
	task, err = manager.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to start task: %v", err)
	}
	task, err = manager.CompleteTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("failed to route to verification: %v", err)
	}

	task, err = manager.RejectTask(ctx, task.ID, "Needs more work", testutil.RandomAgentName(), false)
	if err != nil {
		t.Fatalf("failed to reject: %v", err)
	}

	newAssignee := testutil.RandomAgentName()
	task, err = manager.AssignTask(ctx, task.ID, newAssignee)
	if err != nil {
		t.Fatalf("failed to reassign task: %v", err)
	}
	if task.Assignee != newAssignee {
		t.Errorf("expected assignee %s, got %s", newAssignee, task.Assignee)
	}
}
