import { WorkspaceClient } from './client';
import { ExecOptions, ExecResult } from './types';
export declare class ExecOperations {
    private client;
    constructor(client: WorkspaceClient);
    exec(command: string, args?: string[], options?: ExecOptions): Promise<ExecResult>;
}
//# sourceMappingURL=exec.d.ts.map