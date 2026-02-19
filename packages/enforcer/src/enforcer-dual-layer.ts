import { DualLayerBoulderEnforcer, createDualLayerEnforcer, DualLayerConfig } from './boulder/dual-layer-enforcer.js';
import { ExecutionContext, ValidationResult } from './types.js';

export class EnforcerDualLayer {
  private enforcer: DualLayerBoulderEnforcer;

  constructor(config?: Partial<DualLayerConfig>, onEnforcement?: (message: string) => void) {
    this.enforcer = createDualLayerEnforcer(config, onEnforcement);
  }

  validateBefore(context: Partial<ExecutionContext>): ValidationResult {
    return { passed: true, checks: [], overallScore: 100, recommendations: [], improvementTasks: [], executionTime: 0, isValid: true, errors: [], iteration: 0, boulderStatus: 'CONTINUOUS', currentTask: null, queueStats: { total: 0, pending: 0, active: 0, done: 0 }, timestamp: new Date() };
  }

  validateAfter(context: Partial<ExecutionContext>): ValidationResult {
    return { passed: true, checks: [], overallScore: 100, recommendations: [], improvementTasks: [], executionTime: 0, isValid: true, errors: [], iteration: 0, boulderStatus: 'CONTINUOUS', currentTask: null, queueStats: { total: 0, pending: 0, active: 0, done: 0 }, timestamp: new Date() };
  }

  recordToolCall(toolName: string): void {
    this.enforcer.recordToolCall(toolName);
  }

  checkResponse(text: string): boolean {
    return this.enforcer.checkText(text);
  }

  isBlocked(): boolean {
    const status = this.enforcer.getStatus();
    return status.isEnforcing;
  }

  getEnforcer(): DualLayerBoulderEnforcer {
    return this.enforcer;
  }
}

export function createEnforcerDualLayer(config?: Partial<DualLayerConfig>, onEnforcement?: (message: string) => void): EnforcerDualLayer {
  return new EnforcerDualLayer(config, onEnforcement);
}
