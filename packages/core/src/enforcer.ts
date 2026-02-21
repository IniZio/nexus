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

export class NexusEnforcer {
  private engine: ValidationEngine;
  private generator: PromptGenerator;
  private config: EnforcerConfig;

  constructor(configPath?: string, overridesPath?: string) {
    this.engine = createValidationEngine(configPath, overridesPath);
    this.generator = createPromptGenerator();
    this.config = this.engine.getEffectiveConfig();
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
    const prompt = this.generator.generatePrompt('before', fullContext, { result });

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
