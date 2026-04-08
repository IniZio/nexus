import { WorkspaceClient } from '../client';
import WebSocket from 'ws';

jest.mock('ws');

const MockedWebSocket = WebSocket as jest.MockedClass<typeof WebSocket>;

describe('WorkspaceClient', () => {
  let client: WorkspaceClient;
  let mockWsInstance: jest.Mocked<WebSocket>;
  let eventHandlers: Map<string | symbol, ((...args: unknown[]) => void)[]>;

  beforeEach(() => {
    jest.clearAllMocks();
    eventHandlers = new Map();
    
    mockWsInstance = {
      send: jest.fn(),
      close: jest.fn((code: number, reason: string) => {
        emitEvent('close', code, Buffer.from(reason));
      }),
      on: jest.fn((event: string | symbol, handler: (...args: unknown[]) => void) => {
        const handlers = eventHandlers.get(event) || [];
        handlers.push(handler);
        eventHandlers.set(event, handlers);
        return mockWsInstance;
      }),
      removeListener: jest.fn(),
    } as unknown as jest.Mocked<WebSocket>;

    MockedWebSocket.mockImplementation(() => mockWsInstance);
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

  describe('constructor', () => {
    it('should create a client with default config', () => {
      client = new WorkspaceClient({
        endpoint: 'ws://localhost:8080',
        workspaceId: 'test-workspace',
        token: 'test-token',
      });

      expect(client.isConnected).toBe(false);
      expect(client.connectionState).toBe('disconnected');
      expect(client.fs).toBeDefined();
      expect(client.exec).toBeDefined();
      expect(client.spotlight).toBeDefined();
      expect(client.workspace).toBeDefined();
    });

    it('should create a client with custom reconnect config', () => {
      client = new WorkspaceClient({
        endpoint: 'ws://localhost:8080',
        workspaceId: 'test-workspace',
        token: 'test-token',
        reconnect: false,
        reconnectDelay: 500,
        maxReconnectAttempts: 5,
      });

      expect(client.isConnected).toBe(false);
    });

    it('should create control-plane client without workspaceId', () => {
      client = new WorkspaceClient({
        endpoint: 'ws://localhost:8080',
        token: 'test-token',
      });

      expect(client.isConnected).toBe(false);
      expect(client.workspace).toBeDefined();
    });
  });

  describe('connect', () => {
    it('should connect successfully', async () => {
      client = new WorkspaceClient({
        endpoint: 'ws://localhost:8080',
        workspaceId: 'test-workspace',
        token: 'test-token',
      });

      Object.defineProperty(mockWsInstance, 'readyState', {
        value: WebSocket.OPEN,
        writable: true,
      });

      const connectPromise = client.connect();
      emitEvent('open');
      
      await connectPromise;
      
      expect(client.isConnected).toBe(true);
      expect(client.connectionState).toBe('connected');
    });

    it('should throw error on connection failure', async () => {
      client = new WorkspaceClient({
        endpoint: 'ws://localhost:8080',
        workspaceId: 'test-workspace',
        token: 'test-token',
      });

      const connectPromise = client.connect();
      emitEvent('error', new Error('Connection failed'));

      await expect(connectPromise).rejects.toThrow('Connection failed');
    });

    it('does not append workspaceId query when absent', async () => {
      client = new WorkspaceClient({
        endpoint: 'ws://localhost:8080',
        token: 'test-token',
      });

      Object.defineProperty(mockWsInstance, 'readyState', {
        value: WebSocket.OPEN,
        writable: true,
      });

      const connectPromise = client.connect();
      const calledURL = MockedWebSocket.mock.calls[0][0] as string;
      expect(calledURL).toContain('token=test-token');
      expect(calledURL).not.toContain('workspaceId=');
      emitEvent('open');
      await connectPromise;
    });
  });

  describe('disconnect', () => {
    it('should disconnect successfully', async () => {
      client = new WorkspaceClient({
        endpoint: 'ws://localhost:8080',
        workspaceId: 'test-workspace',
        token: 'test-token',
      });

      await connectClient();
      await client.disconnect();

      expect(client.isConnected).toBe(false);
      expect(client.connectionState).toBe('disconnected');
      expect(mockWsInstance.close).toHaveBeenCalledWith(1000, 'Client disconnect');
    });
  });

  describe('request', () => {
    it('should send request and receive response', async () => {
      client = new WorkspaceClient({
        endpoint: 'ws://localhost:8080',
        workspaceId: 'test-workspace',
        token: 'test-token',
        reconnect: false,
      });

      await connectClient();

      const requestPromise = client.request<string>('test.method', { param: 'value' });
      
      expect(mockWsInstance.send).toHaveBeenCalled();

      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: 'test-result',
      })));

      const result = await requestPromise;
      expect(result).toBe('test-result');
    });

    it('should throw error when not connected', async () => {
      client = new WorkspaceClient({
        endpoint: 'ws://localhost:8080',
        workspaceId: 'test-workspace',
        token: 'test-token',
      });

      await expect(client.request('test.method')).rejects.toThrow('Not connected to workspace');
    });

    it('should handle error response', async () => {
      client = new WorkspaceClient({
        endpoint: 'ws://localhost:8080',
        workspaceId: 'test-workspace',
        token: 'test-token',
        reconnect: false,
      });

      await connectClient();

      const requestPromise = client.request('test.method');
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        error: {
          code: -32600,
          message: 'Invalid request',
        },
      })));

      await expect(requestPromise).rejects.toThrow('Invalid request');
    });
  });

  describe('spotlight ergonomics', () => {
    it('uses configured workspaceId by default for client.spotlight', async () => {
      client = new WorkspaceClient({
        endpoint: 'ws://localhost:8080',
        workspaceId: 'default-ws',
        token: 'test-token',
        reconnect: false,
      });

      await connectClient();

      const listPromise = client.spotlight.list();
      let sentData = mockWsInstance.send.mock.calls[0][0] as string;
      let request = JSON.parse(sentData);
      expect(request.method).toBe('spotlight.list');
      expect(request.params.workspaceId).toBe('default-ws');

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { forwards: [] },
      })));
      await listPromise;

      const exposePromise = client.spotlight.expose({
        service: 'web',
        remotePort: 5173,
        localPort: 5173,
      });
      sentData = mockWsInstance.send.mock.calls[1][0] as string;
      request = JSON.parse(sentData);
      expect(request.method).toBe('spotlight.expose');
      expect(request.params.spec.workspaceId).toBe('default-ws');

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: {
          forward: {
            id: 'spot-1',
            workspaceId: 'default-ws',
            service: 'web',
            remotePort: 5173,
            localPort: 5173,
            host: '127.0.0.1',
            createdAt: new Date().toISOString(),
          },
        },
      })));
      await exposePromise;

      const explicitListPromise = client.spotlight.list('override-ws');
      sentData = mockWsInstance.send.mock.calls[2][0] as string;
      request = JSON.parse(sentData);
      expect(request.params.workspaceId).toBe('override-ws');

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { forwards: [] },
      })));
      await explicitListPromise;
    });
  });

  describe('onDisconnect', () => {
    it('should call disconnect callback', async () => {
      client = new WorkspaceClient({
        endpoint: 'ws://localhost:8080',
        workspaceId: 'test-workspace',
        token: 'test-token',
        reconnect: false,
      });

      const disconnectCallback = jest.fn();
      client.onDisconnect(disconnectCallback);

      await connectClient();
      await client.disconnect();

      expect(disconnectCallback).toHaveBeenCalled();
    });
  });
});
