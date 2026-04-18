package workspace

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/inizio/nexus/packages/nexus/internal/domain/project"
	"github.com/inizio/nexus/packages/nexus/internal/domain/runtime"
	"github.com/inizio/nexus/packages/nexus/internal/domain/workspace"
)

// Service orchestrates workspace lifecycle operations.
type Service struct {
	repo        workspace.Repository
	projectRepo project.Repository
	driver      runtime.Driver
}

// NewService constructs a Service. driver may be nil if no runtime backend is available.
func NewService(repo workspace.Repository, projectRepo project.Repository, driver runtime.Driver) *Service {
	return &Service{repo: repo, projectRepo: projectRepo, driver: driver}
}

// Relations is a graph of workspaces grouped by repo.
type Relations struct {
	Groups []RelationGroup `json:"groups"`
}

// RelationGroup is a set of workspaces sharing a repo.
type RelationGroup struct {
	RepoID       string         `json:"repoId"`
	Repo         string         `json:"repo"`
	Nodes        []RelationNode `json:"nodes"`
	LineageRoots []string       `json:"lineageRoots"`
}

// RelationNode is a slim workspace view for relation graphs.
type RelationNode struct {
	WorkspaceID       string          `json:"workspaceId"`
	ParentWorkspaceID string          `json:"parentWorkspaceId,omitempty"`
	LineageRootID     string          `json:"lineageRootId,omitempty"`
	WorktreeRef       string          `json:"worktreeRef,omitempty"`
	State             workspace.State `json:"state"`
	Backend           string          `json:"backend,omitempty"`
	WorkspaceName     string          `json:"workspaceName"`
	CreatedAt         string          `json:"createdAt"`
	UpdatedAt         string          `json:"updatedAt"`
}

// ForkSpec describes parameters for forking a workspace.
type ForkSpec struct {
	ChildWorkspaceName string `json:"childWorkspaceName,omitempty"`
	ChildRef           string `json:"childRef"`
}

// CheckoutSpec describes parameters for checking out a new ref.
type CheckoutSpec struct {
	TargetRef string `json:"targetRef"`
}

func (s *Service) Info(ctx context.Context, id string) (*workspace.Workspace, error) {
	ws, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, workspace.ErrNotFound
	}
	return ws, nil
}

func (s *Service) List(ctx context.Context) ([]*workspace.Workspace, error) {
	all, err := s.repo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	sort.Slice(all, func(i, j int) bool {
		return all[i].CreatedAt.Before(all[j].CreatedAt)
	})
	return all, nil
}

func (s *Service) Create(ctx context.Context, spec workspace.CreateSpec) (*workspace.Workspace, error) {
	if spec.Repo == "" {
		return nil, fmt.Errorf("repo is required")
	}
	if spec.WorkspaceName == "" {
		return nil, fmt.Errorf("workspaceName is required")
	}
	if err := spec.Policy.Validate(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	id := fmt.Sprintf("ws-%d", now.UnixNano())
	ref := normalizeRef(spec.Ref)

	authBinding := spec.AuthBinding
	if authBinding == nil {
		authBinding = make(map[string]string)
	}

	ws := &workspace.Workspace{
		ID:            id,
		ProjectID:     spec.ProjectID,
		Repo:          spec.Repo,
		RepoID:        deriveRepoID(spec.Repo),
		Ref:           ref,
		WorkspaceName: spec.WorkspaceName,
		AgentProfile:  spec.AgentProfile,
		Policy:        spec.Policy,
		State:         workspace.StateCreated,
		Backend:       spec.Backend,
		AuthBinding:   authBinding,
		ConfigBundle:  spec.ConfigBundle,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	ws.LineageRootID = ws.ID

	if err := s.repo.Create(ctx, ws); err != nil {
		return nil, fmt.Errorf("persist workspace: %w", err)
	}

	if s.driver != nil {
		req := &runtime.CreateRequest{
			WorkspaceID:   ws.ID,
			WorkspaceName: ws.WorkspaceName,
			ProjectRoot:   ws.Repo,
			ConfigBundle:  spec.ConfigBundle,
		}
		if err := s.driver.Create(ctx, req); err != nil && !isAlreadyExists(err) {
			_ = s.repo.Delete(ctx, ws.ID)
			return nil, fmt.Errorf("runtime create: %w", err)
		}
	}

	return ws, nil
}

func (s *Service) Start(ctx context.Context, id string) (*workspace.Workspace, error) {
	ws, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, workspace.ErrNotFound
	}
	if ws.State == workspace.StateRemoved {
		return nil, fmt.Errorf("cannot start removed workspace: %s", id)
	}

	if s.driver != nil {
		if err := s.driver.Start(ctx, ws); err != nil {
			return nil, fmt.Errorf("runtime start: %w", err)
		}
	}

	ws.State = workspace.StateRunning
	ws.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, ws); err != nil {
		return nil, fmt.Errorf("persist start: %w", err)
	}
	return ws, nil
}

func (s *Service) Stop(ctx context.Context, id string) (*workspace.Workspace, error) {
	ws, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, workspace.ErrNotFound
	}
	if ws.State == workspace.StateRemoved {
		return nil, fmt.Errorf("cannot stop removed workspace: %s", id)
	}

	if s.driver != nil {
		if err := s.driver.Stop(ctx, ws); err != nil {
			return nil, fmt.Errorf("runtime stop: %w", err)
		}
	}

	ws.State = workspace.StateStopped
	ws.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, ws); err != nil {
		return nil, fmt.Errorf("persist stop: %w", err)
	}
	return ws, nil
}

func (s *Service) Remove(ctx context.Context, id string) error {
	ws, err := s.repo.Get(ctx, id)
	if err != nil {
		return workspace.ErrNotFound
	}

	if s.driver != nil {
		if err := s.driver.Destroy(ctx, ws); err != nil {
			_ = err
		}
	}

	return s.repo.Delete(ctx, id)
}

func (s *Service) Restore(ctx context.Context, id string) (*workspace.Workspace, error) {
	ws, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, workspace.ErrNotFound
	}
	if ws.State == workspace.StateRemoved {
		return nil, fmt.Errorf("cannot restore removed workspace: %s", id)
	}

	if s.driver != nil {
		snap := &runtime.Snapshot{WorkspaceID: ws.ID}
		if err := s.driver.Restore(ctx, ws, snap); err != nil {
			return nil, fmt.Errorf("runtime restore: %w", err)
		}
	}

	ws.State = workspace.StateRestored
	ws.UpdatedAt = time.Now().UTC()
	if err := s.repo.Update(ctx, ws); err != nil {
		return nil, fmt.Errorf("persist restore: %w", err)
	}
	return ws, nil
}

func (s *Service) Ready(ctx context.Context, id string) (bool, error) {
	ws, err := s.repo.Get(ctx, id)
	if err != nil {
		return false, workspace.ErrNotFound
	}
	return ws.State == workspace.StateRunning, nil
}

func (s *Service) SetLocalWorktree(ctx context.Context, id, path string) error {
	ws, err := s.repo.Get(ctx, id)
	if err != nil {
		return workspace.ErrNotFound
	}
	ws.UpdatedAt = time.Now().UTC()
	_ = path
	return s.repo.Update(ctx, ws)
}

func (s *Service) Relations(ctx context.Context, id string) (*Relations, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}

	groups := make(map[string]*RelationGroup)
	for _, ws := range all {
		repoID := ws.RepoID
		if repoID == "" {
			repoID = "repo-unknown"
		}
		if id != "" && ws.RepoID != id {
			continue
		}
		g, ok := groups[repoID]
		if !ok {
			g = &RelationGroup{RepoID: repoID, Repo: ws.Repo}
			groups[repoID] = g
		}
		g.Nodes = append(g.Nodes, RelationNode{
			WorkspaceID:       ws.ID,
			ParentWorkspaceID: ws.ParentWorkspaceID,
			LineageRootID:     ws.LineageRootID,
			WorktreeRef:       ws.Ref,
			State:             ws.State,
			Backend:           ws.Backend,
			WorkspaceName:     ws.WorkspaceName,
			CreatedAt:         ws.CreatedAt.UTC().Format(time.RFC3339),
			UpdatedAt:         ws.UpdatedAt.UTC().Format(time.RFC3339),
		})
	}

	result := &Relations{}
	for _, g := range groups {
		roots := make(map[string]struct{})
		for _, n := range g.Nodes {
			if n.LineageRootID != "" {
				roots[n.LineageRootID] = struct{}{}
			} else {
				roots[n.WorkspaceID] = struct{}{}
			}
		}
		for rootID := range roots {
			g.LineageRoots = append(g.LineageRoots, rootID)
		}
		sort.Strings(g.LineageRoots)
		result.Groups = append(result.Groups, *g)
	}
	sort.Slice(result.Groups, func(i, j int) bool {
		return result.Groups[i].RepoID < result.Groups[j].RepoID
	})
	return result, nil
}

func normalizeRef(ref string) string {
	return strings.TrimSpace(ref)
}

func deriveRepoID(repo string) string {
	repo = strings.TrimSpace(repo)
	repo = strings.TrimSuffix(repo, ".git")
	repo = strings.NewReplacer(":", "/", "@", "", "//", "/").Replace(repo)
	return repo
}

func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(err.Error(), "already exists")
}
