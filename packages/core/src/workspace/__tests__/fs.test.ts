import { WorkspaceClient } from '../client';
import { FSOperations } from '../fs';
import WebSocket from 'ws';

jest.mock('ws');

const MockedWebSocket = WebSocket as jest.MockedClass<typeof WebSocket>;

describe('FSOperations', () => {
  let client: WorkspaceClient;
  let fs: FSOperations;
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

    fs = new FSOperations(client);
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

  describe('readFile', () => {
    it('should read file with utf8 encoding', async () => {
      await connectClient();

      const promise = fs.readFile('/path/to/file.txt', 'utf8');
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.method).toBe('fs.readFile');
      expect(request.params.path).toBe('/path/to/file.txt');

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { content: 'file content', encoding: 'utf8' },
      })));

      const result = await promise;
      expect(result).toBe('file content');
    });

    it('should read file as buffer for binary encoding', async () => {
      await connectClient();

      const promise = fs.readFile('/path/to/file.bin', 'base64');
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { content: 'SGVsbG8gV29ybGQ=', encoding: 'base64' },
      })));

      const result = await promise;
      expect(result).toBeInstanceOf(Buffer);
    });
  });

  describe('writeFile', () => {
    it('should write file with string content', async () => {
      await connectClient();

      const promise = fs.writeFile('/path/to/file.txt', 'file content');
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.method).toBe('fs.writeFile');
      expect(request.params.path).toBe('/path/to/file.txt');
      expect(request.params.content).toBe('file content');
      expect(request.params.encoding).toBe('utf8');

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { success: true },
      })));

      await expect(promise).resolves.toBeUndefined();
    });

    it('should write file with buffer content', async () => {
      await connectClient();

      const bufferContent = Buffer.from('binary content');
      const promise = fs.writeFile('/path/to/file.bin', bufferContent);
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.params.encoding).toBe('base64');

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { success: true },
      })));

      await expect(promise).resolves.toBeUndefined();
    });
  });

  describe('exists', () => {
    it('should return true when file exists', async () => {
      await connectClient();

      const promise = fs.exists('/path/to/existing-file.txt');
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.method).toBe('fs.exists');

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { exists: true },
      })));

      const result = await promise;
      expect(result).toBe(true);
    });

    it('should return false when file does not exist', async () => {
      await connectClient();

      const promise = fs.exists('/path/to/non-existing.txt');
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { exists: false },
      })));

      const result = await promise;
      expect(result).toBe(false);
    });
  });

  describe('readdir', () => {
    it('should return list of entries', async () => {
      await connectClient();

      const promise = fs.readdir('/path/to/dir');
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.method).toBe('fs.readdir');

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { entries: ['file1.txt', 'file2.txt', 'subdir'] },
      })));

      const result = await promise;
      expect(result).toEqual(['file1.txt', 'file2.txt', 'subdir']);
    });
  });

  describe('stat', () => {
    it('should return file stats', async () => {
      await connectClient();

      const promise = fs.stat('/path/to/file.txt');
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.method).toBe('fs.stat');

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: {
          stats: {
            isFile: true,
            isDirectory: false,
            size: 1024,
            mtime: '2024-01-01T00:00:00.000Z',
            ctime: '2024-01-01T00:00:00.000Z',
            mode: 33188,
          },
        },
      })));

      const result = await promise;
      expect(result.isFile).toBe(true);
      expect(result.isDirectory).toBe(false);
      expect(result.size).toBe(1024);
    });
  });

  describe('rm', () => {
    it('should remove file', async () => {
      await connectClient();

      const promise = fs.rm('/path/to/file.txt');
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.method).toBe('fs.rm');
      expect(request.params.path).toBe('/path/to/file.txt');

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { success: true },
      })));

      await expect(promise).resolves.toBeUndefined();
    });

    it('should remove directory recursively', async () => {
      await connectClient();

      const promise = fs.rm('/path/to/dir', true);
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.params.recursive).toBe(true);

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { success: true },
      })));

      await expect(promise).resolves.toBeUndefined();
    });
  });

  describe('mkdir', () => {
    it('should create directory', async () => {
      await connectClient();

      const promise = fs.mkdir('/path/to/newdir');
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.method).toBe('fs.mkdir');
      expect(request.params.path).toBe('/path/to/newdir');

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { success: true },
      })));

      await expect(promise).resolves.toBeUndefined();
    });

    it('should create directory recursively', async () => {
      await connectClient();

      const promise = fs.mkdir('/path/to/nested/dir', true);
      
      const sentData = mockWsInstance.send.mock.calls[0][0] as string;
      const request = JSON.parse(sentData);
      
      expect(request.params.recursive).toBe(true);

      emitEvent('message', Buffer.from(JSON.stringify({
        jsonrpc: '2.0',
        id: request.id,
        result: { success: true },
      })));

      await expect(promise).resolves.toBeUndefined();
    });
  });
});
