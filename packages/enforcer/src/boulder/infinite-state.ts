export interface InfiniteBoulderState {
  iteration: number;
  sessionStartTime: number;
  totalWorkTimeMs: number;
  tasksCompleted: number;
  tasksCreated: number;
  status: 'CONTINUOUS';
  lastActivity: number;
}

export class InfiniteBoulderStateManager {
  private state: InfiniteBoulderState;
  private taskQueue: string[] = [];

  constructor() {
    this.state = {
      iteration: 0,
      sessionStartTime: Date.now(),
      totalWorkTimeMs: 0,
      tasksCompleted: 0,
      tasksCreated: 0,
      status: 'CONTINUOUS' as const,
      lastActivity: Date.now(),
    };
    this.initializeTaskQueue();
  }

  private initializeTaskQueue(): void {
    this.taskQueue = [
      'Review security considerations',
      'Research best practices for current implementation',
      'Refactor code for better performance',
      'Analyze system dependencies',
      'Optimize data structures',
      'Add comprehensive test coverage',
      'Document internal APIs',
      'Profile critical code paths',
      'Identify potential bottlenecks',
      'Implement defensive programming patterns',
    ];
    this.state.tasksCreated = this.taskQueue.length;
  }

  incrementIteration(): void {
    this.state.iteration += 1;
    this.state.lastActivity = Date.now();
    this.extendTaskQueue();
  }

  private extendTaskQueue(): void {
    const newTasks = [
      `Improvement iteration ${this.state.iteration + 1}`,
      `Refine architecture patterns`,
      `Enhance error handling`,
      `Optimize build processes`,
      `Update documentation`,
      `Review code quality metrics`,
      `Performance tuning exercise`,
      `Dependency audit`,
      `Technical debt assessment`,
      `Best practices alignment`,
    ];
    this.taskQueue.push(...newTasks);
    this.state.tasksCreated += newTasks.length;
  }

  recordTaskCompletion(): void {
    this.state.tasksCompleted += 1;
    this.state.totalWorkTimeMs += Date.now() - this.state.lastActivity;
    this.state.lastActivity = Date.now();
  }

  recordActivity(): void {
    this.state.lastActivity = Date.now();
  }

  getState(): InfiniteBoulderState {
    return { ...this.state };
  }

  getNextTasks(count: number): string[] {
    const tasks = this.taskQueue.splice(0, count);
    if (tasks.length < count) {
      this.extendTaskQueue();
      const remaining = count - tasks.length;
      tasks.push(...this.taskQueue.splice(0, remaining));
    }
    return tasks;
  }

  getStatusMessage(): string {
    const elapsed = Date.now() - this.state.sessionStartTime;
    const elapsedSec = Math.floor(elapsed / 1000);
    const mins = Math.floor(elapsedSec / 60);
    const secs = elapsedSec % 60;

    return `BOULDER[${this.state.iteration}]: NEXUS INTERNAL - Continuous improvement mode.
Tasks completed: ${this.state.tasksCompleted} | Tasks in queue: ${this.taskQueue.length}
Session time: ${mins}m ${secs}s | The boulder NEVER stops.`;
  }
}

export const infiniteBoulderState = new InfiniteBoulderStateManager();
