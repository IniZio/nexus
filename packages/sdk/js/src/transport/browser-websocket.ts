function coerceMessage(raw: unknown): string {
  if (typeof raw === 'string') {
    return raw;
  }
  if (raw && typeof raw === 'object' && 'toString' in raw) {
    return String(raw);
  }
  return '';
}

export class BrowserWebSocketTransport {
  private socket: WebSocket | null = null;

  onOpen?: () => void;
  onMessage?: (data: string) => void;
  onClose?: (code: number, reason: string) => void;
  onError?: (error: Error) => void;

  connect(url: string): void {
    this.disconnect();
    const WSCtor = globalThis.WebSocket;
    if (!WSCtor) {
      throw new Error('WebSocket is not available in this runtime');
    }
    this.socket = new WSCtor(url);
    this.socket.addEventListener('open', () => this.onOpen?.());
    this.socket.addEventListener('message', (evt) => {
      this.onMessage?.(coerceMessage(evt.data));
    });
    this.socket.addEventListener('close', (evt) => {
      this.onClose?.(evt.code, evt.reason ?? '');
    });
    this.socket.addEventListener('error', () => {
      this.onError?.(new Error('WebSocket connection error'));
    });
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
