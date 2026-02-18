export interface ExecutionContext {
  workspacePath: string;
  workingDirectory: string;
  currentFile?: string;
  currentFunction?: string;
  agentType: 'opencode' | 'claude' | 'cursor' | 'custom';
  taskDescription: string;
  timestamp: Date;
  environment: Record<string, string>;
}

export interface ValidationResult {
  passed: boolean;
  checks: FailedCheck[];
  overallScore: number;
  recommendations: string[];
  executionTime: number;
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
  status: 'pending' | 'in_progress' | 'completed';
  priority: 'high' | 'medium' | 'low';
  createdAt: Date;
  completedAt?: Date;
}

export interface EnforcerConfig {
  version: string;
  rules: EnforcerRules;
  adaptive: boolean;
  agentSpecificPrompts: boolean;
  strictMode: boolean;
}

export interface EnforcerRules {
  workspace: WorkspaceRule;
  dogfooding: DogfoodingRule;
  completion: CompletionRule;
  quality: QualityRule[];
}

export interface WorkspaceRule {
  required: boolean;
  enforceWorkspace: boolean;
  enforceIsolation: boolean;
}

export interface DogfoodingRule {
  required: boolean;
  logFriction: boolean;
  testChanges: boolean;
}

export interface CompletionRule {
  required: boolean;
  verifyTests: boolean;
  verifyBuild: boolean;
  verifyLint: boolean;
  verifyTypecheck: boolean;
}

export interface QualityRule {
  id: string;
  name: string;
  description: string;
  enabled: boolean;
  severity: 'error' | 'warning' | 'info';
  category: string;
}

export interface LocalOverrides {
  overrides: Record<string, Partial<EnforcerRules>>;
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

export interface AdaptiveConfig {
  enabled: boolean;
  learningRate: number;
  decayFactor: number;
  minConfidence: number;
}
