// Enforcer Types with Boulder Continuous Enforcement

export interface ExecutionContext {
  workspacePath: string;
  workingDirectory: string;
  currentFile?: string;
  currentFunction?: string;
  agentType: string;
  taskDescription: string;
  timestamp: Date;
  environment: Record<string, string>;
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

export interface ValidationResult {
  passed: boolean;
  checks: FailedCheck[];
  overallScore: number;
  recommendations: string[];
  executionTime: number;
  // Boulder continuous enforcement fields
  iteration?: number;
  boulderStatus?: 'FORCED_CONTINUATION' | 'ALLOWED' | 'BLOCKED';
  canComplete?: boolean;
  improvementTasks?: string[];
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

export interface WorkspaceRules {
  required: boolean;
  enforceWorkspace: boolean;
  enforceIsolation: boolean;
}

export interface DogfoodingRules {
  required: boolean;
  logFriction: boolean;
  testChanges: boolean;
}

export interface CompletionRules {
  required: boolean;
  verifyTests: boolean;
  verifyBuild: boolean;
  verifyLint: boolean;
  verifyTypecheck: boolean;
}

export interface QualityRule {
  id: string;
  name: string;
  enabled: boolean;
  severity: 'error' | 'warning';
}

export interface EnforcerRules {
  workspace: WorkspaceRules;
  dogfooding: DogfoodingRules;
  completion: CompletionRules;
  quality: QualityRule[];
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

export interface Todo {
  id: string;
  description: string;
  priority: 'high' | 'medium' | 'low';
  status: 'pending' | 'in_progress' | 'completed' | 'cancelled';
}
