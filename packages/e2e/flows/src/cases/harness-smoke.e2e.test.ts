import { connectSDKClient, getDaemonEnvConfig } from '../harness/daemon';
import { startSession } from '../harness/session';
import { assertCapabilitiesArray, e2eStrictRuntime, skipTest } from '../harness/assertions';

const hasDaemonEnv = (): boolean => getDaemonEnvConfig() !== null;

const runningInCI = (): boolean => process.env.CI === 'true';

const harnessIt = hasDaemonEnv() || runningInCI() || e2eStrictRuntime() ? it : it.skip;

describe('flows e2e harness', () => {
  harnessIt('connects to daemon using @nexus/sdk', async () => {
    const env = getDaemonEnvConfig();
    if (env) {
      const client = await connectSDKClient(env);
      try {
        const caps = await client.workspaces.capabilities();
        assertCapabilitiesArray(caps);
      } finally {
        await client.disconnect();
      }
      return;
    }

    if (!runningInCI()) {
      if (e2eStrictRuntime()) {
        throw new Error(
          'E2E strict: set NEXUS_DAEMON_WS and NEXUS_DAEMON_TOKEN, or run with CI=true for a managed daemon'
        );
      }
      skipTest('daemon env not configured (NEXUS_DAEMON_WS/NEXUS_DAEMON_TOKEN); skipping harness connectivity check');
      return;
    }

    const session = await startSession({ forceManaged: true });
    try {
      const caps = await session.client.workspaces.capabilities();
      assertCapabilitiesArray(caps);
    } finally {
      await session.stop();
    }
  });
});
