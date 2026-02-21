/**
 * Boulder Infinite Continuous Enforcement - Main Integration
 *
 * Coordinates idle detection, task queue, and state management.
 * Ensures the boulder NEVER stops rolling.
 */
export interface BoulderEnforcementConfig {
    minTasksInQueue: number;
    idleThresholdMs: number;
    nextTasksCount: number;
}
export declare class BoulderContinuousEnforcement {
    private stateManager;
    private taskQueue;
    private idleDetector;
    private config;
    private currentTaskId;
    constructor(config?: Partial<BoulderEnforcementConfig>);
    /**
     * Record tool call - marks agent as working
     */
    recordToolCall(toolName: string): void;
    /**
     * Record text output - checks for completion attempts
     * Returns true if enforcement should trigger
     */
    recordTextOutput(text: string): {
        enforce: boolean;
        message?: string;
    };
    /**
     * Trigger boulder enforcement
     * Always generates new tasks and continues
     */
    private triggerEnforcement;
    /**
     * Build enforcement message
     */
    private buildEnforcementMessage;
    /**
     * Start a new task
     */
    startNextTask(): {
        taskId: string;
        description: string;
    } | null;
    /**
     * Mark current task as done
     */
    completeCurrentTask(): void;
    /**
     * Get current status
     */
    getStatus(): {
        iteration: number;
        currentTask: string | null;
        queueStats: {
            total: number;
            pending: number;
            active: number;
            done: number;
        };
        isWorking: boolean;
    };
    /**
     * Get formatted status message
     */
    getStatusMessage(): string;
}
export declare function getGlobalEnforcement(): BoulderContinuousEnforcement;
export declare function resetGlobalEnforcement(): void;
export { BoulderIdleDetector, getGlobalIdleDetector } from './idle-detector.js';
export { InfiniteBoulderStateManager } from './infinite-state.js';
export { TaskQueue, BoulderTask } from './task-queue.js';
export { DualLayerBoulderEnforcer, createDualLayerEnforcer, DualLayerConfig } from './dual-layer-enforcer.js';
//# sourceMappingURL=index.d.ts.map