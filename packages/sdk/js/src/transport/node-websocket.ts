import WebSocket from 'ws';

export class NodeWebSocketTransport {
  private socket: WebSocket | null = null;

  onOpen?: () => void;
  onMessage?: (data: string) => void;
  onClose?: (code: number, reason: string) => void;
  onError?: (error: Error) => void;

  connect(url: string): void {
    this.disconnect();
    this.socket = new WebSocket(url);
    this.socket.on('open', () => this.onOpen?.());
    this.socket.on('message', (data: Buffer) => this.onMessage?.(data.toString()));
    this.socket.on('close', (code: number, reason: Buffer) =>
      this.onClose?.(code, reason.toString())
    );
    this.socket.on('error', (error: Error) => this.onError?.(error));
  }

  send(data: string): void {
    this.socket?.send(data);
  }

  disconnect(): void {
    if (this.socket) {
      this.socket.close(1000, 'Client disconnect');
      this.socket = null;
    }
  }

  isOpen(): boolean {
    return this.socket !== null && this.socket.readyState === WebSocket.OPEN;
  }
}
