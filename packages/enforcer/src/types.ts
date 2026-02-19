export interface BoulderContinuousState {
  isActive: boolean;
  iteration: number;
  startTime: Date;
  lastActivity: Date;
}

export interface QueueStats {
  total: number;
  pending: number;
  active: number;
  done: number;
}

export interface ExecutionContext {
  workspacePath: string;
  workingDirectory: string;
  currentFile?: string;
  currentFunction?: string;
  agentType: string;
  taskDescription: string;
  timestamp: Date;
  environment: Record<string, unknown>;
}

export interface FailedCheck {
  ruleId: string;
  ruleName: string;
  severity: 'error' | 'warning' | 'info';
  message: string;
  expected?: string;
  actual?: string;
  remediation?: string;
}

export interface WorkspaceInfo {
  workspacePath: string;
  isGitRepository: boolean;
  branch?: string;
  commitHash?: string;
  modifiedFiles: string[];
  untrackedFiles: string[];
  hasNexusDirectory: boolean;
  hasEnforcerRules: boolean;
  hasLocalOverrides: boolean;
}

export interface Todo {
  id: string;
  content: string;
  status: 'pending' | 'in-progress' | 'done';
  priority: number;
}

export interface EnforcerRulesWorkspace {
  required: boolean;
  enforceWorkspace: boolean;
  enforceIsolation: boolean;
}

export interface EnforcerRulesDogfooding {
  required: boolean;
  logFriction: boolean;
  testChanges: boolean;
}

export interface EnforcerRulesCompletion {
  required: boolean;
  verifyTests: boolean;
  verifyBuild: boolean;
  verifyLint: boolean;
  verifyTypecheck: boolean;
}

export interface EnforcerRules {
  workspace: EnforcerRulesWorkspace;
  dogfooding: EnforcerRulesDogfooding;
  completion: EnforcerRulesCompletion;
  quality: unknown[];
}

export interface EnforcerConfig {
  version: string;
  rules: EnforcerRules;
  adaptive: boolean;
  agentSpecificPrompts: boolean;
  strictMode: boolean;
}

export interface LocalOverrides {
  overrides: Partial<EnforcerRules>;
}

export interface AgentPromptConfig {
  prefix: string;
  suffix: string;
  formatting: {
    useEmojis: boolean;
    useBold: boolean;
    useCodeBlocks: boolean;
  };
  sections: string[];
}

export interface DualLayerConfig {
  idleThresholdMs: number;
  checkIntervalMs: number;
  completionKeywords: string[];
  workIndicators: string[];
}

export interface ValidationResult {
  passed: boolean;
  checks: FailedCheck[];
  overallScore: number;
  recommendations: string[];
  improvementTasks: string[];
  executionTime: number;
  isValid: boolean;
  errors: string[];
  iteration: number;
  boulderStatus: BoulderStatus;
  currentTask: string | null;
  queueStats: QueueStats;
  canComplete?: boolean;
  timestamp: Date;
}

export type BoulderStatus = 'CONTINUOUS' | 'FORCED_CONTINUATION' | 'ALLOWED' | 'BLOCKED';

export interface InfiniteValidationResult {
  passed: boolean;
  improvementTasks: string[];
  currentTask: string | null;
  iteration: number;
  queueStats: QueueStats;
  isWorking: boolean;
  completionAttempted: boolean;
}

export class BoulderEnforcementError extends Error {
  constructor(
    message: string,
    public readonly iteration: number,
    public readonly currentTask: string | null,
    public readonly queueStats: QueueStats
  ) {
    super(message);
    this.name = 'BoulderEnforcementError';
  }
}

export type LegacyValidationResult = Omit<ValidationResult, 'canComplete'>;
