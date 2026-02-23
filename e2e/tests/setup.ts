// DEPRECATED: Test setup is disabled
// The workspace-sdk and workspace-daemon packages have been deleted.
// This setup file is preserved for reference when rewriting tests.
//
// To re-enable tests:
// 1. Rewrite to use nexus CLI instead of testcontainers
// 2. Start nexusd daemon directly: cd packages/nexusd && go run ./cmd/daemon
// 3. Use nexus workspace commands for testing

// Placeholder to satisfy TypeScript
export {};

/*
// ORIGINAL SETUP (preserved for reference):

import { GenericContainer, StartedTestContainer } from 'testcontainers';

jest.setTimeout(60000);

declare global {
  let daemonContainer: StartedTestContainer | null;
}

export async function startDaemonContainer(): Promise<StartedTestContainer> {
  const container = new GenericContainer('nexus-workspace-daemon:test')
    .withExposedPorts({ container: 8080, host: 8080 })
    .withEnvironment({
      PORT: '8080',
      WORKSPACE_DIR: '/workspace',
      TOKEN: 'test-token',
    })
    .withStartupTimeout(60000);

  return container.start();
}

export async function stopDaemonContainer(container: StartedTestContainer): Promise<void> {
  await container.stop();
}

beforeAll(async () => {
  console.log('Starting daemon container...');
});

afterAll(async () => {
  // Cleanup is handled in individual tests
});
*/
