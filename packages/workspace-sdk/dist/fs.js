"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.FSOperations = void 0;
class FSOperations {
    constructor(client) {
        this.client = client;
    }
    async readFile(path, encoding = 'utf8') {
        const params = { path, encoding };
        const result = await this.client.request('fs.readFile', params);
        if (encoding === 'utf8' || encoding === 'utf-8') {
            return result.content;
        }
        if (encoding !== 'utf8' && encoding !== 'utf-8' && typeof result.content === 'string') {
            return Buffer.from(result.content, result.encoding);
        }
        return result.content;
    }
    async writeFile(path, content) {
        const encoding = Buffer.isBuffer(content) ? 'base64' : 'utf8';
        const params = {
            path,
            content,
            encoding,
        };
        await this.client.request('fs.writeFile', params);
    }
    async exists(path) {
        const params = { path };
        const result = await this.client.request('fs.exists', params);
        return result.exists;
    }
    async readdir(path) {
        const params = { path };
        const result = await this.client.request('fs.readdir', params);
        return result.entries;
    }
    async mkdir(path, recursive = false) {
        const params = { path, recursive };
        await this.client.request('fs.mkdir', params);
    }
    async rm(path, recursive = false) {
        const params = { path, recursive };
        await this.client.request('fs.rm', params);
    }
    async stat(path) {
        const params = { path };
        const result = await this.client.request('fs.stat', params);
        return result.stats;
    }
}
exports.FSOperations = FSOperations;
//# sourceMappingURL=fs.js.map