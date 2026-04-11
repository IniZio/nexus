export interface WorkspaceClientConfig {
  endpoint: string;
  workspaceId?: string;
  token: string;
  reconnect?: boolean;
  reconnectDelay?: number;
  maxReconnectAttempts?: number;
}

export interface RPCRequest {
  jsonrpc: '2.0';
  id: string;
  method: string;
  params?: Record<string, unknown>;
}

export interface RPCError {
  code: number;
  message: string;
  data?: unknown;
}

export interface RPCResponse {
  jsonrpc: '2.0';
  id: string;
  result?: unknown;
  error?: RPCError;
  method?: string;
  params?: unknown;
}

export interface DisconnectReason {
  code: number;
  reason: string;
}

export type ConnectionState = 'disconnected' | 'connecting' | 'connected' | 'reconnecting';

export type RequestHandler = (params?: Record<string, unknown>) => Promise<unknown>;
