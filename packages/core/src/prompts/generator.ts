import {
  ExecutionContext,
  ValidationResult,
  AgentPromptConfig,
  FailedCheck,
} from '../types.js';

export class PromptGenerator {
  private agentConfigs: Record<string, AgentPromptConfig>;

  constructor() {
    this.agentConfigs = {
      opencode: {
        prefix: '[NEXUS ENFORCER]',
        suffix: '[END NEXUS]',
        formatting: {
          useEmojis: false,
          useBold: true,
          useCodeBlocks: true,
        },
        sections: ['context', 'rules', 'validation', 'recommendations'],
      },
      claude: {
        prefix: '⚠️ Nexus Enforcement',
        suffix: '🔒 End of enforcement check',
        formatting: {
          useEmojis: true,
          useBold: true,
          useCodeBlocks: true,
        },
        sections: ['context', 'rules', 'validation', 'recommendations', 'remediation'],
      },
      cursor: {
        prefix: '📋 Nexus Rules',
        suffix: '✓ Verification complete',
        formatting: {
          useEmojis: true,
          useBold: false,
          useCodeBlocks: true,
        },
        sections: ['context', 'rules', 'validation', 'recommendations'],
      },
      custom: {
        prefix: '[NEXUS]',
        suffix: '[/NEXUS]',
        formatting: {
          useEmojis: false,
          useBold: true,
          useCodeBlocks: true,
        },
        sections: ['context', 'rules', 'validation', 'recommendations'],
      },
    };
  }

  generateBeforePrompt(context: ExecutionContext, rules: Record<string, unknown>): string {
    const config = this.agentConfigs[context.agentType] || this.agentConfigs.custom;

    const sections: string[] = [];

    if (config.sections.includes('context')) {
      sections.push(this.formatSection('Context', this.formatContext(context)));
    }

    if (config.sections.includes('rules')) {
      sections.push(this.formatSection('Enforcement Rules', this.formatRules(rules)));
    }

    sections.push(this.formatSection('Requirements', this.formatRequirements()));

    const formatted = sections.join('\n\n');
    return `${config.prefix}\n\n${formatted}\n\n${config.suffix}`;
  }

  generateAfterPrompt(context: ExecutionContext, result: ValidationResult): string {
    const config = this.agentConfigs[context.agentType] || this.agentConfigs.custom;

    const sections: string[] = [];

    if (config.sections.includes('validation')) {
      sections.push(this.formatSection('Validation Result', this.formatValidationResult(result)));
    }

    if (config.sections.includes('recommendations') && result.recommendations.length > 0) {
      sections.push(this.formatSection('Recommended Actions', this.formatRecommendations(result.recommendations)));
    }

    if (config.sections.includes('remediation')) {
      sections.push(this.formatSection('Remediation Steps', this.formatRemediations(result.checks)));
    }

    const formatted = sections.join('\n\n');
    return `${config.prefix}\n\n${formatted}\n\n${config.suffix}`;
  }

  generatePrompt(phase: 'before' | 'after', context: ExecutionContext, data: Record<string, unknown>): string {
    if (phase === 'before') {
      return this.generateBeforePrompt(context, data.rules as Record<string, unknown>);
    }
    return this.generateAfterPrompt(context, data.result as ValidationResult);
  }

  private formatContext(context: ExecutionContext): string {
    return [
      `Workspace: ${context.workspacePath}`,
      `Working Directory: ${context.workingDirectory}`,
      `Agent Type: ${context.agentType}`,
      `Task: ${context.taskDescription}`,
      `Timestamp: ${context.timestamp.toISOString()}`,
    ].join('\n');
  }

  private formatRules(rules: Record<string, unknown>): string {
    return Object.entries(rules)
      .map(([key, value]) => {
        if (typeof value === 'object' && value !== null) {
          return `- ${key}: ${JSON.stringify(value, null, 2)}`;
        }
        return `- ${key}: ${value}`;
      })
      .join('\n');
  }

  private formatRequirements(): string {
    return [
      '1. Verify workspace isolation',
      '2. Check dogfooding requirements',
      '3. Validate completion criteria',
      '4. Ensure all quality gates pass',
    ].join('\n');
  }

  private formatValidationResult(result: ValidationResult): string {
    const status = result.passed ? '✅ PASSED' : '❌ FAILED';
    const score = `Score: ${result.overallScore}/100`;

    const checkSummary = [
      `Total Checks: ${result.checks.length}`,
      `Errors: ${result.checks.filter(c => c.severity === 'error').length}`,
      `Warnings: ${result.checks.filter(c => c.severity === 'warning').length}`,
      `Info: ${result.checks.filter(c => c.severity === 'info').length}`,
    ].join('\n');

    const checkDetails = result.checks.map(check =>
      this.formatCheck(check)
    ).join('\n');

    return [status, score, '\n' + checkSummary, '\nDetails:', checkDetails].join('\n');
  }

  private formatCheck(check: FailedCheck): string {
    const icon = check.severity === 'error' ? '❌' : check.severity === 'warning' ? '⚠️' : 'ℹ️';
    return [
      `${icon} [${check.ruleId}] ${check.ruleName}`,
      `   Message: ${check.message}`,
    ].join('\n');
  }

  private formatRecommendations(recommendations: string[]): string {
    return recommendations.map((rec, i) => `${i + 1}. ${rec}`).join('\n');
  }

  private formatRemediations(checks: FailedCheck[]): string {
    const remediations = checks.filter(c => c.remediation);

    if (remediations.length === 0) {
      return 'No specific remediation steps required.';
    }

    return remediations.map((check, i) => {
      return `${i + 1}. ${check.ruleName}\n   ${check.remediation}`;
    }).join('\n\n');
  }

  private formatSection(title: string, content: string): string {
    const config = this.agentConfigs.default || this.agentConfigs.custom;
    const formattedTitle = config.formatting.useBold ? `**${title}**` : title;
    return `${formattedTitle}\n${content}`;
  }

  getAgentConfig(agentType: string): AgentPromptConfig {
    return this.agentConfigs[agentType] || this.agentConfigs.custom;
  }

  setAgentConfig(agentType: string, config: Partial<AgentPromptConfig>): void {
    this.agentConfigs[agentType] = {
      ...this.agentConfigs.custom,
      ...config,
    };
  }
}

export function createPromptGenerator(): PromptGenerator {
  return new PromptGenerator();
}
