/**
 * Boulder Infinite Continuous Enforcement - Main Integration
 *
 * Coordinates idle detection, task queue, and state management.
 * Ensures the boulder NEVER stops rolling.
 */
import { getGlobalIdleDetector } from './idle-detector.js';
import { InfiniteBoulderStateManager } from './infinite-state.js';
import { TaskQueue } from './task-queue.js';
const DEFAULT_CONFIG = {
    minTasksInQueue: 5,
    idleThresholdMs: 60000,
    nextTasksCount: 3,
};
export class BoulderContinuousEnforcement {
    stateManager;
    taskQueue;
    idleDetector;
    config;
    currentTaskId = null;
    constructor(config = {}) {
        this.config = { ...DEFAULT_CONFIG, ...config };
        this.stateManager = new InfiniteBoulderStateManager();
        this.taskQueue = new TaskQueue();
        this.idleDetector = getGlobalIdleDetector();
    }
    /**
     * Record tool call - marks agent as working
     */
    recordToolCall(toolName) {
        this.idleDetector.recordToolCall(toolName);
        this.stateManager.recordActivity();
    }
    /**
     * Record text output - checks for completion attempts
     * Returns true if enforcement should trigger
     */
    recordTextOutput(text) {
        const isCompletionAttempt = this.idleDetector.recordTextOutput(text);
        this.stateManager.recordActivity();
        if (isCompletionAttempt) {
            return this.triggerEnforcement();
        }
        // Check for idle
        const idleCheck = this.idleDetector.checkIdle();
        if (idleCheck.isIdle) {
            return this.triggerEnforcement();
        }
        return { enforce: false };
    }
    /**
     * Trigger boulder enforcement
     * Always generates new tasks and continues
     */
    triggerEnforcement() {
        // Increment iteration
        this.stateManager.incrementIteration();
        // Ensure we have enough tasks
        this.taskQueue.ensureMinimumTasks(this.config.minTasksInQueue);
        // Get next tasks
        const nextTasks = this.stateManager.getNextTasks(this.config.nextTasksCount);
        // Get current task info
        const state = this.stateManager.getState();
        const stats = this.taskQueue.getStats();
        // Build enforcement message
        const message = this.buildEnforcementMessage(state, stats, nextTasks);
        return { enforce: true, message };
    }
    /**
     * Build enforcement message
     */
    buildEnforcementMessage(state, stats, nextTasks) {
        const tasksStr = nextTasks.map((task, i) => `${i + 1}. ${task}`).join('\n');
        return (`BOULDER ENFORCEMENT: You attempted completion. NOT ALLOWED.\n\n` +
            `The boulder NEVER stops. Generate new ideas NOW:\n${tasksStr}\n\n` +
            `Iteration: ${state.iteration}\n` +
            `Tasks completed: ${state.tasksCompleted} | In queue: ${stats.pending + stats.active}\n` +
            `Status: FORCED CONTINUATION\n\n` +
            `DO NOT STOP. Execute these tasks. Report your progress.`);
    }
    /**
     * Start a new task
     */
    startNextTask() {
        const task = this.taskQueue.getNextTask();
        if (task) {
            this.currentTaskId = task.id;
            return { taskId: task.id, description: task.description };
        }
        return null;
    }
    /**
     * Mark current task as done
     */
    completeCurrentTask() {
        if (this.currentTaskId) {
            this.taskQueue.markDone(this.currentTaskId);
            this.stateManager.recordTaskCompletion();
            this.currentTaskId = null;
        }
    }
    /**
     * Get current status
     */
    getStatus() {
        return {
            iteration: this.stateManager.getState().iteration,
            currentTask: this.currentTaskId,
            queueStats: this.taskQueue.getStats(),
            isWorking: this.idleDetector.getStatus().isWorking,
        };
    }
    /**
     * Get formatted status message
     */
    getStatusMessage() {
        return this.stateManager.getStatusMessage();
    }
}
// Singleton instance
let globalEnforcement = null;
export function getGlobalEnforcement() {
    if (!globalEnforcement) {
        globalEnforcement = new BoulderContinuousEnforcement();
    }
    return globalEnforcement;
}
export function resetGlobalEnforcement() {
    globalEnforcement = null;
}
// Re-exports
export { BoulderIdleDetector, getGlobalIdleDetector } from './idle-detector.js';
export { InfiniteBoulderStateManager } from './infinite-state.js';
export { TaskQueue } from './task-queue.js';
export { DualLayerBoulderEnforcer, createDualLayerEnforcer } from './dual-layer-enforcer.js';
//# sourceMappingURL=index.js.map