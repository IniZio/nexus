import { WorkspaceClient } from './client.js';
import {
  FSReadFileParams,
  FSWriteFileParams,
  FSExistsParams,
  FSReaddirParams,
  FSMkdirParams,
  FSRmParams,
  FSStatParams,
  FSReadFileResult,
  FSWriteFileResult,
  FSExistsResult,
  FSReaddirResult,
  FSMkdirResult,
  FSRmResult,
  FSStatResult,
} from './types.js';

export class FSOperations {
  private client: WorkspaceClient;

  constructor(client: WorkspaceClient) {
    this.client = client;
  }

  async readFile(path: string, encoding: string = 'utf8'): Promise<string | Buffer> {
    const params: FSReadFileParams = { path, encoding };
    const result = await this.client.request<FSReadFileResult>('fs.readFile', params);

    if (encoding === 'utf8' || encoding === 'utf-8') {
      return result.content as string;
    }

    if (encoding !== 'utf8' && encoding !== 'utf-8' && typeof result.content === 'string') {
      return Buffer.from(result.content, result.encoding as BufferEncoding);
    }

    return result.content as Buffer;
  }

  async writeFile(path: string, content: string | Buffer): Promise<void> {
    const encoding = Buffer.isBuffer(content) ? 'base64' : 'utf8';
    const params: FSWriteFileParams = {
      path,
      content,
      encoding,
    };

    await this.client.request<FSWriteFileResult>('fs.writeFile', params);
  }

  async exists(path: string): Promise<boolean> {
    const params: FSExistsParams = { path };
    const result = await this.client.request<FSExistsResult>('fs.exists', params);
    return result.exists;
  }

  async readdir(path: string): Promise<string[]> {
    const params: FSReaddirParams = { path };
    const result = await this.client.request<FSReaddirResult>('fs.readdir', params);
    return result.entries;
  }

  async mkdir(path: string, recursive: boolean = false): Promise<void> {
    const params: FSMkdirParams = { path, recursive };
    await this.client.request<FSMkdirResult>('fs.mkdir', params);
  }

  async rm(path: string, recursive: boolean = false): Promise<void> {
    const params: FSRmParams = { path, recursive };
    await this.client.request<FSRmResult>('fs.rm', params);
  }

  async stat(path: string): Promise<FSStatResult['stats']> {
    const params: FSStatParams = { path };
    const result = await this.client.request<FSStatResult>('fs.stat', params);
    return result.stats;
  }
}
