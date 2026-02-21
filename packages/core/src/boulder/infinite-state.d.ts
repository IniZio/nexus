export interface InfiniteBoulderState {
    iteration: number;
    sessionStartTime: number;
    totalWorkTimeMs: number;
    tasksCompleted: number;
    tasksCreated: number;
    status: 'CONTINUOUS';
    lastActivity: number;
}
export declare class InfiniteBoulderStateManager {
    private state;
    private taskQueue;
    constructor();
    private initializeTaskQueue;
    incrementIteration(): void;
    private extendTaskQueue;
    recordTaskCompletion(): void;
    recordActivity(): void;
    getState(): InfiniteBoulderState;
    getNextTasks(count: number): string[];
    getStatusMessage(): string;
}
export declare const infiniteBoulderState: InfiniteBoulderStateManager;
//# sourceMappingURL=infinite-state.d.ts.map