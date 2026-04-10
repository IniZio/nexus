import { connectSDKClient, getDaemonEnvConfig } from '../harness/daemon';
import { assertCapabilitiesArray } from '../harness/assertions';

const hasDaemonEnv = (): boolean => getDaemonEnvConfig() !== null;

const runningInCI = (): boolean => process.env.CI === 'true';

const maybeIt = hasDaemonEnv() || runningInCI() ? it : it.skip;

describe('sdk-runtime e2e harness', () => {
  beforeAll(() => {
    if (!hasDaemonEnv() && runningInCI()) {
      throw new Error('Missing daemon env. Set NEXUS_DAEMON_WS and NEXUS_DAEMON_TOKEN in CI.');
    }
  });

  maybeIt('connects to daemon using @nexus/sdk', async () => {
    const client = await connectSDKClient();
    try {
      const caps = await client.workspace.capabilities();
      assertCapabilitiesArray(caps);
    } finally {
      await client.disconnect();
    }
  });
});
