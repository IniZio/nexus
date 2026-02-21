import { ExecutionContext, ValidationResult, EnforcerRules } from '../types.js';
import { BoulderStateManager } from '../boulder/state.js';

export interface PromptGenerator {
  generatePrompt(
    phase: 'before' | 'after',
    context: ExecutionContext,
    data: { result?: ValidationResult; rules?: EnforcerRules }
  ): string;
}

export class PromptGeneratorImpl implements PromptGenerator {
  private boulder: BoulderStateManager;

  constructor() {
    this.boulder = BoulderStateManager.getInstance();
  }

  generatePrompt(
    phase: 'before' | 'after',
    context: ExecutionContext,
    data: { result?: ValidationResult; rules?: EnforcerRules }
  ): string {
    const boulderState = this.boulder.getState();
    
    if (phase === 'before') {
      return this.generateBeforePrompt(context, data.rules, boulderState);
    } else {
      return this.generateAfterPrompt(context, data.result, boulderState);
    }
  }

  private generateBeforePrompt(
    context: ExecutionContext,
    rules?: EnforcerRules,
    boulderState?: { iteration: number; canComplete: boolean; status: string }
  ): string {
    const _iteration = boulderState?.iteration || 1;
    const boulderMsg = this.boulder.getEnforcementMessage();
    
    return `[NEXUS ENFORCER] Starting task validation...

${boulderMsg}

Task: ${context.taskDescription}
Agent: ${context.agentType}
Workspace: ${context.workspacePath}

Remember: The boulder never stops rolling.
Always look for improvements. Never declare completion prematurely.
`;
  }

  private generateAfterPrompt(
    context: ExecutionContext,
    result?: ValidationResult,
    boulderState?: { iteration: number; canComplete: boolean; status: string }
  ): string {
    if (!result) {
      return `[NEXUS ENFORCER] Validation complete. No result provided.`;
    }

    const iteration = result.iteration || boulderState?.iteration || 1;
    const boulderStatus = result.boulderStatus || boulderState?.status || 'FORCED_CONTINUATION';
    const canComplete = result.canComplete ?? boulderState?.canComplete ?? false;

    let output = `[NEXUS ENFORCER] Validation Results\n`;
    output += `Iteration: ${iteration}\n`;
    output += `Status: ${boulderStatus}\n`;
    output += `Can Complete: ${canComplete ? 'YES' : 'NO'}\n`;
    output += `Overall Score: ${result.overallScore}/100\n`;
    output += `Passed: ${result.passed ? '✓' : '✗'}\n\n`;

    if (result.checks.length > 0) {
      output += `Issues Found:\n`;
      result.checks.forEach((check) => {
        const icon = check.severity === 'error' ? '✗' : check.severity === 'warning' ? '⚠' : 'ℹ';
        output += `  ${icon} [${check.ruleId}] ${check.message}\n`;
        if (check.remediation) {
          output += `    → ${check.remediation}\n`;
        }
      });
      output += '\n';
    }

    if (result.recommendations.length > 0) {
      output += `Recommendations:\n`;
      result.recommendations.forEach((rec) => {
        output += `  • ${rec}\n`;
      });
      output += '\n';
    }

    if (result.improvementTasks && result.improvementTasks.length > 0) {
      output += `Improvement Tasks (The boulder NEVER stops):\n`;
      result.improvementTasks.forEach((task, i) => {
        output += `  ${i + 1}. ${task}\n`;
      });
      output += '\n';
    }

    if (!canComplete) {
      output += `⚠ BOULDER ENFORCEMENT: Completion not allowed yet.\n`;
      output += `   Continue improving. The boulder never stops.\n`;
    }

    return output;
  }
}

export function createPromptGenerator(): PromptGenerator {
  return new PromptGeneratorImpl();
}
