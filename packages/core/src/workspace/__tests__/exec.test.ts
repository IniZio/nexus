import { WorkspaceClient } from '../client';
import { ExecOperations } from '../exec';
import WebSocket from 'ws';

jest.mock('ws');

const MockedWebSocket = WebSocket as jest.MockedClass<typeof WebSocket>;

describe('ExecOperations', () => {
  let client: WorkspaceClient;
  let exec: ExecOperations;
  let mockWsInstance: jest.Mocked<WebSocket>;
  let eventHandlers: Map<string | symbol, ((...args: unknown[]) => void)[]>;

  beforeEach(() => {
    jest.clearAllMocks();
    eventHandlers = new Map();
    
    mockWsInstance = {
      send: jest.fn(),
      close: jest.fn(),
      on: jest.fn((event: string | symbol, handler: (...args: unknown[]) => void) => {
        const handlers = eventHandlers.get(event) || [];
        handlers.push(handler);
        eventHandlers.set(event, handlers);
        return mockWsInstance;
      }),
      removeListener: jest.fn(),
    } as unknown as jest.Mocked<WebSocket>;

    MockedWebSocket.mockImplementation(() => mockWsInstance);

    client = new WorkspaceClient({
      endpoint: 'ws://localhost:8080',
      workspaceId: 'test-workspace',
      token: 'test-token',
      reconnect: false,
    });

    exec = new ExecOperations(client);
  });

  const emitEvent = (event: string | symbol, ...args: unknown[]) => {
    const handlers = eventHandlers.get(event) || [];
    handlers.forEach(handler => handler(...args));
  };

  const connectClient = async () => {
    Object.defineProperty(mockWsInstance, 'readyState', {
      value: WebSocket.OPEN,
      writable: true,
    });
    const connectPromise = client.connect();
    emitEvent('open');
    await connectPromise;
  };

  describe('exec', () => {
    it('should execute command successfully', async () => {
      await connectClient();

      const promise = exec.exec('ls', ['-la']);
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.method).toBe('exec');
      expect(request.params.command).toBe('ls');
      expect(request.params.args).toEqual(['-la']);

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: {
          stdout: 'total 16\ndrwxr-xr-x  5 user  staff   160 Jan  1 00:00 file.txt',
          stderr: '',
          exit_code: 0,
        },
      })));

      const result = await promise;
      expect(result.stdout).toBe('total 16\ndrwxr-xr-x  5 user  staff   160 Jan  1 00:00 file.txt');
      expect(result.stderr).toBe('');
      expect(result.exitCode).toBe(0);
    });

    it('should execute command with options', async () => {
      await connectClient();

      const promise = exec.exec('npm', ['install'], {
        cwd: '/project',
        env: { NODE_ENV: 'test' },
        timeout: 60000,
      });
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.params.options).toEqual({
        cwd: '/project',
        env: { NODE_ENV: 'test' },
        timeout: 60000,
      });

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: {
          stdout: 'added 100 packages',
          stderr: '',
          exit_code: 0,
        },
      })));

      const result = await promise;
      expect(result.exitCode).toBe(0);
    });

    it('should handle non-zero exit code', async () => {
      await connectClient();

      const promise = exec.exec('ls', ['/nonexistent']);
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: {
          stdout: '',
          stderr: 'ls: /nonexistent: No such file or directory',
          exit_code: 1,
        },
      })));

      const result = await promise;
      expect(result.exitCode).toBe(1);
      expect(result.stderr).toBe('ls: /nonexistent: No such file or directory');
    });

    it('should handle stderr output', async () => {
      await connectClient();

      const promise = exec.exec('node', ['-e', 'console.error("error output")']);
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: {
          stdout: '',
          stderr: 'error output',
          exit_code: 0,
        },
      })));

      const result = await promise;
      expect(result.stderr).toBe('error output');
    });

    it('should handle command without args', async () => {
      await connectClient();

      const promise = exec.exec('pwd');
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.params.args).toEqual([]);

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: {
          stdout: '/home/user',
          stderr: '',
          exit_code: 0,
        },
      })));

      const result = await promise;
      expect(result.stdout).toBe('/home/user');
    });
  });
});
