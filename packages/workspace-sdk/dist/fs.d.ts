import { WorkspaceClient } from './client';
import { FSStatResult } from './types';
export declare class FSOperations {
    private client;
    constructor(client: WorkspaceClient);
    readFile(path: string, encoding?: string): Promise<string | Buffer>;
    writeFile(path: string, content: string | Buffer): Promise<void>;
    exists(path: string): Promise<boolean>;
    readdir(path: string): Promise<string[]>;
    mkdir(path: string, recursive?: boolean): Promise<void>;
    rm(path: string, recursive?: boolean): Promise<void>;
    stat(path: string): Promise<FSStatResult['stats']>;
}
//# sourceMappingURL=fs.d.ts.map