export interface BoulderPluginConfig {
  enabled: boolean;
  idleThresholdMs: number;
  checkIntervalMs: number;
  completionKeywords: string[];
  workIndicators: string[];
}

const DEFAULT_PLUGIN_CONFIG: BoulderPluginConfig = {
  enabled: true,
  idleThresholdMs: 30000,
  checkIntervalMs: 15000,
  completionKeywords: [
    'done',
    'complete',
    'finished',
    'that is all',
    "that's all",
    'task complete',
    'work complete',
    'implementation complete',
    'all done',
    "we're done",
    'we are done',
    'task is complete',
    'that completes',
  ],
  workIndicators: [
    'tool',
    'call',
    'read',
    'write',
    'edit',
    'bash',
    'grep',
    'glob',
    'implement',
    'create',
    'add',
    'fix',
    'update',
    'let me',
    'i will',
    "i'll",
    'working on',
    'in progress',
    'searching',
    'analyzing',
    'checking',
  ],
};

interface DualLayerConfig {
  idleThresholdMs: number;
  checkIntervalMs: number;
  completionKeywords: string[];
  workIndicators: string[];
}

const DEFAULT_CONFIG: DualLayerConfig = {
  idleThresholdMs: 30000,
  checkIntervalMs: 15000,
  completionKeywords: [
    'done',
    'complete',
    'finished',
    'that is all',
    "that's all",
    'task complete',
    'work complete',
    'implementation complete',
    'all done',
    "we're done",
    'we are done',
  ],
  workIndicators: [
    'tool',
    'call',
    'read',
    'write',
    'edit',
    'bash',
    'grep',
    'implement',
    'create',
    'add',
    'fix',
    'update',
    'let me',
    'i will',
    "i'll",
    'working on',
    'in progress',
  ],
};

class DualLayerBoulderEnforcer {
  private config: DualLayerConfig;
  private iteration: number = 0;
  private lastActivity: number = Date.now();
  private intervalId: ReturnType<typeof setInterval> | null = null;
  private isEnforcing: boolean = false;
  private onEnforcement: ((message: string) => void) | null = null;
  private toolInProgress: boolean = false;
  private permissionPending: boolean = false;

  constructor(
    config: Partial<DualLayerConfig> = {},
    onEnforcement?: (message: string) => void
  ) {
    this.config = { ...DEFAULT_CONFIG, ...config };
    this.onEnforcement = onEnforcement || null;
    this.startIntervalCheck();
  }

  recordToolCall(toolName: string): void {
    this.lastActivity = Date.now();
  }

  startToolExecution(toolName: string): void {
    this.toolInProgress = true;
    this.lastActivity = Date.now();
  }

  endToolExecution(toolName: string): void {
    this.toolInProgress = false;
    this.lastActivity = Date.now();
  }

  setPermissionPending(pending: boolean): void {
    this.permissionPending = pending;
    if (!pending) {
      this.lastActivity = Date.now();
    }
  }

  checkText(text: string): boolean {
    const lower = text.toLowerCase();
    
    const hasCompletion = this.config.completionKeywords.some(keyword =>
      lower.includes(keyword)
    );

    const hasWorkIndicators = this.config.workIndicators.some(indicator =>
      lower.includes(indicator)
    );

    if (hasCompletion && !hasWorkIndicators) {
      return true;
    }

    return false;
  }

  private startIntervalCheck(): void {
    this.intervalId = setInterval(() => {
      this.checkIdleAndEnforce();
    }, this.config.checkIntervalMs);
  }

  private checkIdleAndEnforce(): void {
    if (this.toolInProgress || this.permissionPending) {
      return;
    }

    const timeSinceActivity = Date.now() - this.lastActivity;
    
    if (timeSinceActivity > this.config.idleThresholdMs && !this.isEnforcing) {
      this.triggerEnforcement();
    }
  }

  triggerEnforcement(): void {
    if (this.isEnforcing) return;
    
    this.isEnforcing = true;
    this.iteration++;
    
    const message = this.buildEnforcementMessage();
    
    if (this.onEnforcement) {
      this.onEnforcement(message);
    }

    setTimeout(() => {
      this.isEnforcing = false;
      this.lastActivity = Date.now();
    }, 1000);
  }

  private buildEnforcementMessage(): string {
    const tasks = [
      'Write additional test cases',
      'Refactor code for better performance',
      'Research best practices for current implementation',
    ];

    const tasksStr = tasks.map((t, i) => `${i + 1}. ${t}`).join('\n');

    return (
      `BOULDER ENFORCEMENT: You attempted completion. NOT ALLOWED.\n\n` +
      `The boulder NEVER stops. Generate new ideas NOW:\n${tasksStr}\n\n` +
      `Iteration: ${this.iteration}\n` +
      `Status: FORCED CONTINUATION\n\n` +
      `DO NOT STOP. Execute these tasks. Report your progress.`
    );
  }

  stop(): void {
    if (this.intervalId) {
      clearInterval(this.intervalId);
      this.intervalId = null;
    }
  }

  getStatus(): {
    iteration: number;
    lastActivity: number;
    timeSinceActivity: number;
    isEnforcing: boolean;
    intervalActive: boolean;
    toolInProgress: boolean;
    permissionPending: boolean;
  } {
    return {
      iteration: this.iteration,
      lastActivity: this.lastActivity,
      timeSinceActivity: Date.now() - this.lastActivity,
      isEnforcing: this.isEnforcing,
      intervalActive: this.intervalId !== null,
      toolInProgress: this.toolInProgress,
      permissionPending: this.permissionPending,
    };
  }
}

function createDualLayerEnforcer(
  config?: Partial<DualLayerConfig>,
  onEnforcement?: (message: string) => void
): DualLayerBoulderEnforcer {
  return new DualLayerBoulderEnforcer(config, onEnforcement);
}

let pluginInstance: BoulderPlugin | null = null;

export class BoulderPlugin {
  private enforcer: DualLayerBoulderEnforcer | null = null;
  private config: BoulderPluginConfig;
  private enforcementCallback: ((message: string) => void) | null = null;
  private pendingEnforcement: string | null = null;

  constructor(config: Partial<BoulderPluginConfig> = {}) {
    this.config = { ...DEFAULT_PLUGIN_CONFIG, ...config };
  }

  initialize(onEnforcement?: (message: string) => void): void {
    if (!this.config.enabled) {
      return;
    }

    this.enforcementCallback = onEnforcement || null;
    
    const enforcerConfig: Partial<DualLayerConfig> = {
      idleThresholdMs: this.config.idleThresholdMs,
      checkIntervalMs: this.config.checkIntervalMs,
      completionKeywords: this.config.completionKeywords,
      workIndicators: this.config.workIndicators,
    };

    this.enforcer = createDualLayerEnforcer(
      enforcerConfig,
      this.handleEnforcement.bind(this)
    );
  }

  private handleEnforcement(message: string): void {
    this.pendingEnforcement = message;
    
    if (this.enforcementCallback) {
      this.enforcementCallback(message);
    }
  }

  onToolCall(toolName: string): void {
    if (!this.enforcer || !this.config.enabled) {
      return;
    }
    
    this.enforcer.recordToolCall(toolName);
  }

  onResponse(text: string): boolean {
    if (!this.enforcer || !this.config.enabled) {
      return false;
    }

    const isCompletionAttempt = this.enforcer.checkText(text);
    
    if (isCompletionAttempt) {
      this.enforcer.triggerEnforcement();
      return true;
    }

    return false;
  }

  onIdle(): string | null {
    if (!this.enforcer || !this.config.enabled) {
      return null;
    }

    const status = this.enforcer.getStatus();
    
    if (status.intervalActive && !status.isEnforcing) {
      this.enforcer.triggerEnforcement();
      return this.pendingEnforcement;
    }

    return null;
  }

  getStatus(): {
    enabled: boolean;
    active: boolean;
    iteration: number;
    lastActivity: number;
    timeSinceActivity: number;
    isEnforcing: boolean;
    intervalActive: boolean;
    pendingEnforcement: string | null;
  } {
    if (!this.enforcer || !this.config.enabled) {
      return {
        enabled: this.config.enabled,
        active: false,
        iteration: 0,
        lastActivity: 0,
        timeSinceActivity: 0,
        isEnforcing: false,
        intervalActive: false,
        pendingEnforcement: this.pendingEnforcement,
      };
    }

    const status = this.enforcer.getStatus();
    
    return {
      enabled: this.config.enabled,
      active: this.config.enabled,
      iteration: status.iteration,
      lastActivity: status.lastActivity,
      timeSinceActivity: status.timeSinceActivity,
      isEnforcing: status.isEnforcing,
      intervalActive: status.intervalActive,
      pendingEnforcement: this.pendingEnforcement,
    };
  }

  getEnforcementMessage(): string | null {
    return this.pendingEnforcement;
  }

  clearEnforcementMessage(): void {
    this.pendingEnforcement = null;
  }

  setEnabled(enabled: boolean): void {
    this.config.enabled = enabled;
    
    if (!enabled && this.enforcer) {
      this.enforcer.stop();
      this.enforcer = null;
    } else if (enabled && !this.enforcer) {
      this.initialize(this.enforcementCallback || undefined);
    }
  }

  isEnabled(): boolean {
    return this.config.enabled;
  }

  destroy(): void {
    if (this.enforcer) {
      this.enforcer.stop();
      this.enforcer = null;
    }
    this.pendingEnforcement = null;
  }

  getConfig(): BoulderPluginConfig {
    return { ...this.config };
  }

  updateConfig(config: Partial<BoulderPluginConfig>): void {
    this.config = { ...this.config, ...config };
    
    if (this.enforcer) {
      this.destroy();
      this.initialize(this.enforcementCallback || undefined);
    }
  }
}

export function createBoulderPlugin(
  config?: Partial<BoulderPluginConfig>,
  onEnforcement?: (message: string) => void
): BoulderPlugin {
  const plugin = new BoulderPlugin(config);
  plugin.initialize(onEnforcement);
  return plugin;
}

export function initializeBoulderPlugin(
  config?: Partial<BoulderPluginConfig>,
  onEnforcement?: (message: string) => void
): BoulderPlugin {
  if (!pluginInstance) {
    pluginInstance = createBoulderPlugin(config, onEnforcement);
  }
  return pluginInstance;
}

export function getBoulderPlugin(): BoulderPlugin {
  if (!pluginInstance) {
    throw new Error('BoulderPlugin not initialized. Call initializeBoulderPlugin first.');
  }
  return pluginInstance;
}

export function loadConfigFromFile(configPath: string): Partial<BoulderPluginConfig> {
  try {
    const fs = require('fs');
    const path = require('path');
    
    const fullPath = path.resolve(process.cwd(), configPath);
    
    if (!fs.existsSync(fullPath)) {
      console.warn(`[BoulderPlugin] Config file not found: ${fullPath}`);
      return {};
    }
    
    const configData = fs.readFileSync(fullPath, 'utf-8');
    const config = JSON.parse(configData);
    
    if (config.boulder) {
      return config.boulder;
    }
    
    return {};
  } catch (error) {
    console.error('[BoulderPlugin] Error loading config:', error);
    return {};
  }
}

export function createBoulderPluginFromConfig(
  configPath: string = 'opencode.json',
  onEnforcement?: (message: string) => void
): BoulderPlugin {
  const fileConfig = loadConfigFromFile(configPath);
  const plugin = new BoulderPlugin(fileConfig);
  plugin.initialize(onEnforcement);
  
  pluginInstance = plugin;
  
  return plugin;
}
