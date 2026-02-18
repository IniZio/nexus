import { ValidationEngine, createValidationEngine } from 'nexus-enforcer/engine';
import { PromptGenerator, createPromptGenerator } from 'nexus-enforcer/prompts';
import * as types from 'nexus-enforcer/types';
import {
  ExecutionContext,
  ValidationResult,
  EnforcerConfig,
} from 'nexus-enforcer/types';

export interface ClaudePlugin {
  validateBefore: (context: Partial<ExecutionContext>) => Promise<ValidationResult>;
  validateAfter: (context: Partial<ExecutionContext>) => Promise<ValidationResult>;
  getStatus: () => { enabled: boolean; strictMode: boolean; config: EnforcerConfig };
  setEnabled: (enabled: boolean) => void;
  setStrictMode: (strict: boolean) => void;
  autoInstall: () => Promise<boolean>;
}

export interface ClaudeHooks {
  'Task:before': (input: { workspacePath: string; taskDescription: string }) => Promise<ValidationResult>;
  'Task:after': (input: { workspacePath: string; taskDescription: string }) => Promise<ValidationResult>;
}

export function createClaudePlugin(
  configPath?: string,
  overridesPath?: string
): ClaudePlugin {
  const engine = createValidationEngine(configPath, overridesPath);
  const generator = createPromptGenerator();

  let enabled = true;
  let strictMode = false;
  let verbose = false;

  async function runValidation(
    phase: 'before' | 'after',
    context: Partial<ExecutionContext>
  ): Promise<ValidationResult> {
    if (!enabled) {
      return {
        passed: true,
        checks: [],
        overallScore: 100,
        recommendations: [],
        executionTime: 0,
      };
    }

    const fullContext: ExecutionContext = {
      workspacePath: context.workspacePath || process.cwd(),
      workingDirectory: context.workingDirectory || process.cwd(),
      currentFile: context.currentFile,
      currentFunction: context.currentFunction,
      agentType: 'claude',
      taskDescription: context.taskDescription || '',
      timestamp: new Date(),
      environment: context.environment || {},
    };

    const result = engine.validate(fullContext);

    if (verbose) {
      const prompt = generator.generatePrompt(phase, fullContext, { result });
      console.log(prompt);
    }

    return result;
  }

  return {
    async validateBefore(context: Partial<ExecutionContext>): Promise<ValidationResult> {
      return runValidation('before', context);
    },

    async validateAfter(context: Partial<ExecutionContext>): Promise<ValidationResult> {
      return runValidation('after', context);
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

    async autoInstall(): Promise<boolean> {
      try {
        const { execSync } = require('child_process');
        execSync('npm install -g nexus-claude', { stdio: 'inherit' });
        return true;
      } catch {
        return false;
      }
    },
  };
}

export function createClaudeHooks(
  configPath?: string,
  overridesPath?: string
): ClaudeHooks {
  const plugin = createClaudePlugin(configPath, overridesPath);

  return {
    'Task:before': async (input: { workspacePath: string; taskDescription: string }) => {
      return plugin.validateBefore({
        workspacePath: input.workspacePath,
        taskDescription: input.taskDescription,
      });
    },

    'Task:after': async (input: { workspacePath: string; taskDescription: string }) => {
      return plugin.validateAfter({
        workspacePath: input.workspacePath,
        taskDescription: input.taskDescription,
      });
    },
  };
}

export type { ExecutionContext, ValidationResult, EnforcerConfig };
