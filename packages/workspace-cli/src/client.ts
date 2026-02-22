import axios, { AxiosInstance, AxiosRequestConfig } from 'axios';

export interface Workspace {
  id: string;
  name: string;
  display_name: string;
  status: WorkspaceStatus;
  backend: string;
  repository?: {
    url: string;
    provider?: string;
    local_path?: string;
  };
  branch?: string;
  ports?: PortMapping[];
  labels?: Record<string, string>;
  created_at: string;
  updated_at: string;
}

export type WorkspaceStatus = 'creating' | 'running' | 'sleeping' | 'stopped' | 'error';

export interface PortMapping {
  name: string;
  protocol: string;
  container_port: number;
  host_port: number;
  visibility: string;
  url?: string;
}

export interface CreateWorkspaceRequest {
  name: string;
  display_name?: string;
  repository_url?: string;
  branch?: string;
  config?: WorkspaceConfig;
  labels?: Record<string, string>;
}

export interface WorkspaceConfig {
  image?: string;
  devcontainer_path?: string;
  env?: Record<string, string>;
  volumes?: VolumeConfig[];
}

export interface VolumeConfig {
  type: string;
  source: string;
  target: string;
  read_only?: boolean;
}

export interface ExecRequest {
  command: string[];
}

export interface ExecResponse {
  output: string;
}

export interface APIResponse<T> {
  success: boolean;
  data?: T;
  error?: string;
}

export interface ListWorkspacesResponse {
  workspaces: Workspace[];
  total: number;
}

export class WorkspaceClient {
  private client: AxiosInstance;
  private baseURL: string;

  constructor(baseURL: string = 'http://localhost:8080', token?: string) {
    this.baseURL = baseURL;
    
    const config: AxiosRequestConfig = {
      baseURL,
      timeout: 30000,
      headers: {
        'Content-Type': 'application/json',
      },
    };

    if (token) {
      config.headers!['Authorization'] = `Bearer ${token}`;
    }

    this.client = axios.create(config);
  }

  setBaseURL(url: string): void {
    this.baseURL = url;
    this.client.defaults.baseURL = url;
  }

  setToken(token: string): void {
    this.client.defaults.headers['Authorization'] = `Bearer ${token}`;
  }

  async health(): Promise<{ status: string; time: string }> {
    const response = await this.client.get<APIResponse<{ status: string; time: string }>>('/health');
    if (!response.data.success) {
      throw new Error(response.data.error || 'Health check failed');
    }
    return response.data.data!;
  }

  async createWorkspace(req: CreateWorkspaceRequest): Promise<Workspace> {
    const response = await this.client.post<APIResponse<Workspace>>('/api/v1/workspaces', req);
    if (!response.data.success) {
      throw new Error(response.data.error || 'Failed to create workspace');
    }
    return response.data.data!;
  }

  async listWorkspaces(): Promise<ListWorkspacesResponse> {
    const response = await this.client.get<APIResponse<ListWorkspacesResponse>>('/api/v1/workspaces');
    if (!response.data.success) {
      throw new Error(response.data.error || 'Failed to list workspaces');
    }
    return response.data.data!;
  }

  async getWorkspace(id: string): Promise<Workspace> {
    const response = await this.client.get<APIResponse<Workspace>>(`/api/v1/workspaces/${id}`);
    if (!response.data.success) {
      throw new Error(response.data.error || 'Failed to get workspace');
    }
    return response.data.data!;
  }

  async startWorkspace(id: string): Promise<Workspace> {
    const response = await this.client.post<APIResponse<Workspace>>(`/api/v1/workspaces/${id}/start`);
    if (!response.data.success) {
      throw new Error(response.data.error || 'Failed to start workspace');
    }
    return response.data.data!;
  }

  async stopWorkspace(id: string, timeoutSeconds?: number): Promise<Workspace> {
    const response = await this.client.post<APIResponse<Workspace>>(
      `/api/v1/workspaces/${id}/stop`,
      timeoutSeconds ? { timeout_seconds: timeoutSeconds } : undefined
    );
    if (!response.data.success) {
      throw new Error(response.data.error || 'Failed to stop workspace');
    }
    return response.data.data!;
  }

  async deleteWorkspace(id: string): Promise<boolean> {
    const response = await this.client.delete<APIResponse<{ success: boolean }>>(`/api/v1/workspaces/${id}`);
    if (!response.data.success) {
      throw new Error(response.data.error || 'Failed to delete workspace');
    }
    return response.data.data?.success ?? false;
  }

  async exec(id: string, command: string[]): Promise<string> {
    const response = await this.client.post<APIResponse<ExecResponse>>(
      `/api/v1/workspaces/${id}/exec`,
      { command }
    );
    if (!response.data.success) {
      throw new Error(response.data.error || 'Failed to execute command');
    }
    return response.data.data?.output ?? '';
  }

  async getLogs(id: string, tail: number = 100): Promise<string> {
    const response = await this.client.get<APIResponse<{ logs: string }>>(
      `/api/v1/workspaces/${id}/logs`,
      { params: { tail } }
    );
    if (!response.data.success) {
      throw new Error(response.data.error || 'Failed to get logs');
    }
    return response.data.data?.logs ?? '';
  }
}
