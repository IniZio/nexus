import WebSocket from 'ws';
import { WorkspaceClient } from '../client';

jest.mock('ws');

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

    const promise = client.workspace.create({
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
              state: 'setup',
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
    expect(ws.exec).toBeDefined();
    expect(ws.spotlight).toBeDefined();

    const execPromise = ws.exec.exec('pwd');
    let sentData2 = mockWsInstance.send.mock.calls[1][0] as string;
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

    const infoPromise = ws.info();
    sentData2 = mockWsInstance.send.mock.calls[2][0] as string;
    request2 = JSON.parse(sentData2);
    expect(request2.method).toBe('workspace.info');
    expect(request2.params.workspaceId).toBe('ws-1');

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request2.id,
          result: {
            workspace_id: 'ws-1',
            workspace_path: '/remote/ws-1',
            spotlight: [],
          },
        })
      )
    );

    const info = await infoPromise;
    expect(info.workspace_id).toBe('ws-1');

    const gitPromise = ws.git('status');
    sentData2 = mockWsInstance.send.mock.calls[3][0] as string;
    request2 = JSON.parse(sentData2);
    expect(request2.method).toBe('git.command');
    expect(request2.params.workspaceId).toBe('ws-1');
    expect(request2.params.action).toBe('status');
    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request2.id,
          result: { stdout: '## main', stderr: '', exit_code: 0 },
        })
      )
    );
    const gitResult = await gitPromise;
    expect((gitResult as { exit_code: number }).exit_code).toBe(0);

    const servicePromise = ws.service('status', { name: 'api' });
    sentData2 = mockWsInstance.send.mock.calls[4][0] as string;
    request2 = JSON.parse(sentData2);
    expect(request2.method).toBe('service.command');
    expect(request2.params.workspaceId).toBe('ws-1');
    expect(request2.params.action).toBe('status');
    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request2.id,
          result: { running: false },
        })
      )
    );
    const serviceResult = await servicePromise;
    expect((serviceResult as { running: boolean }).running).toBe(false);

    const readyPromise = ws.ready([{ name: 'api', command: 'sh', args: ['-lc', 'exit 0'] }], {
      timeoutMs: 500,
      intervalMs: 50,
    });
    sentData2 = mockWsInstance.send.mock.calls[5][0] as string;
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

    const profilePromise = ws.readyProfile('default-services', { timeoutMs: 300, intervalMs: 50 });
    sentData2 = mockWsInstance.send.mock.calls[6][0] as string;
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
});
