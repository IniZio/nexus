import * as fs from 'fs';
import * as path from 'path';
import deepmerge from 'deepmerge';
import {
  ExecutionContext,
  ValidationResult,
  FailedCheck,
  WorkspaceInfo,
  EnforcerConfig,
  EnforcerRules,
  LocalOverrides,
} from '../types.js';

export class ValidationEngine {
  private baseConfig: EnforcerConfig;
  private localOverrides?: LocalOverrides;

  constructor(configPath?: string, overridesPath?: string) {
    this.baseConfig = this.loadBaseConfig(configPath);
    this.localOverrides = this.loadOverrides(overridesPath);
  }

  private loadBaseConfig(configPath?: string): EnforcerConfig {
    const defaultPath = path.resolve(process.cwd(), '.nexus/enforcer-rules.json');
    const finalPath = configPath || defaultPath;

    if (fs.existsSync(finalPath)) {
      const content = fs.readFileSync(finalPath, 'utf-8');
      return JSON.parse(content) as EnforcerConfig;
    }

    return {
      version: '1.0.0',
      rules: {
        workspace: { required: true, enforceWorkspace: true, enforceIsolation: true },
        dogfooding: { required: true, logFriction: true, testChanges: true },
        completion: { required: true, verifyTests: true, verifyBuild: true, verifyLint: true, verifyTypecheck: true },
        quality: [],
      },
      adaptive: true,
      agentSpecificPrompts: true,
      strictMode: false,
    };
  }

  private loadOverrides(overridesPath?: string): LocalOverrides | undefined {
    const defaultPath = path.resolve(process.cwd(), '.nexus/enforcer-rules.local.json');
    const finalPath = overridesPath || defaultPath;

    if (fs.existsSync(finalPath)) {
      const content = fs.readFileSync(finalPath, 'utf-8');
      return JSON.parse(content) as LocalOverrides;
    }

    return undefined;
  }

  getEffectiveConfig(): EnforcerConfig {
    if (!this.localOverrides) {
      return this.baseConfig;
    }

    const mergedRules = deepmerge(this.baseConfig.rules, this.localOverrides.overrides);

    return {
      ...this.baseConfig,
      rules: mergedRules as EnforcerRules,
    };
  }

  getWorkspaceInfo(workspacePath: string): WorkspaceInfo {
    const gitPath = path.join(workspacePath, '.git');
    const nexusPath = path.join(workspacePath, '.nexus');
    const enforcerRulesPath = path.join(nexusPath, 'enforcer-rules.json');
    const localOverridesPath = path.join(nexusPath, 'enforcer-rules.local.json');

    const isGitRepository = fs.existsSync(gitPath);
    let branch: string | undefined;
    let commitHash: string | undefined;

    if (isGitRepository) {
      try {
        const gitHeadPath = path.join(workspacePath, '.git', 'HEAD');
        if (fs.existsSync(gitHeadPath)) {
          const headContent = fs.readFileSync(gitHeadPath, 'utf-8').trim();
          if (headContent.startsWith('ref: ')) {
            branch = headContent.replace('ref: ', '').split('/').pop() || 'unknown';
          }
        }

        const gitLogsPath = path.join(workspacePath, '.git', 'logs', 'HEAD');
        if (fs.existsSync(gitLogsPath)) {
          const logsContent = fs.readFileSync(gitLogsPath, 'utf-8');
          const lastLine = logsContent.split('\n').filter(Boolean).pop();
          if (lastLine) {
            commitHash = lastLine.split(' ')[1];
          }
        }
      } catch {
        // Ignore git errors
      }
    }

    let modifiedFiles: string[] = [];
    let untrackedFiles: string[] = [];

    if (isGitRepository) {
      try {
        const { execSync } = require('child_process');
        modifiedFiles = execSync('git diff --name-only', { cwd: workspacePath, encoding: 'utf-8' })
          .split('\n')
          .filter(Boolean);
        untrackedFiles = execSync('git ls-files --others --exclude-standard', { cwd: workspacePath, encoding: 'utf-8' })
          .split('\n')
          .filter(Boolean);
      } catch {
        // Ignore git errors
      }
    }

    return {
      workspacePath,
      isGitRepository,
      branch,
      commitHash,
      modifiedFiles,
      untrackedFiles,
      hasNexusDirectory: fs.existsSync(nexusPath),
      hasEnforcerRules: fs.existsSync(enforcerRulesPath),
      hasLocalOverrides: fs.existsSync(localOverridesPath),
    };
  }

  validateWorkspace(workspaceInfo: WorkspaceInfo, rules: EnforcerRules['workspace']): FailedCheck[] {
    const failures: FailedCheck[] = [];

    if (rules.required && !workspaceInfo.hasNexusDirectory) {
      failures.push({
        ruleId: 'workspace-001',
        ruleName: 'Nexus Directory Required',
        severity: 'error',
        message: 'Missing .nexus directory',
        expected: '.nexus directory should exist',
        actual: 'No .nexus directory found',
        remediation: 'Create .nexus directory with enforcer-rules.json',
      });
    }

    if (rules.enforceWorkspace && !workspaceInfo.isGitRepository) {
      failures.push({
        ruleId: 'workspace-002',
        ruleName: 'Git Repository Required',
        severity: 'warning',
        message: 'Not a git repository',
        expected: 'Should be a git repository for version control',
        actual: 'No .git directory found',
        remediation: 'Initialize git repository: git init',
      });
    }

    if (rules.enforceIsolation && workspaceInfo.modifiedFiles.length === 0 && workspaceInfo.untrackedFiles.length === 0) {
      failures.push({
        ruleId: 'workspace-003',
        ruleName: 'No Changes Detected',
        severity: 'warning',
        message: 'No modified or untracked files found',
        expected: 'Should have changes in progress',
        actual: 'No changes detected',
        remediation: 'Ensure you are working on changes',
      });
    }

    return failures;
  }

  validateDogfooding(workspaceInfo: WorkspaceInfo, rules: EnforcerRules['dogfooding'], _frictionLogPath?: string): FailedCheck[] {
    const failures: FailedCheck[] = [];

    if (rules.required && rules.logFriction) {
      const dogfoodingPath = path.join(workspaceInfo.workspacePath, '.nexus', 'dogfooding', 'friction-log.md');

      if (!fs.existsSync(dogfoodingPath)) {
        failures.push({
          ruleId: 'dogfooding-001',
          ruleName: 'Friction Log Required',
          severity: 'warning',
          message: 'Missing friction log',
          expected: 'friction-log.md should exist',
          actual: 'No friction log found',
          remediation: 'Create .nexus/dogfooding/friction-log.md',
        });
      }
    }

    return failures;
  }

  validateCompletion(context: ExecutionContext, rules: EnforcerRules['completion']): FailedCheck[] {
    const failures: FailedCheck[] = [];

    if (rules.required) {
      if (rules.verifyTests) {
        failures.push({
          ruleId: 'completion-001',
          ruleName: 'Tests Required',
          severity: 'error',
          message: 'Tests must pass before completion',
          remediation: 'Run test suite to verify all tests pass',
        });
      }

      if (rules.verifyBuild) {
        failures.push({
          ruleId: 'completion-002',
          ruleName: 'Build Required',
          severity: 'error',
          message: 'Build must succeed before completion',
          remediation: 'Run build command to verify compilation',
        });
      }

      if (rules.verifyLint) {
        failures.push({
          ruleId: 'completion-003',
          ruleName: 'Lint Required',
          severity: 'warning',
          message: 'No lint errors allowed',
          remediation: 'Run linter to check for issues',
        });
      }

      if (rules.verifyTypecheck) {
        failures.push({
          ruleId: 'completion-004',
          ruleName: 'TypeCheck Required',
          severity: 'error',
          message: 'No type errors allowed',
          remediation: 'Run type checker to verify types',
        });
      }
    }

    return failures;
  }

  validate(context: ExecutionContext): ValidationResult {
    const startTime = Date.now();
    const config = this.getEffectiveConfig();
    const workspaceInfo = this.getWorkspaceInfo(context.workspacePath);

    const allFailures: FailedCheck[] = [];

    allFailures.push(...this.validateWorkspace(workspaceInfo, config.rules.workspace));
    allFailures.push(...this.validateDogfooding(workspaceInfo, config.rules.dogfooding));
    allFailures.push(...this.validateCompletion(context, config.rules.completion));

    const errorCount = allFailures.filter(f => f.severity === 'error').length;
    const warningCount = allFailures.filter(f => f.severity === 'warning').length;
    const overallScore = Math.max(0, 100 - (errorCount * 20) - (warningCount * 5));

    const recommendations = allFailures
      .filter(f => f.severity === 'error')
      .map(f => f.remediation || f.message);

    return {
      passed: errorCount === 0,
      checks: allFailures,
      overallScore,
      recommendations,
      improvementTasks: [],
      executionTime: Date.now() - startTime,
      isValid: errorCount === 0,
      errors: allFailures.map(f => f.message),
      iteration: 0,
      boulderStatus: 'CONTINUOUS' as const,
      currentTask: null,
      queueStats: { total: 0, pending: 0, active: 0, done: 0 },
      timestamp: new Date(),
    };
  }
}

export function createValidationEngine(configPath?: string, overridesPath?: string): ValidationEngine {
  return new ValidationEngine(configPath, overridesPath);
}
