export interface RPCClient {
  request<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T>;
  onNotification(method: string, callback: (params: unknown) => void): () => void;
}
