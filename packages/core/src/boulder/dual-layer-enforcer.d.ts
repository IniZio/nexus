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
export declare class DualLayerBoulderEnforcer {
    private config;
    private iteration;
    private lastActivity;
    private intervalId;
    private isEnforcing;
    private onEnforcement;
    private toolInProgress;
    private permissionPending;
    constructor(config?: Partial<DualLayerConfig>, onEnforcement?: (message: string) => void);
    /**
     * LAYER 1: Record tool call (active work)
     */
    recordToolCall(toolName: string): void;
    /**
     * Start tool execution - pauses idle checking
     */
    startToolExecution(toolName: string): void;
    /**
     * End tool execution - resumes idle checking
     */
    endToolExecution(toolName: string): void;
    /**
     * Set permission pending state - pauses idle checking
     */
    setPermissionPending(pending: boolean): void;
    /**
     * LAYER 1: Check text for explicit completion
     */
    checkText(text: string): boolean;
    /**
     * LAYER 2: Start interval-based idle checking
     * This is the fallback that catches missed completions
     */
    private startIntervalCheck;
    /**
     * LAYER 2: Check if idle and enforce
     */
    private checkIdleAndEnforce;
    /**
     * Trigger enforcement
     */
    triggerEnforcement(): void;
    /**
     * Build enforcement message
     */
    private buildEnforcementMessage;
    /**
     * Stop the interval
     */
    stop(): void;
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
    };
}
export declare function createDualLayerEnforcer(config?: Partial<DualLayerConfig>, onEnforcement?: (message: string) => void): DualLayerBoulderEnforcer;
//# sourceMappingURL=dual-layer-enforcer.d.ts.map