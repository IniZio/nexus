/**
 * Dual-Layer Boulder Enforcer
 * 
 * Based on oh-my-opencode pattern:
 * Layer 1: Explicit completion detection
 * Layer 2: Interval idle checking (fallback)
 */

export interface DualLayerConfig {
  idleThresholdMs: number;
  checkIntervalMs: number;
  completionKeywords: string[];
  workIndicators: string[];
}

const DEFAULT_CONFIG: DualLayerConfig = {
  idleThresholdMs: 30000,  // 30 seconds
  checkIntervalMs: 15000,  // Check every 15 seconds
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

export class DualLayerBoulderEnforcer {
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

  /**
   * LAYER 1: Record tool call (active work)
   */
  recordToolCall(toolName: string): void {
    this.lastActivity = Date.now();
  }

  /**
   * Start tool execution - pauses idle checking
   */
  startToolExecution(toolName: string): void {
    this.toolInProgress = true;
    this.lastActivity = Date.now();
  }

  /**
   * End tool execution - resumes idle checking
   */
  endToolExecution(toolName: string): void {
    this.toolInProgress = false;
    this.lastActivity = Date.now();
  }

  /**
   * Set permission pending state - pauses idle checking
   */
  setPermissionPending(pending: boolean): void {
    this.permissionPending = pending;
    if (!pending) {
      this.lastActivity = Date.now();
    }
  }

  /**
   * LAYER 1: Check text for explicit completion
   */
  checkText(text: string): boolean {
    const lower = text.toLowerCase();
    
    // Check for completion keywords
    const hasCompletion = this.config.completionKeywords.some(keyword =>
      lower.includes(keyword)
    );

    // Check for work indicators (false positive prevention)
    const hasWorkIndicators = this.config.workIndicators.some(indicator =>
      lower.includes(indicator)
    );

    // Only trigger if explicit completion AND no work indicators
    if (hasCompletion && !hasWorkIndicators) {
      return true;
    }

    return false;
  }

  /**
   * LAYER 2: Start interval-based idle checking
   * This is the fallback that catches missed completions
   */
  private startIntervalCheck(): void {
    this.intervalId = setInterval(() => {
      this.checkIdleAndEnforce();
    }, this.config.checkIntervalMs);
  }

  /**
   * LAYER 2: Check if idle and enforce
   */
  private checkIdleAndEnforce(): void {
    if (this.toolInProgress || this.permissionPending) {
      return;
    }

    const timeSinceActivity = Date.now() - this.lastActivity;
    
    if (timeSinceActivity > this.config.idleThresholdMs && !this.isEnforcing) {
      this.triggerEnforcement();
    }
  }

  /**
   * Trigger enforcement
   */
  triggerEnforcement(): void {
    if (this.isEnforcing) return;
    
    this.isEnforcing = true;
    this.iteration++;
    
    const message = this.buildEnforcementMessage();
    
    if (this.onEnforcement) {
      this.onEnforcement(message);
    }

    // Reset after enforcement
    setTimeout(() => {
      this.isEnforcing = false;
      this.lastActivity = Date.now();
    }, 1000);
  }

  /**
   * Build enforcement message
   */
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

  /**
   * Stop the interval
   */
  stop(): void {
    if (this.intervalId) {
      clearInterval(this.intervalId);
      this.intervalId = null;
    }
  }

  /**
   * Get current status
   */
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

export function createDualLayerEnforcer(
  config?: Partial<DualLayerConfig>,
  onEnforcement?: (message: string) => void
): DualLayerBoulderEnforcer {
  return new DualLayerBoulderEnforcer(config, onEnforcement);
}
