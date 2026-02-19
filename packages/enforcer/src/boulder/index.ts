/**
 * Boulder Infinite Continuous Enforcement - Main Integration
 * 
 * Coordinates idle detection, task queue, and state management.
 * Ensures the boulder NEVER stops rolling.
 */

import { BoulderIdleDetector, getGlobalIdleDetector } from './idle-detector.js';
import { InfiniteBoulderStateManager } from './infinite-state.js';
import { TaskQueue } from './task-queue.js';

export interface BoulderEnforcementConfig {
  minTasksInQueue: number;
  idleThresholdMs: number;
  nextTasksCount: number;
}

const DEFAULT_CONFIG: BoulderEnforcementConfig = {
  minTasksInQueue: 5,
  idleThresholdMs: 60000,
  nextTasksCount: 3,
};

export class BoulderContinuousEnforcement {
  private stateManager: InfiniteBoulderStateManager;
  private taskQueue: TaskQueue;
  private idleDetector: BoulderIdleDetector;
  private config: BoulderEnforcementConfig;
  private currentTaskId: string | null = null;

  constructor(config: Partial<BoulderEnforcementConfig> = {}) {
    this.config = { ...DEFAULT_CONFIG, ...config };
    this.stateManager = new InfiniteBoulderStateManager();
    this.taskQueue = new TaskQueue();
    this.idleDetector = getGlobalIdleDetector();
  }

  /**
   * Record tool call - marks agent as working
   */
  recordToolCall(toolName: string): void {
    this.idleDetector.recordToolCall(toolName);
    this.stateManager.recordActivity();
  }

  /**
   * Record text output - checks for completion attempts
   * Returns true if enforcement should trigger
   */
  recordTextOutput(text: string): { enforce: boolean; message?: string } {
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
  private triggerEnforcement(): { enforce: boolean; message: string } {
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
  private buildEnforcementMessage(
    state: { iteration: number; tasksCompleted: number },
    stats: { pending: number; active: number },
    nextTasks: string[]
  ): string {
    const tasksStr = nextTasks.map((task, i) => `${i + 1}. ${task}`).join('\n');
    
    return (
      `BOULDER ENFORCEMENT: You attempted completion. NOT ALLOWED.\n\n` +
      `The boulder NEVER stops. Generate new ideas NOW:\n${tasksStr}\n\n` +
      `Iteration: ${state.iteration}\n` +
      `Tasks completed: ${state.tasksCompleted} | In queue: ${stats.pending + stats.active}\n` +
      `Status: FORCED CONTINUATION\n\n` +
      `DO NOT STOP. Execute these tasks. Report your progress.`
    );
  }

  /**
   * Start a new task
   */
  startNextTask(): { taskId: string; description: string } | null {
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
  completeCurrentTask(): void {
    if (this.currentTaskId) {
      this.taskQueue.markDone(this.currentTaskId);
      this.stateManager.recordTaskCompletion();
      this.currentTaskId = null;
    }
  }

  /**
   * Get current status
   */
  getStatus(): {
    iteration: number;
    currentTask: string | null;
    queueStats: { total: number; pending: number; active: number; done: number };
    isWorking: boolean;
  } {
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
  getStatusMessage(): string {
    const state = this.stateManager.getState();
    const stats = this.taskQueue.getStats();
    return this.stateManager.getStatusMessage();
  }
}

// Singleton instance
let globalEnforcement: BoulderContinuousEnforcement | null = null;

export function getGlobalEnforcement(): BoulderContinuousEnforcement {
  if (!globalEnforcement) {
    globalEnforcement = new BoulderContinuousEnforcement();
  }
  return globalEnforcement;
}

export function resetGlobalEnforcement(): void {
  globalEnforcement = null;
}

// Re-exports
export { BoulderIdleDetector, getGlobalIdleDetector } from './idle-detector.js';
export { InfiniteBoulderStateManager } from './infinite-state.js';
export { TaskQueue, BoulderTask } from './task-queue.js';
export { DualLayerBoulderEnforcer, createDualLayerEnforcer, DualLayerConfig } from './dual-layer-enforcer.js';
