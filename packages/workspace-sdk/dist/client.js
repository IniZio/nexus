"use strict";
var __importDefault = (this && this.__importDefault) || function (mod) {
    return (mod && mod.__esModule) ? mod : { "default": mod };
};
Object.defineProperty(exports, "__esModule", { value: true });
exports.WorkspaceClient = void 0;
const ws_1 = __importDefault(require("ws"));
const fs_1 = require("./fs");
const exec_1 = require("./exec");
class WorkspaceClient {
    constructor(config) {
        this.ws = null;
        this.state = 'disconnected';
        this.reconnectAttempts = 0;
        this.requestMap = new Map();
        this.disconnectCallbacks = [];
        this.reconnectTimeout = null;
        this.messageQueue = [];
        this.reconnectEnabled = true;
        this.requestId = 0;
        this.config = {
            endpoint: config.endpoint,
            workspaceId: config.workspaceId,
            token: config.token,
            reconnect: config.reconnect ?? true,
            reconnectDelay: config.reconnectDelay ?? 1000,
            maxReconnectAttempts: config.maxReconnectAttempts ?? 10,
        };
        this.fs = new fs_1.FSOperations(this);
        this.exec = new exec_1.ExecOperations(this);
    }
    get isConnected() {
        return this.state === 'connected';
    }
    get connectionState() {
        return this.state;
    }
    async connect() {
        if (this.state === 'connected' || this.state === 'connecting') {
            return;
        }
        this.state = 'connecting';
        return new Promise((resolve, reject) => {
            try {
                const url = new URL(this.config.endpoint);
                url.searchParams.set('workspaceId', this.config.workspaceId);
                url.searchParams.set('token', this.config.token);
                this.ws = new ws_1.default(url.toString());
                this.ws.on('open', () => {
                    this.state = 'connected';
                    this.reconnectAttempts = 0;
                    this.processMessageQueue();
                    resolve();
                });
                this.ws.on('message', (data) => {
                    this.handleMessage(data.toString());
                });
                this.ws.on('close', (code, reason) => {
                    const disconnectReason = {
                        code,
                        reason: reason.toString(),
                    };
                    this.handleDisconnect(disconnectReason);
                });
                this.ws.on('error', (error) => {
                    if (this.state === 'connecting') {
                        reject(error);
                    }
                    else {
                        console.error('WebSocket error:', error.message);
                    }
                });
            }
            catch (error) {
                this.state = 'disconnected';
                reject(error);
            }
        });
    }
    async disconnect() {
        this.reconnectEnabled = false;
        if (this.reconnectTimeout) {
            clearTimeout(this.reconnectTimeout);
            this.reconnectTimeout = null;
        }
        if (this.ws) {
            this.ws.close(1000, 'Client disconnect');
            this.ws = null;
        }
        this.state = 'disconnected';
        this.requestMap.forEach(({ reject }) => {
            reject(new Error('Connection closed'));
        });
        this.requestMap.clear();
        this.messageQueue = [];
    }
    onDisconnect(callback) {
        this.disconnectCallbacks.push(callback);
    }
    async request(method, params) {
        if (!this.ws || this.ws.readyState !== ws_1.default.OPEN) {
            throw new Error('Not connected to workspace');
        }
        const id = this.generateRequestId();
        const request = {
            jsonrpc: '2.0',
            id,
            method,
            params,
        };
        return new Promise((resolve, reject) => {
            this.requestMap.set(id, { resolve: resolve, reject });
            try {
                this.ws.send(JSON.stringify(request));
            }
            catch (error) {
                this.requestMap.delete(id);
                reject(error);
            }
        });
    }
    generateRequestId() {
        this.requestId++;
        return `req-${Date.now()}-${this.requestId}`;
    }
    handleMessage(data) {
        try {
            const response = JSON.parse(data);
            if (response.id) {
                const pending = this.requestMap.get(response.id);
                if (pending) {
                    this.requestMap.delete(response.id);
                    if (response.error) {
                        pending.reject(new Error(response.error.message));
                    }
                    else {
                        pending.resolve(response.result);
                    }
                }
            }
        }
        catch (error) {
            console.error('Failed to parse RPC response:', error);
        }
    }
    handleDisconnect(reason) {
        this.ws = null;
        this.state = 'disconnected';
        this.requestMap.forEach(({ reject }) => {
            reject(new Error(`Connection closed: ${reason.reason}`));
        });
        this.requestMap.clear();
        this.disconnectCallbacks.forEach((callback) => callback());
        if (this.reconnectEnabled && this.config.reconnect) {
            this.attemptReconnect();
        }
    }
    attemptReconnect() {
        if (this.reconnectAttempts >= this.config.maxReconnectAttempts) {
            console.error('Max reconnection attempts reached');
            return;
        }
        this.state = 'reconnecting';
        this.reconnectAttempts++;
        const delay = this.calculateExponentialBackoff();
        console.log(`Attempting to reconnect in ${delay}ms (attempt ${this.reconnectAttempts})`);
        this.reconnectTimeout = setTimeout(async () => {
            try {
                await this.connect();
                console.log('Successfully reconnected');
            }
            catch (error) {
                console.error('Reconnection failed:', error);
            }
        }, delay);
    }
    calculateExponentialBackoff() {
        return Math.min(this.config.reconnectDelay * Math.pow(2, this.reconnectAttempts - 1), 30000);
    }
    processMessageQueue() {
        while (this.messageQueue.length > 0) {
            const request = this.messageQueue.shift();
            if (request && this.ws && this.ws.readyState === ws_1.default.OPEN) {
                try {
                    this.ws.send(JSON.stringify(request));
                }
                catch (error) {
                    console.error('Failed to send queued message:', error);
                }
            }
        }
    }
}
exports.WorkspaceClient = WorkspaceClient;
//# sourceMappingURL=client.js.map