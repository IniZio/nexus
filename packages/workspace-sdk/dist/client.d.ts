import { WorkspaceClientConfig, ConnectionState } from './types';
import { FSOperations } from './fs';
import { ExecOperations } from './exec';
export declare class WorkspaceClient {
    private ws;
    private config;
    private state;
    private reconnectAttempts;
    private requestMap;
    private disconnectCallbacks;
    private reconnectTimeout;
    private messageQueue;
    private reconnectEnabled;
    private requestId;
    readonly fs: FSOperations;
    readonly exec: ExecOperations;
    constructor(config: WorkspaceClientConfig);
    get isConnected(): boolean;
    get connectionState(): ConnectionState;
    connect(): Promise<void>;
    disconnect(): Promise<void>;
    onDisconnect(callback: () => void): void;
    request<T = unknown>(method: string, params?: Record<string, unknown>): Promise<T>;
    private generateRequestId;
    private handleMessage;
    private handleDisconnect;
    private attemptReconnect;
    private calculateExponentialBackoff;
    private processMessageQueue;
}
//# sourceMappingURL=client.d.ts.map