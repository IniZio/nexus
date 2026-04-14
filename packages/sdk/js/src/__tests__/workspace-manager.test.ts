import WebSocket from 'ws';
import { WorkspaceClient } from '../client';

jest.mock('ws');
jest.mock('../bundle', () => ({ buildConfigBundle: jest.fn().mockReturnValue('') }));

const MockedWebSocket = WebSocket as jest.MockedClass<typeof WebSocket>;

describe('WorkspaceManager', () => {
  let client: WorkspaceClient;
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
      workspaceId: 'control',
      token: 'test-token',
      reconnect: false,
    });
  });

  const emitEvent = (event: string | symbol, ...args: unknown[]) => {
    const handlers = eventHandlers.get(event) || [];
    handlers.forEach((handler) => handler(...args));
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

  it('creates workspace and returns handle', async () => {
    await connectClient();

    const promise = client.workspaces.create({
      repo: '<internal-repo-url>',
      ref: 'main',
      workspaceName: 'alpha',
      agentProfile: 'default',
    });

    const sentData = mockWsInstance.send.mock.calls[0][0] as string;
    const request = JSON.parse(sentData);
    expect(request.method).toBe('workspace.create');

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request.id,
          result: {
            workspace: {
              id: 'ws-1',
              repo: '<internal-repo-url>',
              ref: 'main',
              workspaceName: 'alpha',
              agentProfile: 'default',
              backend: 'dind',
              authBinding: { github: 'newman' },
              state: 'created',
              rootPath: '/remote/ws-1',
              createdAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
            },
          },
        })
      )
    );

    const ws = await promise;
    expect(ws.id).toBe('ws-1');
    const fsReadPromise = ws.readFile('/workspace/README.md', 'utf8');
    let fsReadData = mockWsInstance.send.mock.calls[1][0] as string;
    let fsReadReq = JSON.parse(fsReadData);
    expect(fsReadReq.method).toBe('fs.readFile');
    expect(fsReadReq.params.workspaceId).toBe('ws-1');

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: fsReadReq.id,
          result: { content: 'hello', encoding: 'utf8' },
        })
      )
    );

    await expect(fsReadPromise).resolves.toBe('hello');

    const execPromise = ws.exec('pwd');
    let sentData2 = mockWsInstance.send.mock.calls[2][0] as string;
    let request2 = JSON.parse(sentData2);
    expect(request2.method).toBe('exec');
    expect(request2.params.workspaceId).toBe('ws-1');

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request2.id,
          result: { stdout: '/remote/ws-1', stderr: '', exit_code: 0 },
        })
      )
    );

    await execPromise;

    const readyPromise = ws.ready([{ name: 'api', command: 'sh', args: ['-lc', 'exit 0'] }], {
      timeoutMs: 500,
      intervalMs: 50,
    });
    sentData2 = mockWsInstance.send.mock.calls[3][0] as string;
    request2 = JSON.parse(sentData2);
    expect(request2.method).toBe('workspace.ready');
    expect(request2.params.workspaceId).toBe('ws-1');

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request2.id,
          result: {
            ready: true,
            workspaceId: 'ws-1',
            elapsedMs: 12,
            attempts: 1,
            lastResults: { api: 0 },
          },
        })
      )
    );

    const readyRes = await readyPromise;
    expect(readyRes.ready).toBe(true);

    const profilePromise = ws.ready('default-services', { timeoutMs: 300, intervalMs: 50 });
    sentData2 = mockWsInstance.send.mock.calls[4][0] as string;
    request2 = JSON.parse(sentData2);
    expect(request2.method).toBe('workspace.ready');
    expect(request2.params.profile).toBe('default-services');

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request2.id,
          result: {
            ready: true,
            workspaceId: 'ws-1',
            profile: 'default-services',
            elapsedMs: 20,
            attempts: 1,
            lastResults: { 'student-portal': 0, api: 0, 'opencode-acp': 0 },
          },
        })
      )
    );

    const profileRes = await profilePromise;
    expect(profileRes.profile).toBe('default-services');
    expect(profileRes.ready).toBe(true);
  });

  it('stops workspace and returns boolean', async () => {
    await connectClient();

    const promise = client.workspaces.stop('ws-1');

    const sentData = mockWsInstance.send.mock.calls[0][0] as string;
    const request = JSON.parse(sentData);
    expect(request.method).toBe('workspace.stop');
    expect(request.params).toEqual({ id: 'ws-1' });

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request.id,
          result: { stopped: true },
        })
      )
    );

    const result = await promise;
    expect(result).toBe(true);
  });

  it('supports lifecycle via workspace manager', async () => {
    await connectClient();

    const startPromise = client.workspaces.start('ws-1');
    let sentData = mockWsInstance.send.mock.calls[0][0] as string;
    let request = JSON.parse(sentData);
    expect(request.method).toBe('workspace.start');
    expect(request.params).toEqual({ id: 'ws-1' });

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request.id,
          result: {
            workspace: {
              id: 'ws-1',
              repo: '<internal-repo-url>',
              ref: 'main',
              workspaceName: 'alpha',
              agentProfile: 'default',
              backend: 'firecracker',
              state: 'created',
              rootPath: '/remote/ws-1',
              createdAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
            },
          },
        })
      )
    );

    await startPromise;

    const stopPromise = client.workspaces.stop('ws-1');
    sentData = mockWsInstance.send.mock.calls[1][0] as string;
    request = JSON.parse(sentData);
    expect(request.method).toBe('workspace.stop');
    expect(request.params).toEqual({ id: 'ws-1' });
    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request.id,
          result: { stopped: true },
        })
      )
    );
    await expect(stopPromise).resolves.toBe(true);

    const removePromise = client.workspaces.remove('ws-1');
    sentData = mockWsInstance.send.mock.calls[2][0] as string;
    request = JSON.parse(sentData);
    expect(request.method).toBe('workspace.remove');
    expect(request.params).toEqual({ id: 'ws-1' });
    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request.id,
          result: { removed: true },
        })
      )
    );
    await expect(removePromise).resolves.toBe(true);
  });

  it('restores workspace and returns handle', async () => {
    await connectClient();

    const promise = client.workspaces.restore('ws-1');

    const sentData = mockWsInstance.send.mock.calls[0][0] as string;
    const request = JSON.parse(sentData);
    expect(request.method).toBe('workspace.restore');
    expect(request.params).toEqual({ id: 'ws-1' });

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request.id,
          result: {
            restored: true,
            workspace: {
              id: 'ws-1',
              repo: '<internal-repo-url>',
              ref: 'main',
              workspaceName: 'alpha',
              agentProfile: 'default',
              backend: 'dind',
              state: 'restored',
              rootPath: '/remote/ws-1',
              createdAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
            },
          },
        })
      )
    );

    const ws = await promise;
    expect(ws.id).toBe('ws-1');
    expect(ws.state).toBe('restored');
  });

  it('forks workspace and returns child handle', async () => {
    await connectClient();

    const forkPromise = client.workspaces.fork('ws-1', 'alpha-child', 'alpha-child');
    const sent = mockWsInstance.send.mock.calls[0][0] as string;
    const req = JSON.parse(sent);
    expect(req.method).toBe('workspace.fork');
    expect(req.params).toEqual({ id: 'ws-1', childWorkspaceName: 'alpha-child', childRef: 'alpha-child' });

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: req.id,
          result: {
            forked: true,
            workspace: {
              id: 'ws-2',
              repo: '<internal-repo-url>',
              ref: 'main',
              workspaceName: 'alpha-child',
              agentProfile: 'default',
              backend: 'firecracker',
              parentWorkspaceId: 'ws-1',
              state: 'created',
              rootPath: '/remote/ws-2',
              createdAt: new Date().toISOString(),
              updatedAt: new Date().toISOString(),
            },
          },
        })
      )
    );

    const child = await forkPromise;
    expect(child.id).toBe('ws-2');
    expect(child.rootPath).toBe('/remote/ws-2');
  });
});
