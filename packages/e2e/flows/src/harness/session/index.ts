import { WorkspaceClient } from '@nexus/sdk';
import {
  connectSDKClient,
  getDaemonEnvConfig,
  startManagedDaemon,
  type ManagedDaemon,
  type ManagedDaemonOptions,
} from '../daemon';

export { rpcRequest } from './rpc';

export type DaemonSession = {
  client: WorkspaceClient;
  managed: boolean;
  stop: () => Promise<void>;
};

export type SessionOptions = {
  forceManaged?: boolean;
  managed?: ManagedDaemonOptions;
};

export async function startSession(options: SessionOptions = {}): Promise<DaemonSession> {
  if (!options.forceManaged) {
    const env = getDaemonEnvConfig();
    if (env) {
      const client = await connectSDKClient(env);
      return {
        client,
        managed: false,
        stop: async () => {
          await client.disconnect();
        },
      };
    }
  }

  const managedDaemon = await startManagedDaemon(options.managed);
  try {
    return await connectManagedSession(managedDaemon);
  } catch (error) {
    await managedDaemon.stop();
    throw error;
  }
}

async function connectManagedSession(managedDaemon: ManagedDaemon): Promise<DaemonSession> {
  const client = await connectSDKClient({ endpoint: managedDaemon.endpoint, token: managedDaemon.token });
  return {
    client,
    managed: true,
    stop: async () => {
      await client.disconnect();
      await managedDaemon.stop();
    },
  };
}
