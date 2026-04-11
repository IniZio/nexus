import WebSocket from 'ws';
import { WorkspaceClient } from '../client';

jest.mock('ws');

const MockedWebSocket = WebSocket as jest.MockedClass<typeof WebSocket>;

describe('Spotlight on WorkspaceHandle', () => {
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

  async function createHandle() {
    const promise = client.workspaces.create({
      repo: '<internal-repo-url>',
      workspaceName: 'alpha',
      agentProfile: 'default',
    });

    const sentData = mockWsInstance.send.mock.calls[0][0] as string;
    const request = JSON.parse(sentData);
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

    return promise;
  }

  it('exposes and lists spotlight mappings', async () => {
    await connectClient();
    const handle = await createHandle();

    const startPromise = handle.tunnel.start({
      service: 'student-portal',
      remotePort: 5173,
      localPort: 5173,
    });

    let sentData = mockWsInstance.send.mock.calls[1][0] as string;
    let request = JSON.parse(sentData);
    expect(request.method).toBe('spotlight.expose');

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request.id,
          result: {
            forward: {
              id: 'spot-1',
              workspaceId: 'ws-1',
              service: 'student-portal',
              remotePort: 5173,
              localPort: 5173,
              host: '127.0.0.1',
              createdAt: new Date().toISOString(),
            },
          },
        })
      )
    );

    const fwd = await startPromise;
    expect(fwd.id).toBe('spot-1');

    const listPromise = handle.tunnel.list();
    sentData = mockWsInstance.send.mock.calls[2][0] as string;
    request = JSON.parse(sentData);
    expect(request.method).toBe('spotlight.list');

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request.id,
          result: {
            forwards: [
              {
                id: 'spot-1',
                workspaceId: 'ws-1',
                service: 'student-portal',
                remotePort: 5173,
                localPort: 5173,
                host: '127.0.0.1',
                createdAt: new Date().toISOString(),
              },
            ],
          },
        })
      )
    );

    const all = await listPromise;
    expect(all.forwards.some((x) => x.id === fwd.id)).toBe(true);

    const applyPromise = handle.tunnel.applyDefaults();
    sentData = mockWsInstance.send.mock.calls[3][0] as string;
    request = JSON.parse(sentData);
    expect(request.method).toBe('spotlight.applyDefaults');

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request.id,
          result: {
            forwards: [
              {
                id: 'spot-2',
                workspaceId: 'ws-1',
                service: 'api',
                remotePort: 8000,
                localPort: 8000,
                host: '127.0.0.1',
                createdAt: new Date().toISOString(),
              },
            ],
          },
        })
      )
    );

    const applied = await applyPromise;
    expect(applied.forwards.length).toBe(1);

    const applyComposePromise = handle.tunnel.applyComposePorts();
    sentData = mockWsInstance.send.mock.calls[4][0] as string;
    request = JSON.parse(sentData);
    expect(request.method).toBe('spotlight.applyComposePorts');

    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request.id,
          result: {
            forwards: [
              {
                id: 'spot-3',
                workspaceId: 'ws-1',
                service: 'student',
                remotePort: 5173,
                localPort: 5173,
                host: '127.0.0.1',
                createdAt: new Date().toISOString(),
              },
            ],
            errors: [
              {
                service: 'api',
                hostPort: 8000,
                targetPort: 8000,
                message: 'local port 8000 already in use',
              },
            ],
          },
        })
      )
    );

    const composeApplied = await applyComposePromise;
    expect(composeApplied.forwards.length).toBe(1);
    expect(composeApplied.errors.length).toBe(1);

    const stopPromise = fwd.stop();
    sentData = mockWsInstance.send.mock.calls[5][0] as string;
    request = JSON.parse(sentData);
    expect(request.method).toBe('spotlight.close');
    expect(request.params.id).toBe('spot-1');
    emitEvent(
      'message',
      Buffer.from(
        JSON.stringify({
          jsonrpc: '2.0',
          id: request.id,
          result: { closed: true },
        })
      )
    );
    await expect(stopPromise).resolves.toBe(true);
  });
});
