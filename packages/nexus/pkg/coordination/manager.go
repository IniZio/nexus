package coordination

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type TaskManager struct {
	storage Storage
}

func NewTaskManager(workspaceDir string) (*TaskManager, error) {
	dbPath := filepath.Join(workspaceDir, ".nexus", "pulse.db")
	workspaceID := filepath.Base(workspaceDir)
	workspacesRoot := workspaceDir

	storage, err := NewSQLiteStorage(dbPath, workspaceID, workspacesRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to create storage: %w", err)
	}

	return &TaskManager{storage: storage}, nil
}

func (m *TaskManager) CreateTask(ctx context.Context, req CreateTaskRequest) (*Task, error) {
	if req.Title == "" {
		return nil, fmt.Errorf("task title is required")
	}

	if err := m.validateDependencies(ctx, req.DependsOn); err != nil {
		return nil, fmt.Errorf("invalid dependencies: %w", err)
	}

	task := &Task{
		Title:       req.Title,
		Description: req.Description,
		Priority:    req.Priority,
		DependsOn:   req.DependsOn,
	}

	if err := m.storage.CreateTask(ctx, task); err != nil {
		return nil, fmt.Errorf("failed to create task: %w", err)
	}

	m.recordEvent(ctx, Event{
		Type:   EventTaskCreated,
		TaskID: task.ID,
	})

	return task, nil
}

func (m *TaskManager) GetTask(ctx context.Context, id string) (*Task, error) {
	task, err := m.storage.GetTask(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", id)
	}
	return task, nil
}

func (m *TaskManager) ListTasks(ctx context.Context, status TaskStatus) ([]*Task, error) {
	tasks, err := m.storage.ListTasks(ctx, "", status)
	if err != nil {
		return nil, fmt.Errorf("failed to list tasks: %w", err)
	}
	return tasks, nil
}

func (m *TaskManager) AssignTask(ctx context.Context, taskID string, agentID string) (*Task, error) {
	task, err := m.storage.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}

	if task.Status == TaskStatusCompleted {
		return nil, fmt.Errorf("task already completed")
	}

	if err := m.checkDependencies(ctx, task); err != nil {
		return nil, fmt.Errorf("dependencies not satisfied: %w", err)
	}

	task.Assignee = agentID
	task.Status = TaskStatusAssigned
	if err := m.storage.UpdateTask(ctx, taskID, task); err != nil {
		return nil, fmt.Errorf("failed to assign task: %w", err)
	}

	m.recordEvent(ctx, Event{
		Type:    EventTaskAssigned,
		TaskID:  task.ID,
		AgentID: agentID,
	})

	return task, nil
}

func (m *TaskManager) StartTask(ctx context.Context, taskID string) (*Task, error) {
	task, err := m.storage.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}

	task.Status = TaskStatusInProgress
	if err := m.storage.UpdateTask(ctx, taskID, task); err != nil {
		return nil, fmt.Errorf("failed to start task: %w", err)
	}

	return task, nil
}

func (m *TaskManager) CompleteTask(ctx context.Context, taskID string) (*Task, error) {
	task, err := m.storage.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}

	if task.Status != TaskStatusInProgress {
		return nil, fmt.Errorf("cannot complete task: task must be in_progress, got %s", task.Status)
	}

	task.Status = TaskStatusVerification
	if err := m.storage.UpdateTask(ctx, taskID, task); err != nil {
		return nil, fmt.Errorf("failed to route task to verification: %w", err)
	}

	m.recordEvent(ctx, Event{
		Type:   EventTaskCompleted,
		TaskID: task.ID,
		Data:   "routed to verification",
	})

	return task, nil
}

func (m *TaskManager) VerifyTask(ctx context.Context, taskID string) (*Task, error) {
	task, err := m.storage.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}

	if task.Status != TaskStatusInProgress {
		return nil, fmt.Errorf("cannot verify task: task must be in_progress, got %s", task.Status)
	}

	task.Status = TaskStatusVerification
	if err := m.storage.UpdateTask(ctx, taskID, task); err != nil {
		return nil, fmt.Errorf("failed to verify task: %w", err)
	}

	m.recordEvent(ctx, Event{
		Type:   EventTaskCompleted,
		TaskID: task.ID,
		Data:   "verification started",
	})

	return task, nil
}

func (m *TaskManager) ApproveTask(ctx context.Context, taskID string, verifiedBy string, criteria *VerificationCriteria) (*Task, error) {
	if verifiedBy == "" {
		return nil, fmt.Errorf("verifier identity is required")
	}

	task, err := m.storage.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}

	if task.Status != TaskStatusVerification {
		return nil, fmt.Errorf("cannot approve task: task must be in verification status, got %s", task.Status)
	}

	now := time.Now()
	task.Status = TaskStatusCompleted
	task.CompletedAt = &now
	task.VerificationBy = verifiedBy
	task.VerificationAt = &now
	task.Verification = criteria

	if err := m.storage.UpdateTask(ctx, taskID, task); err != nil {
		return nil, fmt.Errorf("failed to approve task: %w", err)
	}

	m.recordEvent(ctx, Event{
		Type:    EventTaskCompleted,
		TaskID:  task.ID,
		AgentID: verifiedBy,
		Data:    "task approved",
	})

	return task, nil
}

func (m *TaskManager) RejectTask(ctx context.Context, taskID string, reason string, rejectedBy string, unassign bool) (*Task, error) {
	if rejectedBy == "" {
		return nil, fmt.Errorf("rejector identity is required")
	}
	if reason == "" {
		return nil, fmt.Errorf("rejection reason is required")
	}

	task, err := m.storage.GetTask(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("failed to get task: %w", err)
	}
	if task == nil {
		return nil, fmt.Errorf("task not found: %s", taskID)
	}

	if task.Status != TaskStatusVerification {
		return nil, fmt.Errorf("cannot reject task: task must be in verification status, got %s", task.Status)
	}

	now := time.Now()
	record := RejectionRecord{
		Reason:     reason,
		RejectedBy: rejectedBy,
		RejectedAt: now,
	}

	task.Status = TaskStatusInProgress
	task.RejectionCount++
	task.RejectionHistory = append(task.RejectionHistory, record)
	if unassign {
		task.Assignee = ""
	}

	if err := m.storage.UpdateTask(ctx, taskID, task); err != nil {
		return nil, fmt.Errorf("failed to reject task: %w", err)
	}

	m.recordEvent(ctx, Event{
		Type:    EventTaskCompleted,
		TaskID:  task.ID,
		AgentID: rejectedBy,
		Data:    fmt.Sprintf("task rejected: %s", reason),
	})

	return task, nil
}

func (m *TaskManager) RegisterAgent(ctx context.Context, name string, capabilities []string) (*Agent, error) {
	if name == "" {
		return nil, fmt.Errorf("agent name is required")
	}

	agent := &Agent{
		Name:         name,
		Capabilities: capabilities,
		Status:       AgentStatusIdle,
	}

	if err := m.storage.RegisterAgent(ctx, agent); err != nil {
		return nil, fmt.Errorf("failed to register agent: %w", err)
	}

	m.recordEvent(ctx, Event{
		Type:    EventAgentJoined,
		AgentID: agent.ID,
	})

	return agent, nil
}

func (m *TaskManager) ListAgents(ctx context.Context) ([]*Agent, error) {
	agents, err := m.storage.ListAgents(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("failed to list agents: %w", err)
	}
	return agents, nil
}

func (m *TaskManager) UpdateAgentStatus(ctx context.Context, agentID string, status AgentStatus, currentTask string) (*Agent, error) {
	agent, err := m.storage.GetAgent(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("failed to get agent: %w", err)
	}
	if agent == nil {
		return nil, fmt.Errorf("agent not found: %s", agentID)
	}

	agent.Status = status
	agent.CurrentTask = currentTask
	if err := m.storage.UpdateAgent(ctx, agentID, agent); err != nil {
		return nil, fmt.Errorf("failed to update agent: %w", err)
	}

	return agent, nil
}

func (m *TaskManager) Close() error {
	return m.storage.Close()
}

func (m *TaskManager) SavePortMapping(ctx context.Context, workspaceName string, mapping PortMapping) error {
	return m.storage.SavePortMapping(ctx, workspaceName, mapping)
}

func (m *TaskManager) GetPortMappings(ctx context.Context, workspaceName string) ([]PortMapping, error) {
	return m.storage.GetPortMappings(ctx, workspaceName)
}

func (m *TaskManager) DeletePortMappings(ctx context.Context, workspaceName string) error {
	return m.storage.DeletePortMappings(ctx, workspaceName)
}

func (m *TaskManager) ListAllPortMappings(ctx context.Context) (map[string][]PortMapping, error) {
	return m.storage.ListAllPortMappings(ctx)
}

func (m *TaskManager) SaveSyncSession(ctx context.Context, workspaceName, sessionID string) error {
	return m.storage.SaveSyncSession(ctx, workspaceName, sessionID)
}

func (m *TaskManager) GetSyncSession(ctx context.Context, workspaceName string) (string, error) {
	return m.storage.GetSyncSession(ctx, workspaceName)
}

func (m *TaskManager) DeleteSyncSession(ctx context.Context, workspaceName string) error {
	return m.storage.DeleteSyncSession(ctx, workspaceName)
}

func (m *TaskManager) validateDependencies(ctx context.Context, deps []string) error {
	for _, depID := range deps {
		task, err := m.storage.GetTask(ctx, depID)
		if err != nil {
			return err
		}
		if task == nil {
			return fmt.Errorf("dependency not found: %s", depID)
		}
	}
	return nil
}

func (m *TaskManager) checkDependencies(ctx context.Context, task *Task) error {
	for _, depID := range task.DependsOn {
		dep, err := m.storage.GetTask(ctx, depID)
		if err != nil {
			return err
		}
		if dep == nil {
			return fmt.Errorf("dependency not found: %s", depID)
		}
		if dep.Status != TaskStatusCompleted {
			return fmt.Errorf("dependency not completed: %s (%s)", depID, dep.Status)
		}
	}
	return nil
}

func (m *TaskManager) recordEvent(ctx context.Context, event Event) {
	_ = m.storage.RecordEvent(ctx, &event)
}

func GetWorkspaceID() string {
	dir := os.Getenv("NEXUS_WORKSPACE_DIR")
	if dir == "" {
		dir = "."
	}
	return filepath.Base(dir)
}
