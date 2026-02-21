/**
 * Boulder Idle Detection System
 *
 * Detects when agent is truly idle or attempting to complete.
 * Only triggers enforcement on actual completion attempts, not regular tool usage.
 */
const DEFAULT_CONFIG = {
    idleThresholdMs: 60000, // 60 seconds
    completionKeywords: [
        'complete',
        'done',
        'finished',
        'that\'s all',
        'that is all',
        'task complete',
        'work complete',
        'implementation complete',
        'all done',
        'we\'re done',
        'we are done',
        'closing',
        'wrap up',
        'conclude',
        'summary of what we did',
        'here\'s what we accomplished',
        'to summarize',
    ],
    falsePositivePatterns: [
        'tool',
        'call',
        'read',
        'write',
        'edit',
        'bash',
        'grep',
        'fetch',
        'the user',
        'the task',
        'in progress',
        'working on',
        'let me',
        'i will',
        'i\'ll',
    ],
};
export class BoulderIdleDetector {
    lastActivity = Date.now();
    activityHistory = [];
    config;
    lastToolCall = Date.now();
    isWorking = true;
    constructor(config = {}) {
        this.config = { ...DEFAULT_CONFIG, ...config };
    }
    /**
     * Record a tool call - this is ACTIVE WORK, not idle
     */
    recordToolCall(toolName) {
        this.lastToolCall = Date.now();
        this.lastActivity = Date.now();
        this.isWorking = true;
        this.activityHistory.push({
            timestamp: Date.now(),
            type: 'tool_call',
            content: toolName,
        });
        // Trim history to last 100 records
        if (this.activityHistory.length > 100) {
            this.activityHistory = this.activityHistory.slice(-100);
        }
    }
    /**
     * Record text output - check for completion patterns
     */
    recordTextOutput(text) {
        this.lastActivity = Date.now();
        const isCompletionAttempt = this.detectCompletionAttempt(text);
        this.activityHistory.push({
            timestamp: Date.now(),
            type: isCompletionAttempt ? 'completion_attempt' : 'text_output',
            content: text.slice(0, 200), // Store first 200 chars
        });
        return isCompletionAttempt;
    }
    /**
     * Detect if text contains completion patterns
     */
    detectCompletionAttempt(text) {
        const lowerText = text.toLowerCase();
        // Check for completion keywords
        const hasCompletionKeyword = this.config.completionKeywords.some(keyword => lowerText.includes(keyword.toLowerCase()));
        // Check for false positive patterns (tools, ongoing work)
        const hasFalsePositive = this.config.falsePositivePatterns.some(pattern => lowerText.includes(pattern.toLowerCase()));
        // Completion attempt = has completion keyword AND no false positive
        return hasCompletionKeyword && !hasFalsePositive;
    }
    /**
     * Check if agent is idle
     * Only returns true if:
     * 1. No tool calls for threshold period
     * 2. Last output was a completion attempt
     */
    checkIdle() {
        const timeSinceLastTool = Date.now() - this.lastToolCall;
        const timeSinceLastActivity = Date.now() - this.lastActivity;
        // Recent tool call = definitely not idle
        if (timeSinceLastTool < this.config.idleThresholdMs) {
            return { isIdle: false, reason: 'active_tool_usage' };
        }
        // Check recent history for completion attempts
        const recentAttempts = this.activityHistory
            .filter(a => Date.now() - a.timestamp < this.config.idleThresholdMs)
            .filter(a => a.type === 'completion_attempt');
        if (recentAttempts.length > 0) {
            return {
                isIdle: true,
                reason: 'completion_attempt_detected'
            };
        }
        // No activity for threshold period
        if (timeSinceLastActivity > this.config.idleThresholdMs) {
            return {
                isIdle: true,
                reason: 'no_activity_timeout'
            };
        }
        return { isIdle: false, reason: 'active_work' };
    }
    /**
     * Get current status
     */
    getStatus() {
        const recentHistory = this.activityHistory.filter(a => Date.now() - a.timestamp < 60000 // Last minute
        );
        return {
            isWorking: this.isWorking,
            timeSinceLastTool: Date.now() - this.lastToolCall,
            timeSinceLastActivity: Date.now() - this.lastActivity,
            recentToolCalls: recentHistory.filter(a => a.type === 'tool_call').length,
            recentCompletionAttempts: recentHistory.filter(a => a.type === 'completion_attempt').length,
        };
    }
    /**
     * Reset detector
     */
    reset() {
        this.lastActivity = Date.now();
        this.lastToolCall = Date.now();
        this.activityHistory = [];
        this.isWorking = true;
    }
}
/**
 * Singleton instance for global idle detection
 */
let globalDetector = null;
export function getGlobalIdleDetector() {
    if (!globalDetector) {
        globalDetector = new BoulderIdleDetector();
    }
    return globalDetector;
}
export function resetGlobalIdleDetector() {
    globalDetector = null;
}
//# sourceMappingURL=idle-detector.js.map