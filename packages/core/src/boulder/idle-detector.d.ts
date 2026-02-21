/**
 * Boulder Idle Detection System
 *
 * Detects when agent is truly idle or attempting to complete.
 * Only triggers enforcement on actual completion attempts, not regular tool usage.
 */
export interface IdleDetectionConfig {
    idleThresholdMs: number;
    completionKeywords: string[];
    falsePositivePatterns: string[];
}
export interface ActivityRecord {
    timestamp: number;
    type: 'tool_call' | 'text_output' | 'completion_attempt';
    content?: string;
}
export declare class BoulderIdleDetector {
    private lastActivity;
    private activityHistory;
    private config;
    private lastToolCall;
    private isWorking;
    constructor(config?: Partial<IdleDetectionConfig>);
    /**
     * Record a tool call - this is ACTIVE WORK, not idle
     */
    recordToolCall(toolName: string): void;
    /**
     * Record text output - check for completion patterns
     */
    recordTextOutput(text: string): boolean;
    /**
     * Detect if text contains completion patterns
     */
    private detectCompletionAttempt;
    /**
     * Check if agent is idle
     * Only returns true if:
     * 1. No tool calls for threshold period
     * 2. Last output was a completion attempt
     */
    checkIdle(): {
        isIdle: boolean;
        reason: string;
    };
    /**
     * Get current status
     */
    getStatus(): {
        isWorking: boolean;
        timeSinceLastTool: number;
        timeSinceLastActivity: number;
        recentToolCalls: number;
        recentCompletionAttempts: number;
    };
    /**
     * Reset detector
     */
    reset(): void;
}
export declare function getGlobalIdleDetector(): BoulderIdleDetector;
export declare function resetGlobalIdleDetector(): void;
//# sourceMappingURL=idle-detector.d.ts.map