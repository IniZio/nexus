import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import zlib from 'node:zlib';
import { promisify } from 'node:util';
import { buildConfigBundle } from '../bundle';

const gunzip = promisify(zlib.gunzip);

async function decodeTar(encoded: string): Promise<Map<string, Buffer>> {
  const compressed = Buffer.from(encoded, 'base64');
  const raw = await gunzip(compressed);
  const files = new Map<string, Buffer>();
  let offset = 0;
  while (offset < raw.length - 1024) {
    const name = raw.subarray(offset, offset + 100).toString('utf8').replace(/\0+$/, '').trim();
    if (!name) break;
    const sizeStr = raw.subarray(offset + 124, offset + 136).toString('ascii').replace(/\0+$/, '').trim();
    const size = parseInt(sizeStr, 8) || 0;
    offset += 512;
    if (size > 0) {
      files.set(name, raw.subarray(offset, offset + size));
      offset += Math.ceil(size / 512) * 512;
    }
  }
  return files;
}

describe('buildConfigBundle', () => {
  let tmpHome: string;

  beforeEach(() => {
    tmpHome = fs.mkdtempSync(path.join(os.tmpdir(), 'nexus-bundle-test-'));
  });

  afterEach(() => {
    fs.rmSync(tmpHome, { recursive: true, force: true });
  });

  it('returns empty string when home has no known credential files', async () => {
    const result = await buildConfigBundle(tmpHome);
    expect(result).toBe('');
  });

  it('includes a known credential file in the bundle', async () => {
    fs.mkdirSync(path.join(tmpHome, '.codex'), { recursive: true });
    fs.writeFileSync(path.join(tmpHome, '.codex', 'auth.json'), '{"token":"test-token"}');

    const bundle = await buildConfigBundle(tmpHome);
    expect(bundle).toBeTruthy();

    const files = await decodeTar(bundle);
    const content = files.get('.codex/auth.json');
    expect(content).toBeDefined();
    expect(content!.toString()).toBe('{"token":"test-token"}');
  });

  it('includes files from a known credential directory', async () => {
    const skillsDir = path.join(tmpHome, '.codex', 'skills');
    fs.mkdirSync(skillsDir, { recursive: true });
    fs.writeFileSync(path.join(skillsDir, 'my-skill.md'), '# My Skill');

    const bundle = await buildConfigBundle(tmpHome);
    expect(bundle).toBeTruthy();

    const files = await decodeTar(bundle);
    const found = [...files.keys()].some(k => k.includes('skills') && k.includes('my-skill.md'));
    expect(found).toBe(true);
  });

  it('produces valid base64 gzip', async () => {
    fs.mkdirSync(path.join(tmpHome, '.codex'), { recursive: true });
    fs.writeFileSync(path.join(tmpHome, '.codex', 'auth.json'), '{}');

    const bundle = await buildConfigBundle(tmpHome);
    expect(() => Buffer.from(bundle, 'base64')).not.toThrow();
    const raw = Buffer.from(bundle, 'base64');
    await expect(gunzip(raw)).resolves.toBeDefined();
  });

  it('uses os.homedir() when no homeDir argument is provided', async () => {
    await expect(buildConfigBundle()).resolves.not.toThrow();
  });
});
