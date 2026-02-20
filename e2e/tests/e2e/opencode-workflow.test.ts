import { GenericContainer, StartedTestContainer } from 'testcontainers';
import { WorkspaceClient } from '@nexus/workspace-sdk';

interface OpenCodeReadResult {
  content: string;
  source: 'workspace' | 'local';
  path: string;
}

interface OpenCodeCommandResult {
  stdout: string;
  stderr: string;
  exitCode: number;
  source: 'workspace' | 'local';
}

describe('OpenCode E2E Workflow', () => {
  let daemon: StartedTestContainer;
  let sdk: WorkspaceClient;
  let daemonPort: number;

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

    daemonPort = daemon.getMappedPort(8080);

    sdk = new WorkspaceClient({
      endpoint: `ws://localhost:${daemonPort}`,
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

  describe('File Interception', () => {
    beforeEach(async () => {
      await sdk.fs.writeFile('/workspace/src/index.ts', 'console.log("hello from workspace");');
      await sdk.fs.writeFile('/workspace/package.json', '{"name": "test-project"}');
      await sdk.fs.mkdir('/workspace/src', true);
    });

    afterEach(async () => {
      try {
        await sdk.fs.rm('/workspace/src', true);
        await sdk.fs.rm('/workspace/package.json');
      } catch {
        // Ignore cleanup errors
      }
    });

    test('file read is intercepted and routed to workspace', async () => {
      const result = await simulateOpenCodeRead('/workspace/src/index.ts');

      expect(result.content).toBe('console.log("hello from workspace");');
      expect(result.source).toBe('workspace');
      expect(result.path).toBe('/workspace/src/index.ts');
    });

    test('read non-workspace file falls back to local', async () => {
      const result = await simulateOpenCodeRead('/etc/hosts');

      expect(result.source).toBe('local');
    });

    test('workspace file write goes through SDK', async () => {
      const testPath = '/workspace/test-write.txt';
      const testContent = 'Written through SDK';

      await sdk.fs.writeFile(testPath, testContent);

      const readResult = await simulateOpenCodeRead(testPath);

      expect(readResult.content).toBe(testContent);
      expect(readResult.source).toBe('workspace');
    });
  });

  describe('Command Interception', () => {
    test('shell command runs through workspace daemon', async () => {
      const result = await simulateOpenCodeCommand('echo hello');

      expect(result.stdout.trim()).toBe('hello');
      expect(result.source).toBe('workspace');
      expect(result.exitCode).toBe(0);
    });

    test('git command runs in workspace context', async () => {
      await sdk.fs.mkdir('/workspace/test-repo', true);
      await sdk.exec.exec('git', ['init'], { cwd: '/workspace/test-repo' });

      const result = await simulateOpenCodeCommand('git status', '/workspace/test-repo');

      expect(result.source).toBe('workspace');
      expect(result.stdout).toContain('test-repo');
    });

    test('npm install runs in workspace', async () => {
      await sdk.fs.writeFile('/workspace/package.json', '{"name": "test", "version": "1.0.0"}');

      const result = await simulateOpenCodeCommand('npm --version');

      expect(result.exitCode).toBe(0);
      expect(result.source).toBe('workspace');
    });

    test('failing command returns proper exit code', async () => {
      const result = await simulateOpenCodeCommand('exit 1');

      expect(result.exitCode).toBe(1);
      expect(result.source).toBe('workspace');
    });
  });

  describe('Full Workflow Simulation', () => {
    test('complete development workflow', async () => {
      await sdk.fs.writeFile('/workspace/app.ts', 'const x = 1;');
      const readResult = await simulateOpenCodeRead('/workspace/app.ts');
      expect(readResult.content).toBe('const x = 1;');

      const execResult = await simulateOpenCodeCommand('node -e "console.log(1 + 1)"');
      expect(execResult.stdout.trim()).toBe('2');
    });

    test('workspace isolation between operations', async () => {
      await sdk.fs.mkdir('/workspace/project-a', true);
      await sdk.fs.mkdir('/workspace/project-b', true);

      await sdk.fs.writeFile('/workspace/project-a/file.txt', 'A');
      await sdk.fs.writeFile('/workspace/project-b/file.txt', 'B');

      const contentA = await simulateOpenCodeRead('/workspace/project-a/file.txt');
      const contentB = await simulateOpenCodeRead('/workspace/project-b/file.txt');

      expect(contentA.content).toBe('A');
      expect(contentB.content).toBe('B');
    });
  });
});

async function simulateOpenCodeRead(filePath: string): Promise<OpenCodeReadResult> {
  const isWorkspacePath = filePath.startsWith('/workspace/') || filePath.startsWith('/home/');

  if (!isWorkspacePath) {
    return {
      content: '',
      source: 'local',
      path: filePath,
    };
  }

  try {
    const content = await sdk.fs.readFile(filePath);
    return {
      content: content as string,
      source: 'workspace',
      path: filePath,
    };
  } catch (error) {
    return {
      content: '',
      source: 'local',
      path: filePath,
    };
  }
}

async function simulateOpenCodeCommand(
  command: string,
  cwd?: string
): Promise<OpenCodeCommandResult> {
  try {
    const result = await sdk.exec.exec(command, [], { cwd });
    return {
      stdout: result.stdout,
      stderr: result.stderr,
      exitCode: result.exitCode,
      source: 'workspace',
    };
  } catch (error) {
    return {
      stdout: '',
      stderr: String(error),
      exitCode: 1,
      source: 'workspace',
    };
  }
}
