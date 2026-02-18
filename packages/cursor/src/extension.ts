import { ValidationEngine, createValidationEngine } from 'nexus-enforcer/engine';
import { PromptGenerator, createPromptGenerator } from 'nexus-enforcer/prompts';
import * as types from 'nexus-enforcer/types';
import {
  ExecutionContext,
  ValidationResult,
  EnforcerConfig,
} from 'nexus-enforcer/types';

export interface CursorExtension {
  validateBefore: (context: Partial<ExecutionContext>) => Promise<ValidationResult>;
  validateAfter: (context: Partial<ExecutionContext>) => Promise<ValidationResult>;
  getStatus: () => { enabled: boolean; strictMode: boolean; config: EnforcerConfig };
  setEnabled: (enabled: boolean) => void;
  setStrictMode: (strict: boolean) => void;
  onDidChangeTextDocument: (event: { document: { uri: { fsPath: string } } }) => void;
  onDidSaveTextDocument: (event: { document: { uri: { fsPath: string } } }) => void;
}

export function createCursorExtension(
  configPath?: string,
  overridesPath?: string
): CursorExtension {
  const engine = createValidationEngine(configPath, overridesPath);
  const generator = createPromptGenerator();

  let enabled = true;
  let strictMode = false;

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
      agentType: 'cursor',
      taskDescription: context.taskDescription || '',
      timestamp: new Date(),
      environment: context.environment || {},
    };

    const result = engine.validate(fullContext);

    const prompt = generator.generatePrompt(phase, fullContext, { result });
    console.log(prompt);

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

    onDidChangeTextDocument(event: { document: { uri: { fsPath: string } } }): void {
      const filePath = event.document.uri.fsPath;
      console.log(`[NEXUS] File changed: ${filePath}`);
    },

    onDidSaveTextDocument(event: { document: { uri: { fsPath: string } } }): void {
      const filePath = event.document.uri.fsPath;
      console.log(`[NEXUS] File saved: ${filePath}`);
    },
  };
}

export function activate(): CursorExtension {
  return createCursorExtension();
}

export function deactivate(): void {
  console.log('[NEXUS] Extension deactivated');
}

export type { ExecutionContext, ValidationResult, EnforcerConfig };
