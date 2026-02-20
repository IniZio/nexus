import type { WorkspaceClient } from '../types/workspace-sdk';

interface ActivityTracker {
  lastActivity: number;
  pingInterval: ReturnType<typeof setInterval> | null;
  client: WorkspaceClient | null;
  config: {
    idleTimeout: number;
    keepAliveInterval: number;
  };
}

const tracker: ActivityTracker = {
  lastActivity: Date.now(),
  pingInterval: null,
  client: null,
  config: {
    idleTimeout: 300000,
    keepAliveInterval: 60000,
  },
};

export function initActivityTracking(client: WorkspaceClient, config: { idleTimeout?: number; keepAliveInterval?: number }): void {
  tracker.client = client;
  tracker.config.idleTimeout = config.idleTimeout ?? tracker.config.idleTimeout;
  tracker.config.keepAliveInterval = config.keepAliveInterval ?? tracker.config.keepAliveInterval;
  tracker.lastActivity = Date.now();

  if (tracker.pingInterval) {
    clearInterval(tracker.pingInterval);
  }

  tracker.pingInterval = setInterval(async () => {
    try {
      if (tracker.client) {
        await tracker.client.ping();
        const elapsed = Date.now() - tracker.lastActivity;
        if (elapsed > tracker.config.idleTimeout) {
          await tracker.client.setStatus('idle');
        } else {
          await tracker.client.setStatus('active');
        }
      }
    } catch (error) {
      console.error('[nexus] Failed to ping workspace:', error);
    }
  }, tracker.config.keepAliveInterval);
}

export function recordActivity(): void {
  tracker.lastActivity = Date.now();
}

export function stopActivityTracking(): void {
  if (tracker.pingInterval) {
    clearInterval(tracker.pingInterval);
    tracker.pingInterval = null;
  }
  tracker.client = null;
}

export function getActivityStatus(): { lastActivity: number; isActive: boolean } {
  const elapsed = Date.now() - tracker.lastActivity;
  return {
    lastActivity: tracker.lastActivity,
    isActive: elapsed < tracker.config.idleTimeout,
  };
}
