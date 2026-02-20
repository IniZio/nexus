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
  // eslint-disable-next-line no-console
  console.log('Starting daemon container...');
});

afterAll(async () => {
  // Cleanup is handled in individual tests
});
