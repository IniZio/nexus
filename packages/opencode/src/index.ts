import { ValidationEngine, createValidationEngine } from 'nexus-enforcer/engine';
import { PromptGenerator, createPromptGenerator } from 'nexus-enforcer/prompts';
import * as types from 'nexus-enforcer/types';
import {
  ExecutionContext,
  ValidationResult,
  EnforcerConfig,
} from 'nexus-enforcer/types';

export interface OpenCodePlugin {
  validateBefore: (context: Partial<ExecutionContext>) => Promise<ValidationResult>;
  validateAfter: (context: Partial<ExecutionContext>) => Promise<ValidationResult>;
  getStatus: () => { enabled: boolean; strictMode: boolean; config: EnforcerConfig };
  setEnabled: (enabled: boolean) => void;
  setStrictMode: (strict: boolean) => void;
}

function createEmptyValidationResult(): ValidationResult {
  return {
    passed: true,
    checks: [],
    overallScore: 100,
    recommendations: [],
    improvementTasks: [],
    executionTime: 0,
    isValid: true,
    errors: [],
    iteration: 0,
    boulderStatus: 'CONTINUOUS',
    currentTask: null,
    queueStats: { total: 0, pending: 0, active: 0, done: 0 },
    timestamp: new Date(),
  };
}

export function createOpenCodePlugin(
  configPath?: string,
  overridesPath?: string
): OpenCodePlugin {
  const engine = createValidationEngine(configPath, overridesPath);
  const generator = createPromptGenerator();

  let enabled = true;
  let strictMode = false;

  return {
    async validateBefore(context: Partial<ExecutionContext>): Promise<ValidationResult> {
      if (!enabled) {
        return createEmptyValidationResult();
      }

      const fullContext: ExecutionContext = {
        workspacePath: context.workspacePath || process.cwd(),
        workingDirectory: context.workingDirectory || process.cwd(),
        currentFile: context.currentFile,
        currentFunction: context.currentFunction,
        agentType: 'opencode',
        taskDescription: context.taskDescription || '',
        timestamp: new Date(),
        environment: context.environment || {},
      };

      const prompt = generator.generatePrompt('before', fullContext, {
        rules: engine.getEffectiveConfig().rules,
      });

      console.log(prompt);

      const result = engine.validate(fullContext);

      return result;
    },

    async validateAfter(context: Partial<ExecutionContext>): Promise<ValidationResult> {
      if (!enabled) {
        return createEmptyValidationResult();
      }

      const fullContext: ExecutionContext = {
        workspacePath: context.workspacePath || process.cwd(),
        workingDirectory: context.workingDirectory || process.cwd(),
        currentFile: context.currentFile,
        currentFunction: context.currentFunction,
        agentType: 'opencode',
        taskDescription: context.taskDescription || '',
        timestamp: new Date(),
        environment: context.environment || {},
      };

      const result = engine.validate(fullContext);

      const prompt = generator.generatePrompt('after', fullContext, { result });

      console.log(prompt);

      return result;
    },

    getStatus(): { enabled: boolean; strictMode: boolean; config: EnforcerConfig } {
      return {
        enabled,
        strictMode,
        config: engine.getEffectiveConfig(),
      };
    },

    setEnabled(value: boolean): void {
      enabled = value;
    },

    setStrictMode(value: boolean): void {
      strictMode = value;
    },
  };
}

export { types };
export type { ExecutionContext, ValidationResult, EnforcerConfig };
