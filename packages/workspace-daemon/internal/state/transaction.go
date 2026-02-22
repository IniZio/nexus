package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/nexus/nexus/packages/workspace-daemon/internal/types"
)

var (
	ErrTransactionFailed = errors.New("transaction failed")
	ErrTransactionRolledBack = errors.New("transaction rolled back")
)

type TransactionType string

const (
	TransactionCreate   TransactionType = "create"
	TransactionUpdate   TransactionType = "update"
	TransactionDelete   TransactionType = "delete"
	TransactionStart    TransactionType = "start"
	TransactionStop     TransactionType = "stop"
	TransactionSnapshot TransactionType = "snapshot"
	TransactionRestore  TransactionType = "restore"
)

type TransactionStatus string

const (
	TransactionStatusPending   TransactionStatus = "pending"
	TransactionStatusCommitted TransactionStatus = "committed"
	TransactionStatusRolledBack TransactionStatus = "rolled_back"
	TransactionStatusFailed    TransactionStatus = "failed"
)

type Transaction struct {
	ID            string
	Type          TransactionType
	WorkspaceID   string
	Status        TransactionStatus
	Payload       json.RawMessage
	PreviousState json.RawMessage
	ErrorMessage  string
	CreatedAt     time.Time
	CompletedAt   time.Time
}

type TransactionLog struct {
	baseDir string
	mu      sync.Mutex
}

func NewTransactionLog(baseDir string) (*TransactionLog, error) {
	absPath, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to get absolute path: %w", err)
	}

	if err := os.MkdirAll(absPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create transaction log directory: %w", err)
	}

	return &TransactionLog{
		baseDir: absPath,
	}, nil
}

func (t *TransactionLog) LogTransaction(tx *Transaction) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	tx.CreatedAt = time.Now()
	tx.Status = TransactionStatusPending

	data, err := json.MarshalIndent(tx, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal transaction: %w", err)
	}

	filename := fmt.Sprintf("%s_%d.json", tx.ID, tx.CreatedAt.UnixNano())
	path := filepath.Join(t.baseDir, filename)

	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write transaction: %w", err)
	}

	return nil
}

func (t *TransactionLog) CommitTransaction(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	tx, err := t.findTransaction(id)
	if err != nil {
		return err
	}

	tx.Status = TransactionStatusCommitted
	tx.CompletedAt = time.Now()

	return t.updateTransaction(tx)
}

func (t *TransactionLog) RollbackTransaction(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	tx, err := t.findTransaction(id)
	if err != nil {
		return err
	}

	tx.Status = TransactionStatusRolledBack
	tx.CompletedAt = time.Now()

	return t.updateTransaction(tx)
}

func (t *TransactionLog) FailTransaction(id string, errMsg string) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	tx, err := t.findTransaction(id)
	if err != nil {
		return err
	}

	tx.Status = TransactionStatusFailed
	tx.ErrorMessage = errMsg
	tx.CompletedAt = time.Now()

	return t.updateTransaction(tx)
}

func (t *TransactionLog) GetTransaction(id string) (*Transaction, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.findTransaction(id)
}

func (t *TransactionLog) ListTransactions(workspaceID string, limit int) ([]*Transaction, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	entries, err := os.ReadDir(t.baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read transaction log: %w", err)
	}

	var transactions []*Transaction
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		path := filepath.Join(t.baseDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var tx Transaction
		if err := json.Unmarshal(data, &tx); err != nil {
			continue
		}

		if workspaceID != "" && tx.WorkspaceID != workspaceID {
			continue
		}

		transactions = append(transactions, &tx)
	}

	if limit > 0 && len(transactions) > limit {
		transactions = transactions[:limit]
	}

	return transactions, nil
}

func (t *TransactionLog) findTransaction(id string) (*Transaction, error) {
	entries, err := os.ReadDir(t.baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read transaction log: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		path := filepath.Join(t.baseDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var tx Transaction
		if err := json.Unmarshal(data, &tx); err != nil {
			continue
		}

		if tx.ID == id {
			return &tx, nil
		}
	}

	return nil, fmt.Errorf("transaction not found: %s", id)
}

func (t *TransactionLog) updateTransaction(tx *Transaction) error {
	entries, err := os.ReadDir(t.baseDir)
	if err != nil {
		return fmt.Errorf("failed to read transaction log: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}

		path := filepath.Join(t.baseDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		var existing Transaction
		if err := json.Unmarshal(data, &existing); err != nil {
			continue
		}

		if existing.ID == tx.ID {
			newData, err := json.MarshalIndent(tx, "", "  ")
			if err != nil {
				return fmt.Errorf("failed to marshal transaction: %w", err)
			}

			if err := os.WriteFile(path, newData, 0644); err != nil {
				return fmt.Errorf("failed to update transaction: %w", err)
			}

			return nil
		}
	}

	return fmt.Errorf("transaction not found: %s", tx.ID)
}

type TransactionalStore struct {
	store *StateStore
	log   *TransactionLog
	mu    sync.Mutex
}

func NewTransactionalStore(baseDir string) (*TransactionalStore, error) {
	store, err := NewStateStore(baseDir)
	if err != nil {
		return nil, err
	}

	log, err := NewTransactionLog(filepath.Join(baseDir, ".transactions"))
	if err != nil {
		return nil, err
	}

	return &TransactionalStore{
		store: store,
		log:   log,
	}, nil
}

func (ts *TransactionalStore) CreateWorkspace(w *types.Workspace) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	tx := &Transaction{
		ID:          fmt.Sprintf("tx_%d", time.Now().UnixNano()),
		Type:        TransactionCreate,
		WorkspaceID: w.ID,
	}

	if existing, _ := ts.store.GetWorkspace(w.ID); existing != nil {
		payload, _ := json.Marshal(w)
		tx.Payload = payload
		tx.Status = TransactionStatusFailed
		tx.ErrorMessage = ErrWorkspaceExists.Error()
		ts.log.LogTransaction(tx)
		return ErrWorkspaceExists
	}

	previousState, _ := json.Marshal((*types.Workspace)(nil))
	tx.PreviousState = previousState

	payload, _ := json.Marshal(w)
	tx.Payload = payload

	ts.log.LogTransaction(tx)

	if err := ts.store.SaveWorkspace(w); err != nil {
		ts.log.FailTransaction(tx.ID, err.Error())
		return err
	}

	ts.log.CommitTransaction(tx.ID)
	return nil
}

func (ts *TransactionalStore) UpdateWorkspace(w *types.Workspace) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	tx := &Transaction{
		ID:          fmt.Sprintf("tx_%d", time.Now().UnixNano()),
		Type:        TransactionUpdate,
		WorkspaceID: w.ID,
	}

	existing, err := ts.store.GetWorkspace(w.ID)
	if err != nil {
		tx.Status = TransactionStatusFailed
		tx.ErrorMessage = ErrWorkspaceNotFound.Error()
		ts.log.LogTransaction(tx)
		return ErrWorkspaceNotFound
	}

	previousState, _ := json.Marshal(existing)
	tx.PreviousState = previousState

	payload, _ := json.Marshal(w)
	tx.Payload = payload

	ts.log.LogTransaction(tx)

	if err := ts.store.SaveWorkspace(w); err != nil {
		ts.log.FailTransaction(tx.ID, err.Error())
		return err
	}

	ts.log.CommitTransaction(tx.ID)
	return nil
}

func (ts *TransactionalStore) DeleteWorkspace(id string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	tx := &Transaction{
		ID:          fmt.Sprintf("tx_%d", time.Now().UnixNano()),
		Type:        TransactionDelete,
		WorkspaceID: id,
	}

	existing, err := ts.store.GetWorkspace(id)
	if err != nil {
		tx.Status = TransactionStatusFailed
		tx.ErrorMessage = ErrWorkspaceNotFound.Error()
		ts.log.LogTransaction(tx)
		return ErrWorkspaceNotFound
	}

	previousState, _ := json.Marshal(existing)
	tx.PreviousState = previousState

	ts.log.LogTransaction(tx)

	if err := ts.store.DeleteWorkspace(id); err != nil {
		ts.log.FailTransaction(tx.ID, err.Error())
		return err
	}

	ts.log.CommitTransaction(tx.ID)
	return nil
}
