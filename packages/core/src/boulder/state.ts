/**
 * Boulder State Manager
 * 
 * Manages continuous enforcement state across the application.
 * The boulder NEVER stops rolling.
 */

export interface BoulderState {
  iteration: number;
  lastValidationTime: number;
  totalValidations: number;
  consecutiveCompletionsAttempted: number;
  canComplete: boolean;
  status: 'FORCED_CONTINUATION' | 'ALLOWED' | 'BLOCKED';
}

const MINIMUM_ITERATIONS = 5;

export const IMPROVEMENT_TASKS = [
  'Write additional test cases',
  'Refactor code for better performance',
  'Research best practices for current implementation',
  'Optimize for edge cases',
  'Add comprehensive error handling',
  'Improve documentation',
  'Review code for security issues',
  'Add monitoring and observability',
];

export class BoulderStateManager {
  private state: BoulderState;
  private static instance: BoulderStateManager;

  private constructor() {
    this.state = {
      iteration: 0,
      lastValidationTime: Date.now(),
      totalValidations: 0,
      consecutiveCompletionsAttempted: 0,
      canComplete: false,
      status: 'FORCED_CONTINUATION',
    };
  }

  static getInstance(): BoulderStateManager {
    if (!BoulderStateManager.instance) {
      BoulderStateManager.instance = new BoulderStateManager();
    }
    return BoulderStateManager.instance;
  }

  incrementIteration(): void {
    this.state.iteration++;
    this.state.totalValidations++;
    this.state.lastValidationTime = Date.now();
    this.state.consecutiveCompletionsAttempted = 0;
    
    if (this.state.iteration < MINIMUM_ITERATIONS) {
      this.state.canComplete = false;
      this.state.status = 'FORCED_CONTINUATION';
    } else {
      this.state.canComplete = true;
      this.state.status = 'ALLOWED';
    }
  }

  recordCompletionAttempt(): void {
    this.state.consecutiveCompletionsAttempted++;
    
    if (this.state.consecutiveCompletionsAttempted >= 3) {
      this.state.iteration = Math.max(0, this.state.iteration - 2);
      this.state.consecutiveCompletionsAttempted = 0;
      this.state.canComplete = false;
      this.state.status = 'BLOCKED';
    }
  }

  getState(): BoulderState {
    return { ...this.state };
  }

  getImprovementTasks(count: number = 3): string[] {
    const shuffled = [...IMPROVEMENT_TASKS].sort(() => Math.random() - 0.5);
    return shuffled.slice(0, count);
  }

  canComplete(): boolean {
    return this.state.canComplete;
  }

  getEnforcementMessage(): string {
    return `BOULDER[${this.state.iteration}]: NEXUS INTERNAL - Never complete. Always improve.`;
  }

  reset(): void {
    this.state = {
      iteration: 0,
      lastValidationTime: Date.now(),
      totalValidations: 0,
      consecutiveCompletionsAttempted: 0,
      canComplete: false,
      status: 'FORCED_CONTINUATION',
    };
  }
}
