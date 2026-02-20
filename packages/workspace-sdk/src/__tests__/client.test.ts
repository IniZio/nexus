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
