import type {
  WorkspaceRecord,
  WorkspaceRelationsGroup,
  SpotlightForward,
  WorkspaceRelationsListResult,
  SpotlightListResult,
  SpotlightApplyDefaultsResult,
  SpotlightApplyComposePortsResult,
  WorkspaceCreateResult,
  WorkspaceForkResult,
  WorkspaceStartResult,
  WorkspaceStopResult,
  WorkspacePauseResult,
  WorkspaceResumeResult,
  WorkspaceRemoveResult,
  WorkspaceRestoreResult,
} from "@nexus/sdk";

type Pending = {
  resolve: (value: unknown) => void;
  reject: (reason: Error) => void;
};

type Subscription = (params: Record<string, unknown>) => void;

class NexusRPC {
  private ws: WebSocket | null = null;
  private req = 0;
  private pending = new Map<string, Pending>();
  private subscriptions = new Map<string, Set<Subscription>>();

  async connect(): Promise<void> {
    if (this.ws && this.ws.readyState === WebSocket.OPEN) {
      return;
    }

    const protocol = window.location.protocol === "https:" ? "wss:" : "ws:";
    const endpoint = `${protocol}//${window.location.host}/`;
    const url = new URL(endpoint);
    url.searchParams.set("workspaceId", "control");
    url.searchParams.set("token", window.localStorage.getItem("nexus.token") || "dev-token");

    await new Promise<void>((resolve, reject) => {
      this.ws = new WebSocket(url.toString());
      this.ws.onopen = () => resolve();
      this.ws.onmessage = (evt) => this.handleMessage(String(evt.data));
      this.ws.onerror = () => reject(new Error("websocket connection failed"));
      this.ws.onclose = () => {
        this.ws = null;
        this.pending.forEach(({ reject }) => reject(new Error("connection closed")));
        this.pending.clear();
      };
    });
  }

  async request<T>(method: string, params: Record<string, unknown> = {}): Promise<T> {
    await this.connect();
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) {
      throw new Error("not connected");
    }

    const id = `req-${Date.now()}-${++this.req}`;
    const payload = JSON.stringify({ jsonrpc: "2.0", id, method, params });

    return new Promise<T>((resolve, reject) => {
      this.pending.set(id, { resolve: resolve as (value: unknown) => void, reject });
      this.ws!.send(payload);
    });
  }

  private handleMessage(raw: string): void {
    let msg: {
      id?: string;
      method?: string;
      params?: Record<string, unknown>;
      result?: unknown;
      error?: { message?: string };
    };
    try {
      msg = JSON.parse(raw) as {
        id?: string;
        method?: string;
        params?: Record<string, unknown>;
        result?: unknown;
        error?: { message?: string };
      };
    } catch {
      return;
    }

    if (!msg.id && msg.method) {
      const handlers = this.subscriptions.get(msg.method);
      if (!handlers || !handlers.size) {
        return;
      }
      for (const handler of handlers) {
        handler(msg.params || {});
      }
      return;
    }

    if (!msg.id) {
      return;
    }
    const pending = this.pending.get(msg.id);
    if (!pending) {
      return;
    }
    this.pending.delete(msg.id);
    if (msg.error) {
      pending.reject(new Error(msg.error.message || "rpc error"));
      return;
    }
    pending.resolve(msg.result);
  }

  subscribe(method: string, handler: Subscription): () => void {
    const list = this.subscriptions.get(method) || new Set<Subscription>();
    list.add(handler);
    this.subscriptions.set(method, list);
    return () => {
      const current = this.subscriptions.get(method);
      if (!current) return;
      current.delete(handler);
      if (!current.size) {
        this.subscriptions.delete(method);
      }
    };
  }
}

const rpc = new NexusRPC();

export async function listWorkspaces(): Promise<WorkspaceRecord[]> {
  const res = await rpc.request<{ workspaces: WorkspaceRecord[] }>("workspace.list", {});
  return res.workspaces;
}

export async function listRelations(repoId?: string): Promise<WorkspaceRelationsGroup[]> {
  const res = await rpc.request<WorkspaceRelationsListResult>("workspace.relations.list", { repoId });
  return res.relations;
}

export async function createWorkspace(spec: {
  repo: string;
  ref?: string;
  workspaceName: string;
  agentProfile: string;
}): Promise<WorkspaceRecord> {
  const res = await rpc.request<WorkspaceCreateResult>("workspace.create", { spec });
  return res.workspace;
}

export async function forkWorkspace(id: string, childWorkspaceName: string, childRef?: string): Promise<boolean> {
  const res = await rpc.request<WorkspaceForkResult>("workspace.fork", {
    id,
    childWorkspaceName,
    ...(childRef ? { childRef } : {}),
  });
  return res.forked;
}

export async function pauseWorkspace(id: string): Promise<boolean> {
  const res = await rpc.request<WorkspacePauseResult>("workspace.pause", { id });
  return res.paused;
}

export async function resumeWorkspace(id: string): Promise<boolean> {
  const res = await rpc.request<WorkspaceResumeResult>("workspace.resume", { id });
  return res.resumed;
}

export async function startWorkspace(id: string): Promise<boolean> {
  const res = await rpc.request<WorkspaceStartResult>("workspace.start", { id });
  return res.started;
}

export async function stopWorkspace(id: string): Promise<boolean> {
  const res = await rpc.request<WorkspaceStopResult>("workspace.stop", { id });
  return res.stopped;
}

export async function applySpotlightDefaults(workspaceId: string): Promise<SpotlightForward[]> {
  const res = await rpc.request<SpotlightApplyDefaultsResult>("spotlight.applyDefaults", { workspaceId });
  return res.forwards;
}

export async function applySpotlightCompose(workspaceId: string): Promise<SpotlightForward[]> {
  const res = await rpc.request<SpotlightApplyComposePortsResult>("spotlight.applyComposePorts", { workspaceId });
  return res.forwards;
}

export async function listSpotlight(workspaceId: string): Promise<SpotlightForward[]> {
  const res = await rpc.request<SpotlightListResult>("spotlight.list", { workspaceId });
  return res.forwards;
}

export async function exposeSpotlight(spec: {
  workspaceId: string;
  service?: string;
  remotePort: number;
  localPort: number;
  host?: string;
}): Promise<SpotlightForward> {
  const res = await rpc.request<{ forward: SpotlightForward }>("spotlight.expose", {
    spec: {
      workspaceId: spec.workspaceId,
      service: spec.service || "",
      remotePort: spec.remotePort,
      localPort: spec.localPort,
      host: spec.host || "127.0.0.1",
    },
  });
  return res.forward;
}

export async function closeSpotlight(id: string): Promise<boolean> {
  const res = await rpc.request<{ closed: boolean }>("spotlight.close", { id });
  return res.closed;
}

export async function ptyOpen(workspaceId: string, cols: number, rows: number): Promise<string> {
  const res = await rpc.request<{ sessionId: string }>("pty.open", { workspaceId, cols, rows });
  return res.sessionId;
}

export async function ptyWrite(sessionId: string, data: string): Promise<void> {
  await rpc.request("pty.write", { sessionId, data });
}

export async function ptyResize(sessionId: string, cols: number, rows: number): Promise<void> {
  await rpc.request("pty.resize", { sessionId, cols, rows });
}

export async function ptyClose(sessionId: string): Promise<void> {
  await rpc.request("pty.close", { sessionId });
}

export function onPTYData(handler: (sessionId: string, data: string) => void): () => void {
  return rpc.subscribe("pty.data", (params) => {
    const sessionId = String(params.sessionId || "");
    const data = String(params.data || "");
    if (!sessionId || !data) {
      return;
    }
    handler(sessionId, data);
  });
}

export function onPTYExit(handler: (sessionId: string, exitCode: number) => void): () => void {
  return rpc.subscribe("pty.exit", (params) => {
    const sessionId = String(params.sessionId || "");
    const exitCode = Number(params.exitCode ?? 0);
    if (!sessionId) {
      return;
    }
    handler(sessionId, exitCode);
  });
}

export async function removeWorkspace(id: string): Promise<boolean> {
  const res = await rpc.request<WorkspaceRemoveResult>("workspace.remove", { id });
  return res.removed;
}

export async function restoreWorkspace(id: string): Promise<boolean> {
  const res = await rpc.request<WorkspaceRestoreResult>("workspace.restore", { id });
  return res.restored;
}

export async function setLocalWorktree(
  id: string,
  localWorktreePath: string,
  mutagenSessionId?: string,
): Promise<void> {
  await rpc.request<unknown>("workspace.setLocalWorktree", {
    id,
    localWorktreePath,
    ...(mutagenSessionId ? { mutagenSessionId } : {}),
  });
}
