export class BoulderEnforcementError extends Error {
    iteration;
    currentTask;
    queueStats;
    constructor(message, iteration, currentTask, queueStats) {
        super(message);
        this.iteration = iteration;
        this.currentTask = currentTask;
        this.queueStats = queueStats;
        this.name = 'BoulderEnforcementError';
    }
}
//# sourceMappingURL=types.js.map