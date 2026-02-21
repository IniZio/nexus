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
export declare class TaskQueue {
    private tasks;
    private taskDir;
    private taskFile;
    private globalIteration;
    private taskIdCounter;
    constructor(taskDir?: string);
    private ensureDirectory;
    private generateId;
    private generateTask;
    private loadFromDisk;
    private saveToDisk;
    addTask(task: BoulderTask): void;
    getNextTask(): BoulderTask | null;
    private areDependenciesMet;
    markDone(taskId: string): void;
    pauseTask(taskId: string): void;
    resumeTask(taskId: string): void;
    ensureMinimumTasks(count: number): void;
    getStats(): {
        total: number;
        pending: number;
        active: number;
        done: number;
        paused: number;
    };
    getTaskById(taskId: string): BoulderTask | undefined;
    getAllTasks(): BoulderTask[];
    getTasksByStatus(status: TaskStatus): BoulderTask[];
    getTasksByCategory(category: TaskCategory): BoulderTask[];
    addDependency(taskId: string, dependencyId: string): void;
    removeDependency(taskId: string, dependencyId: string): void;
    updatePriority(taskId: string, priority: number): void;
    clearDoneTasks(): void;
    reset(): void;
}
export declare const taskQueue: TaskQueue;
//# sourceMappingURL=task-queue.d.ts.map