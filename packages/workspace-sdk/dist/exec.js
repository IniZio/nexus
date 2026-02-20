"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.ExecOperations = void 0;
class ExecOperations {
    constructor(client) {
        this.client = client;
    }
    async exec(command, args = [], options = {}) {
        const params = {
            command,
            args,
            options,
        };
        const result = await this.client.request('exec', params);
        return {
            stdout: result.stdout,
            stderr: result.stderr,
            exitCode: result.exit_code,
        };
    }
}
exports.ExecOperations = ExecOperations;
//# sourceMappingURL=exec.js.map