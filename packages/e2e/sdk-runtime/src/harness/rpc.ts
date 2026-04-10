import { WorkspaceClient } from '@nexus/sdk';

export async function rpcRequest<T = unknown>(
  client: WorkspaceClient,
  method: string,
  params?: Record<string, unknown>
): Promise<T> {
  return client.request<T>(method, params);
}
