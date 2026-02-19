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
  BoulderStatus,
} from './types.js';
import { BoulderContinuousEnforcement, getGlobalEnforcement } from './boulder/index.js';

export class BoulderEnforcementError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'BoulderEnforcementError';
  }
}

export interface InfiniteValidationResult extends Omit<ValidationResult, 'boulderStatus'> {
  boulderStatus: BoulderStatus;
  infiniteMode: boolean;
  completionAttempted: boolean;
}

export class NexusEnforcerInfinite {
  private engine: ValidationEngine;
  private generator: PromptGenerator;
  private config: EnforcerConfig;
  private boulder: BoulderContinuousEnforcement;
  private toolCallCount: Map<string, number>;
  private lastCompletionAttempt: Date | null;

  constructor(configPath?: string, overridesPath?: string) {
    this.engine = createValidationEngine(configPath, overridesPath);
    this.generator = createPromptGenerator();
    this.config = this.engine.getEffectiveConfig();
    this.boulder = getGlobalEnforcement();
    this.toolCallCount = new Map();
    this.lastCompletionAttempt = null;
  }

  validateBefore(context: Partial<ExecutionContext>): InfiniteValidationResult {
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
    const boulderStatus = this.boulder.getStatus().isWorking ? 'CONTINUOUS' : 'FORCED_CONTINUATION';

    const infiniteResult: InfiniteValidationResult = {
      ...result,
      boulderStatus,
      infiniteMode: true,
      completionAttempted: false,
    };

    return infiniteResult;
  }

  validateAfter(context: Partial<ExecutionContext>): InfiniteValidationResult {
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
    const boulderStatus = this.boulder.getStatus().isWorking ? 'CONTINUOUS' : 'FORCED_CONTINUATION';

    const infiniteResult: InfiniteValidationResult = {
      ...result,
      boulderStatus,
      infiniteMode: true,
      completionAttempted: false,
    };

    return infiniteResult;
  }

  recordToolCall(toolName: string): void {
    const count = this.toolCallCount.get(toolName) || 0;
    this.toolCallCount.set(toolName, count + 1);
  }

  getInfiniteStatus(): BoulderStatus {
    const status = this.boulder.getStatus();
    return status.isWorking ? 'CONTINUOUS' : 'FORCED_CONTINUATION';
  }

  getToolCallStats(): Record<string, number> {
    return Object.fromEntries(this.toolCallCount);
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

export function createNexusEnforcerInfinite(configPath?: string, overridesPath?: string): NexusEnforcerInfinite {
  return new NexusEnforcerInfinite(configPath, overridesPath);
}

export type {
  ExecutionContext,
  ValidationResult,
  EnforcerConfig,
  EnforcerRules,
  WorkspaceInfo,
  FailedCheck,
  Todo,
  BoulderStatus,
};
