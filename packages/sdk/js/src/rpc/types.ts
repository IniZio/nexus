import type { RPCSchema } from './schema';

export interface RPCClient {
  request<M extends keyof RPCSchema>(method: M, params: RPCSchema[M][0]): Promise<RPCSchema[M][1]>;
  request<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T>;
  onNotification(method: string, callback: (params: unknown) => void): () => void;
}
