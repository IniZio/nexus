package driver

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	bolt "go.etcd.io/bbolt"
)

type SandboxPhase string

const (
	PhaseProvisioning SandboxPhase = "Provisioning"
	PhaseReady        SandboxPhase = "Ready"
	PhaseStopped      SandboxPhase = "Stopped"
	PhaseDeleting     SandboxPhase = "Deleting"
	PhaseFailed       SandboxPhase = "Failed"
)

type WorkloadIdentityRecord struct {
	User  string `json:"user,omitempty"`
	Group string `json:"group,omitempty"`
}

type SandboxRecord struct {
	ID                   string                 `json:"id"`
	Name                 string                 `json:"name"`
	Workspace            string                 `json:"workspace,omitempty"`
	Phase                SandboxPhase           `json:"phase"`
	CreatedAt            time.Time              `json:"created_at"`
	UpdatedAt            time.Time              `json:"updated_at"`
	Spec                 SandboxSpec            `json:"spec"`
	RuntimeID            RuntimeIdentityRecord  `json:"runtime_id,omitempty"`
	LaunchAuthentication []byte                 `json:"launch_authentication,omitempty"`
	WorkloadIdentity     WorkloadIdentityRecord `json:"workload_identity,omitempty"`
}

type SandboxSpec struct {
	Image    string            `json:"image"`
	Command  []string          `json:"command,omitempty"`
	TTY      bool              `json:"tty,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Mounts   []MountRecord     `json:"mounts,omitempty"`
	Token    string            `json:"token,omitempty"`
	LogLevel string            `json:"log_level,omitempty"`
}

type MountRecord struct {
	HostPath  string `json:"host_path"`
	GuestPath string `json:"guest_path"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

type RuntimeIdentityRecord struct {
	PID         int    `json:"pid,omitempty"`
	APISocket   string `json:"api_socket,omitempty"`
	VsockSocket string `json:"vsock_socket,omitempty"`
	StateDir    string `json:"state_dir,omitempty"`
}

type Store interface {
	Put(r SandboxRecord) error
	Get(id string) (SandboxRecord, bool)
	GetByName(name string) (SandboxRecord, bool)
	List() []SandboxRecord
	Delete(id string) bool
}

var sandboxBucket = []byte("sandboxes")

type BboltStore struct {
	db *bolt.DB
}

func NewBboltStore(path string) (*BboltStore, error) {
	db, err := bolt.Open(path, 0600, nil)
	if err != nil {
		return nil, fmt.Errorf("bbolt open %s: %w", path, err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(sandboxBucket)
		return err
	}); err != nil {
		db.Close()
		return nil, err
	}
	return &BboltStore{db: db}, nil
}

func (s *BboltStore) Close() error { return s.db.Close() }

func (s *BboltStore) Put(r SandboxRecord) error {
	r.UpdatedAt = time.Now()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = r.UpdatedAt
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(sandboxBucket).Put([]byte(r.ID), data)
	})
}

func (s *BboltStore) Get(id string) (SandboxRecord, bool) {
	var r SandboxRecord
	_ = s.db.View(func(tx *bolt.Tx) error {
		v := tx.Bucket(sandboxBucket).Get([]byte(id))
		if v == nil {
			return nil
		}
		return json.Unmarshal(v, &r)
	})
	return r, r.ID != ""
}

func (s *BboltStore) GetByName(name string) (SandboxRecord, bool) {
	var found SandboxRecord
	_ = s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(sandboxBucket).ForEach(func(_, v []byte) error {
			var r SandboxRecord
			if json.Unmarshal(v, &r) == nil && r.Name == name {
				found = r
			}
			return nil
		})
	})
	return found, found.ID != ""
}

func (s *BboltStore) List() []SandboxRecord {
	var out []SandboxRecord
	_ = s.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(sandboxBucket).ForEach(func(_, v []byte) error {
			var r SandboxRecord
			if json.Unmarshal(v, &r) == nil {
				out = append(out, r)
			}
			return nil
		})
	})
	return out
}

func (s *BboltStore) Delete(id string) bool {
	var deleted bool
	_ = s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(sandboxBucket)
		if b.Get([]byte(id)) != nil {
			deleted = true
			return b.Delete([]byte(id))
		}
		return nil
	})
	return deleted
}

type MemStore struct {
	mu  sync.RWMutex
	sbs map[string]SandboxRecord
}

func NewMemStore() *MemStore {
	return &MemStore{sbs: make(map[string]SandboxRecord)}
}

func (s *MemStore) Put(r SandboxRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	r.UpdatedAt = now
	s.sbs[r.ID] = r
	return nil
}

func (s *MemStore) Get(id string) (SandboxRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.sbs[id]
	return r, ok
}

func (s *MemStore) GetByName(name string) (SandboxRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.sbs {
		if r.Name == name {
			return r, true
		}
	}
	return SandboxRecord{}, false
}

func (s *MemStore) List() []SandboxRecord {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SandboxRecord, 0, len(s.sbs))
	for _, r := range s.sbs {
		out = append(out, r)
	}
	return out
}

func (s *MemStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sbs[id]
	delete(s.sbs, id)
	return ok
}
