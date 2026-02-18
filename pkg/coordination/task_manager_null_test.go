package coordination

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) (*SQLiteStorage, func()) {
	tmpDir, err := os.MkdirTemp("", "task_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	storage, err := NewSQLiteStorage(dbPath, "test-workspace", tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatalf("Failed to create storage: %v", err)
	}

	cleanup := func() {
		storage.Close()
		os.RemoveAll(tmpDir)
	}

	return storage, cleanup
}

func TestSQLiteStorage_GetTask_NullFields(t *testing.T) {
	storage, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	task := &Task{
		Title:       "Test Task",
		Description: "A test task",
		Status:      TaskStatusPending,
		Priority:    1,
	}
	err := storage.CreateTask(ctx, task)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	retrieved, err := storage.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("GetTask returned nil")
	}

	if retrieved.ID != task.ID {
		t.Errorf("Expected ID %s, got %s", task.ID, retrieved.ID)
	}

	if retrieved.Title != task.Title {
		t.Errorf("Expected Title %s, got %s", task.Title, retrieved.Title)
	}

	if retrieved.VerificationBy != "" {
		t.Errorf("Expected empty VerificationBy, got %s", retrieved.VerificationBy)
	}

	if retrieved.VerificationAt != nil {
		t.Errorf("Expected nil VerificationAt, got %v", retrieved.VerificationAt)
	}

	if retrieved.Verification != nil {
		t.Errorf("Expected nil Verification, got %v", retrieved.Verification)
	}

	if retrieved.RejectionCount != 0 {
		t.Errorf("Expected RejectionCount 0, got %d", retrieved.RejectionCount)
	}
}

func TestSQLiteStorage_ListTasks_NullFields(t *testing.T) {
	storage, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	task1 := &Task{
		Title:       "Task with verification",
		Description: "A task that will be verified",
		Status:      TaskStatusVerification,
		Priority:    1,
	}
	err := storage.CreateTask(ctx, task1)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	task2 := &Task{
		Title:       "Pending task",
		Description: "A pending task",
		Status:      TaskStatusPending,
		Priority:    2,
	}
	err = storage.CreateTask(ctx, task2)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	tasks, err := storage.ListTasks(ctx, "", "")
	if err != nil {
		t.Fatalf("ListTasks failed: %v", err)
	}

	if len(tasks) != 2 {
		t.Errorf("Expected 2 tasks, got %d", len(tasks))
	}

	for _, task := range tasks {
		if task.VerificationBy != "" {
			t.Errorf("Expected empty VerificationBy for task %s, got %s", task.ID, task.VerificationBy)
		}
	}
}

func TestSQLiteStorage_GetTask_WithVerification(t *testing.T) {
	storage, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	task := &Task{
		Title:       "Task to verify",
		Description: "A task ready for verification",
		Status:      TaskStatusVerification,
		Priority:    1,
	}
	err := storage.CreateTask(ctx, task)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	verifier := "test-verifier"
	task.VerificationBy = verifier
	now := time.Now()
	task.VerificationAt = &now
	task.Verification = &VerificationCriteria{
		TestsPass:      true,
		LintPass:       true,
		TypeCheckPass:  true,
		ReviewComplete: true,
		DocsComplete:   true,
	}

	err = storage.UpdateTask(ctx, task.ID, task)
	if err != nil {
		t.Fatalf("UpdateTask failed: %v", err)
	}

	retrieved, err := storage.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("GetTask returned nil")
	}

	if retrieved.VerificationBy != verifier {
		t.Errorf("Expected VerificationBy %s, got %s", verifier, retrieved.VerificationBy)
	}

	if retrieved.VerificationAt == nil {
		t.Error("Expected non-nil VerificationAt")
	}

	if retrieved.Verification == nil {
		t.Error("Expected non-nil Verification")
	}
}

func TestSQLiteStorage_GetTask_WithRejections(t *testing.T) {
	storage, cleanup := setupTestDB(t)
	defer cleanup()

	ctx := context.Background()

	task := &Task{
		Title:       "Task with rejections",
		Description: "A rejected task",
		Status:      TaskStatusRejected,
		Priority:    1,
	}
	err := storage.CreateTask(ctx, task)
	if err != nil {
		t.Fatalf("CreateTask failed: %v", err)
	}

	task.RejectionCount = 2
	task.RejectionHistory = []RejectionRecord{
		{
			Reason:     "Missing tests",
			RejectedBy: "reviewer-1",
			RejectedAt: time.Now().Add(-24 * time.Hour),
		},
		{
			Reason:     "Lint errors",
			RejectedBy: "reviewer-2",
			RejectedAt: time.Now().Add(-1 * time.Hour),
		},
	}

	err = storage.UpdateTask(ctx, task.ID, task)
	if err != nil {
		t.Fatalf("UpdateTask failed: %v", err)
	}

	retrieved, err := storage.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("GetTask failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("GetTask returned nil")
	}

	if retrieved.RejectionCount != 2 {
		t.Errorf("Expected RejectionCount 2, got %d", retrieved.RejectionCount)
	}

	if len(retrieved.RejectionHistory) != 2 {
		t.Errorf("Expected 2 rejection records, got %d", len(retrieved.RejectionHistory))
	}

	if retrieved.RejectionHistory[0].Reason != "Missing tests" {
		t.Errorf("Expected first rejection reason 'Missing tests', got '%s'", retrieved.RejectionHistory[0].Reason)
	}
}
