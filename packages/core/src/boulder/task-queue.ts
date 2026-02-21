import * as fs from 'fs';
import * as path from 'path';

export type TaskStatus = 'pending' | 'active' | 'paused' | 'done';
export type TaskCategory = 'testing' | 'refinement' | 'documentation' | 'robustness' | 'exploration';

export interface BoulderTask {
  id: string;
  description: string;
  iteration: number;
  status: TaskStatus;
  dependencies: string[];
  createdAt: number;
  lastActiveAt: number;
  category: TaskCategory;
  priority: number;
}

interface TaskPool {
  [category: string]: string[];
}

const TASK_POOLS: TaskPool = {
  testing: [
    'Add unit tests for untested functions',
    'Increase test coverage for module X',
    'Write integration tests for API endpoints',
    'Add property-based tests for core algorithms',
    'Create test fixtures for edge cases',
    'Add error handling tests',
    'Implement test doubles for external services',
    'Add performance benchmarks',
    'Create test scenarios for error conditions',
    'Verify edge case handling in validation'
  ],
  refinement: [
    'Refactor complex function into smaller units',
    'Simplify conditional logic using early returns',
    'Extract repeated code into utility functions',
    'Rename variables for clarity',
    'Break down large functions',
    'Consolidate similar validation logic',
    'Simplify nested data structures',
    'Improve error message clarity',
    'Reduce function parameter count',
    'Streamline configuration handling'
  ],
  documentation: [
    'Add JSDoc comments to undocumented functions',
    'Update API documentation',
    'Create architecture overview document',
    'Add examples in docstrings',
    'Document complex business logic',
    'Update README with new features',
    'Add inline code comments',
    'Create troubleshooting guide',
    'Document configuration options',
    'Add decision records for key choices'
  ],
  robustness: [
    'Add input validation for edge cases',
    'Implement proper error handling',
    'Add null/undefined checks',
    'Handle race conditions in async code',
    'Add timeout handling for external calls',
    'Implement retry logic for flaky operations',
    'Add resource cleanup in error paths',
    'Validate file paths and permissions',
    'Add logging for debugging failures',
    'Implement circuit breaker pattern'
  ],
  exploration: [
    'Investigate performance bottlenecks',
    'Explore alternative implementation approaches',
    'Research better error handling patterns',
    'Evaluate third-party library alternatives',
    'Analyze dependency updates',
    'Review security implications of dependencies',
    'Explore incremental computation opportunities',
    'Investigate caching strategies',
    'Review API design consistency',
    'Analyze bundle size optimization'
  ]
};

export class TaskQueue {
  private tasks: Map<string, BoulderTask>;
  private taskDir: string;
  private taskFile: string;
  private globalIteration: number;
  private taskIdCounter: number;

  constructor(taskDir: string = '.nexus/boulder') {
    this.tasks = new Map();
    this.taskDir = taskDir;
    this.taskFile = path.join(this.taskDir, 'tasks.json');
    this.globalIteration = 1;
    this.taskIdCounter = 0;
    this.ensureDirectory();
    this.loadFromDisk();
  }

  private ensureDirectory(): void {
    if (!fs.existsSync(this.taskDir)) {
      fs.mkdirSync(this.taskDir, { recursive: true });
    }
  }

  private generateId(): string {
    this.taskIdCounter++;
    return `task-${Date.now()}-${this.taskIdCounter}`;
  }

  private generateTask(category: TaskCategory): BoulderTask {
    const pool = TASK_POOLS[category];
    const description = pool[Math.floor(Math.random() * pool.length)];
    
    return {
      id: this.generateId(),
      description,
      iteration: this.globalIteration++,
      status: 'pending',
      dependencies: [],
      createdAt: Date.now(),
      lastActiveAt: Date.now(),
      category,
      priority: Math.floor(Math.random() * 100)
    };
  }

  private loadFromDisk(): void {
    if (fs.existsSync(this.taskFile)) {
      try {
        const data = fs.readFileSync(this.taskFile, 'utf-8');
        const parsed = JSON.parse(data);
        this.globalIteration = parsed.globalIteration || 1;
        this.taskIdCounter = parsed.taskIdCounter || 0;
        
        if (Array.isArray(parsed.tasks)) {
          for (const task of parsed.tasks) {
            this.tasks.set(task.id, task);
          }
        }
      } catch (error) {
        console.error('Failed to load tasks from disk:', error);
      }
    }
  }

  private saveToDisk(): void {
    try {
      const data = JSON.stringify({
        globalIteration: this.globalIteration,
        taskIdCounter: this.taskIdCounter,
        tasks: Array.from(this.tasks.values())
      }, null, 2);
      fs.writeFileSync(this.taskFile, data, 'utf-8');
    } catch (error) {
      console.error('Failed to save tasks to disk:', error);
    }
  }

  addTask(task: BoulderTask): void {
    task.createdAt = Date.now();
    task.lastActiveAt = Date.now();
    this.tasks.set(task.id, task);
    this.saveToDisk();
  }

  getNextTask(): BoulderTask | null {
    const candidates: BoulderTask[] = [];
    
    for (const task of this.tasks.values()) {
      if (task.status === 'pending' && this.areDependenciesMet(task)) {
        candidates.push(task);
      }
    }
    
    if (candidates.length === 0) {
      return null;
    }
    
    candidates.sort((a, b) => {
      if (b.priority !== a.priority) {
        return b.priority - a.priority;
      }
      return a.createdAt - b.createdAt;
    });
    
    const selected = candidates[0];
    selected.status = 'active';
    selected.lastActiveAt = Date.now();
    this.saveToDisk();
    
    return selected;
  }

  private areDependenciesMet(task: BoulderTask): boolean {
    for (const depId of task.dependencies) {
      const depTask = this.tasks.get(depId);
      if (!depTask || depTask.status !== 'done') {
        return false;
      }
    }
    return true;
  }

  markDone(taskId: string): void {
    const task = this.tasks.get(taskId);
    if (task) {
      task.status = 'done';
      task.lastActiveAt = Date.now();
      this.saveToDisk();
    }
  }

  pauseTask(taskId: string): void {
    const task = this.tasks.get(taskId);
    if (task && task.status === 'active') {
      task.status = 'paused';
      task.lastActiveAt = Date.now();
      this.saveToDisk();
    }
  }

  resumeTask(taskId: string): void {
    const task = this.tasks.get(taskId);
    if (task && task.status === 'paused') {
      task.status = 'pending';
      task.lastActiveAt = Date.now();
      this.saveToDisk();
    }
  }

  ensureMinimumTasks(count: number): void {
    let pendingCount = 0;
    for (const task of this.tasks.values()) {
      if (task.status === 'pending') {
        pendingCount++;
      }
    }
    
    while (pendingCount < count) {
      const categories: TaskCategory[] = ['testing', 'refinement', 'documentation', 'robustness', 'exploration'];
      const category = categories[Math.floor(Math.random() * categories.length)];
      const task = this.generateTask(category);
      this.addTask(task);
      pendingCount++;
    }
  }

  getStats(): { total: number; pending: number; active: number; done: number; paused: number } {
    let total = 0;
    let pending = 0;
    let active = 0;
    let done = 0;
    let paused = 0;
    
    for (const task of this.tasks.values()) {
      total++;
      switch (task.status) {
        case 'pending': pending++; break;
        case 'active': active++; break;
        case 'done': done++; break;
        case 'paused': paused++; break;
      }
    }
    
    return { total, pending, active, done, paused };
  }

  getTaskById(taskId: string): BoulderTask | undefined {
    return this.tasks.get(taskId);
  }

  getAllTasks(): BoulderTask[] {
    return Array.from(this.tasks.values());
  }

  getTasksByStatus(status: TaskStatus): BoulderTask[] {
    return Array.from(this.tasks.values()).filter(task => task.status === status);
  }

  getTasksByCategory(category: TaskCategory): BoulderTask[] {
    return Array.from(this.tasks.values()).filter(task => task.category === category);
  }

  addDependency(taskId: string, dependencyId: string): void {
    const task = this.tasks.get(taskId);
    if (task && !task.dependencies.includes(dependencyId)) {
      task.dependencies.push(dependencyId);
      this.saveToDisk();
    }
  }

  removeDependency(taskId: string, dependencyId: string): void {
    const task = this.tasks.get(taskId);
    if (task) {
      task.dependencies = task.dependencies.filter(dep => dep !== dependencyId);
      this.saveToDisk();
    }
  }

  updatePriority(taskId: string, priority: number): void {
    const task = this.tasks.get(taskId);
    if (task) {
      task.priority = priority;
      this.saveToDisk();
    }
  }

  clearDoneTasks(): void {
    for (const [id, task] of this.tasks) {
      if (task.status === 'done') {
        this.tasks.delete(id);
      }
    }
    this.saveToDisk();
  }

  reset(): void {
    this.tasks.clear();
    this.globalIteration = 1;
    this.taskIdCounter = 0;
    this.saveToDisk();
  }
}

export const taskQueue = new TaskQueue();
