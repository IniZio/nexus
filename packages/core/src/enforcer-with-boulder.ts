import { ValidationEngine, createValidationEngine } from './engine/checker.js';
import { PromptGenerator, createPromptGenerator } from './prompts/generator.js';
import {
  ExecutionContext,
  ValidationResult,
  EnforcerConfig,
  EnforcerRules,
  WorkspaceInfo,
  FailedCheck,
  Todo,
} from './types.js';
import { BoulderStateManager } from './boulder/state.js';

export class NexusEnforcer {
  private engine: ValidationEngine;
  private generator: PromptGenerator;
  private config: EnforcerConfig;
  private boulder: BoulderStateManager;

  constructor(configPath?: string, overridesPath?: string) {
    this.engine = createValidationEngine(configPath, overridesPath);
    this.generator = createPromptGenerator();
    this.config = this.engine.getEffectiveConfig();
    this.boulder = BoulderStateManager.getInstance();
  }

  validateBefore(context: Partial<ExecutionContext>): ValidationResult {
    const fullContext: ExecutionContext = {
      workspacePath: context.workspacePath || process.cwd(),
      workingDirectory: context.workingDirectory || process.cwd(),
      currentFile: context.currentFile,
      currentFunction: context.currentFunction,
      agentType: context.agentType || 'custom',
      taskDescription: context.taskDescription || '',
      timestamp: new Date(),
      environment: context.environment || {},
    };

    const result = this.engine.validate(fullContext);
    const prompt = this.generator.generatePrompt('before', fullContext, { result, rules: this.config.rules });

    console.log(prompt);

    return result;
  }

  validateAfter(context: Partial<ExecutionContext>): ValidationResult {
    const fullContext: ExecutionContext = {
      workspacePath: context.workspacePath || process.cwd(),
      workingDirectory: context.workingDirectory || process.cwd(),
      currentFile: context.currentFile,
      currentFunction: context.currentFunction,
      agentType: context.agentType || 'custom',
      taskDescription: context.taskDescription || '',
      timestamp: new Date(),
      environment: context.environment || {},
    };

    const result = this.engine.validate(fullContext);
    const prompt = this.generator.generatePrompt('after', fullContext, { result });

    console.log(prompt);

    return result;
  }

  /**
   * Enforce continuous improvement - call when completion is attempted
   * Throws error if completion is not allowed yet
   */
  enforceCompletion(): void {
    if (!this.boulder.canComplete()) {
      const state = this.boulder.getState();
      throw new Error(`BOULDER ENFORCEMENT: Cannot complete yet. Required: ${5} iterations, Current: ${state.iteration}. Keep improving!`);
    }
    this.boulder.recordCompletionAttempt();
  }

  /**
   * Check if completion is allowed
   */
  canComplete(): boolean {
    return this.boulder.canComplete();
  }

  /**
   * Get current boulder state
   */
  getBoulderState() {
    return this.boulder.getState();
  }

  /**
   * Reset boulder state
   */
  resetBoulder(): void {
    this.boulder.reset();
  }

  generatePrompt(phase: 'before' | 'after', context: ExecutionContext): string {
    return this.generator.generatePrompt(phase, context, {
      rules: this.config.rules,
    });
  }

  getWorkspaceInfo(workspacePath: string): WorkspaceInfo {
    return this.engine.getWorkspaceInfo(workspacePath);
  }

  getConfig(): EnforcerConfig {
    return this.config;
  }

  getEffectiveConfig(): EnforcerConfig {
    return this.engine.getEffectiveConfig();
  }
}

export function createNexusEnforcer(configPath?: string, overridesPath?: string): NexusEnforcer {
  return new NexusEnforcer(configPath, overridesPath);
}

export type {
  ExecutionContext,
  ValidationResult,
  EnforcerConfig,
  EnforcerRules,
  WorkspaceInfo,
  FailedCheck,
  Todo,
};
