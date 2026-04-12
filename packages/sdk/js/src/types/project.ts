export interface Project {
  id: string;
  name: string;
  primaryRepo: string;
  repoIds: string[];
  rootPath: string;
  createdAt: string;
  updatedAt: string;
}

export interface ProjectListResult {
  projects: Project[];
}

export interface ProjectWithWorkspaces extends Project {
  workspaces: import('./workspace').WorkspaceRecord[];
}

export interface ProjectGetResult {
  project: Project;
  workspaces?: import('./workspace').WorkspaceRecord[];
}

export interface ProjectRemoveResult {
  removed: boolean;
}
