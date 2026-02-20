import { GenericContainer, StartedTestContainer, DockerodeContainer } from 'testcontainers';
import { WorkspaceClient } from '@nexus/workspace-sdk';

describe('SDK-Daemon Integration', () => {
  let daemon: StartedTestContainer;
  let sdk: WorkspaceClient;

  beforeAll(async () => {
    daemon = await new GenericContainer('nexus-workspace-daemon:test')
      .withExposedPorts({ container: 8080, host: 8080 })
      .withEnvironment({
        PORT: '8080',
        WORKSPACE_DIR: '/workspace',
        TOKEN: 'test-token',
      })
      .withStartupTimeout(60000)
      .start();

    const port = daemon.getMappedPort(8080);
    sdk = new WorkspaceClient({
      endpoint: `ws://localhost:${port}`,
      workspaceId: 'test-workspace',
      token: 'test-token',
      reconnect: false,
    });

    await sdk.connect();
  }, 90000);

  afterAll(async () => {
    if (sdk) {
      await sdk.disconnect();
    }
    if (daemon) {
      await daemon.stop();
    }
  });

  describe('File Operations', () => {
    const testFilePath = '/workspace/test-file.txt';
    const testContent = 'Hello from integration tests!';

    test('write and read file', async () => {
      await sdk.fs.writeFile(testFilePath, testContent);
      const content = await sdk.fs.readFile(testFilePath);
      expect(content).toBe(testContent);
    });

    test('check file exists', async () => {
      const exists = await sdk.fs.exists(testFilePath);
      expect(exists).toBe(true);

      const nonExists = await sdk.fs.exists('/workspace/non-existent.txt');
      expect(nonExists).toBe(false);
    });

    test('create directory and list contents', async () => {
      const testDir = '/workspace/test-dir';

      await sdk.fs.mkdir(testDir, true);

      const exists = await sdk.fs.exists(testDir);
      expect(exists).toBe(true);

      await sdk.fs.writeFile(`${testDir}/file.txt`, 'content');

      const entries = await sdk.fs.readdir(testDir);
      expect(entries).toContain('file.txt');
    });

    test('delete file', async () => {
      const fileToDelete = '/workspace/to-delete.txt';

      await sdk.fs.writeFile(fileToDelete, 'delete me');

      let exists = await sdk.fs.exists(fileToDelete);
      expect(exists).toBe(true);

      await sdk.fs.rm(fileToDelete);

      exists = await sdk.fs.exists(fileToDelete);
      expect(exists).toBe(false);
    });

    test('get file stats', async () => {
      await sdk.fs.writeFile(testFilePath, testContent);

      const stats = await sdk.fs.stat(testFilePath);

      expect(stats.isFile).toBe(true);
      expect(stats.isDirectory).toBe(false);
      expect(stats.size).toBeGreaterThan(0);
    });
  });

  describe('Command Execution', () => {
    test('run echo command', async () => {
      const result = await sdk.exec.exec('echo', ['hello world']);

      expect(result.stdout.trim()).toBe('hello world');
      expect(result.exitCode).toBe(0);
    });

    test('run pwd command', async () => {
      const result = await sdk.exec.exec('pwd');

      expect(result.stdout.trim()).toBe('/workspace');
      expect(result.exitCode).toBe(0);
    });

    test('run failing command', async () => {
      const result = await sdk.exec.exec('exit', ['1']);

      expect(result.exitCode).toBe(1);
    });

    test('capture stderr', async () => {
      const result = await sdk.exec.exec('node', ['-e', 'console.error("error output")']);

      expect(result.stderr.trim()).toBe('error output');
    });

    test('run command with custom working directory', async () => {
      await sdk.fs.mkdir('/workspace/subdir', true);

      const result = await sdk.exec.exec('pwd', [], { cwd: '/workspace/subdir' });

      expect(result.stdout.trim()).toBe('/workspace/subdir');
    });
  });

  describe('Connection Management', () => {
    test('check connection state', () => {
      expect(sdk.isConnected).toBe(true);
    });

    test('disconnect and reconnect', async () => {
      await sdk.disconnect();
      expect(sdk.isConnected).toBe(false);

      await sdk.connect();
      expect(sdk.isConnected).toBe(true);
    });
  });
});
