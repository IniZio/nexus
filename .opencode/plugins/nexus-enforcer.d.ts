import * as types from 'nexus-enforcer/types';
import { ExecutionContext, ValidationResult, EnforcerConfig } from 'nexus-enforcer/types';
export interface OpenCodePlugin {
    validateBefore: (context: Partial<ExecutionContext>) => Promise<ValidationResult>;
    validateAfter: (context: Partial<ExecutionContext>) => Promise<ValidationResult>;
    getStatus: () => {
        enabled: boolean;
        strictMode: boolean;
        config: EnforcerConfig;
    };
    setEnabled: (enabled: boolean) => void;
    setStrictMode: (strict: boolean) => void;
}
export declare function createOpenCodePlugin(configPath?: string, overridesPath?: string): OpenCodePlugin;
export { types };
export type { ExecutionContext, ValidationResult, EnforcerConfig };
//# sourceMappingURL=index.d.ts.map