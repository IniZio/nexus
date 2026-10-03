export const SANDBOX_TOOL_PREFIX = 'mcp__nexus-subagent__'
export const LIST_MAX_BYTES = 64 * 1024

export type SandboxTool = { name: string; description: string; inputSchema: Record<string, unknown> }

const str = (description: string) => ({ type: 'string', description })
const num = (description: string) => ({ type: 'integer', minimum: 0, description })
const bool = (description: string) => ({ type: 'boolean', description })
const REF = str('sandbox reference: exact ID, ID prefix, or project/name handle')
const ARGV = { type: 'array', items: { type: 'string' }, description: 'command and arguments to run in the guest (required)' }
const ENV = { type: 'object', additionalProperties: { type: 'string' }, description: 'additional environment variables as key→value map (optional)' }
const object = (properties: Record<string, unknown>, required: string[]): Record<string, unknown> => ({ type: 'object', properties, required })

export const SANDBOX_TOOLS: SandboxTool[] = [
  {
    name: 'sandbox_create',
    description: "Create a sandbox. Without image fields: mint a record in state 'created'. " +
      "With rootfs_path, digest, or ref: create and boot in one step, returning state 'running'. " +
      'Returns the sandbox as JSON.',
    inputSchema: object({
      project: str('the project name (required)'),
      name: str('the sandbox name (required)'),
      remove_on_exit: bool('remove sandbox when its primary command exits'),
      rootfs_path: str('direct path to a raw ext4 rootfs file on the server (optional; triggers boot)'),
      digest: str('sha256:<hex> image digest in the server image cache (optional; triggers boot)'),
      ref: str('image tag or digest string in the server image cache (optional; triggers boot)'),
      memory_mib: num('guest RAM in MiB (optional; 0 = driver default 512 MiB)'),
      vcpus: num('number of virtual CPUs (optional; 0 = driver default 1)'),
      motive: str("motive ID to associate this sandbox with (optional; '' = unassociated)"),
      nested_virt: bool('expose /dev/kvm inside guest (optional; default false)'),
    }, ['project', 'name', 'remove_on_exit']),
  },
  {
    name: 'sandbox_list',
    description: 'List all sandboxes. Returns a JSON array of sandbox objects. ' +
      'Large lists are capped at 64 KiB; check truncated.bytes_omitted.',
    inputSchema: object({}, []),
  },
  {
    name: 'sandbox_start',
    description: 'Start a created or stopped sandbox. Returns the updated sandbox as JSON.',
    inputSchema: object({ ref: REF }, ['ref']),
  },
  {
    name: 'sandbox_stop',
    description: 'Stop a running sandbox. Returns the updated sandbox as JSON.',
    inputSchema: object({ ref: REF }, ['ref']),
  },
  {
    name: 'sandbox_remove',
    description: 'Remove a sandbox. Returns {"removed":true} on success.',
    inputSchema: object({ ref: REF }, ['ref']),
  },
  {
    name: 'sandbox_exec',
    description: 'Execute a command in an existing running sandbox. ' +
      'Returns {exit_code, stdout, stderr}.',
    inputSchema: object({
      ref: REF,
      argv: ARGV,
      env: ENV,
      cwd: str('working directory inside the guest (optional; default: agent default)'),
      stdin: str("data to pipe to the command's stdin (optional)"),
    }, ['ref', 'argv']),
  },
  {
    name: 'sandbox_run',
    description: 'Create a sandbox, boot it, execute a command, remove it. ' +
      'Returns {exit_code, stdout, stderr}. The sandbox is removed even on error. ' +
      'Needs ref or digest (an image).',
    inputSchema: object({
      project: str('project name (required)'),
      name: str('sandbox name (required)'),
      digest: str('sha256:<hex> image digest in the server image cache (optional)'),
      ref: str('image tag or digest string in the server image cache (optional)'),
      memory_mib: num('guest RAM in MiB (optional; 0 = driver default 512 MiB)'),
      vcpus: num('number of virtual CPUs (optional; 0 = driver default 1)'),
      argv: ARGV,
      env: ENV,
      cwd: str('working directory inside the guest (optional)'),
      stdin: str("data to pipe to the command's stdin (optional)"),
    }, ['project', 'name', 'argv']),
  },
]

export const SANDBOX_TOOL_NAMES = SANDBOX_TOOLS.map(t => t.name)

export type SandboxCall = { argv: string[]; timeoutMs: number; stdin?: string; json: boolean } | { error: string }
export type CliRun = { exitCode: number; stdout: string; stderr: string }
export type SandboxOutcome = { result: string } | { deny: string }

const s = (v: unknown): string => (typeof v === 'string' ? v : '')
const n = (v: unknown): number => (typeof v === 'number' && isFinite(v) && v > 0 ? Math.floor(v) : 0)
const b = (v: unknown): boolean => v === true
const strs = (v: unknown): string[] => (Array.isArray(v) && v.every(x => typeof x === 'string') ? (v as string[]) : [])
const envPairs = (v: unknown): string[] => {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return []
  const out: string[] = []
  for (const [k, val] of Object.entries(v as Record<string, unknown>)) {
    if (typeof val === 'string') out.push(`${k}=${val}`)
  }
  return out
}

export function buildSandboxCall(tool: string, args: Record<string, unknown>): SandboxCall {
  const a = args ?? {}
  switch (tool) {
    case 'sandbox_list':
      return { argv: ['nexus', '--json', 'ls'], timeoutMs: 60_000, json: true }
    case 'sandbox_start':
    case 'sandbox_stop':
    case 'sandbox_remove': {
      const ref = s(a.ref)
      if (!ref) return { error: 'ref is required' }
      const verb = tool === 'sandbox_start' ? 'start' : tool === 'sandbox_stop' ? 'stop' : 'rm'
      return { argv: ['nexus', '--json', verb, ref], timeoutMs: 300_000, json: true }
    }
    case 'sandbox_pause':
    case 'sandbox_resume':
      return { error: `${tool} is not supported: the nexus CLI has no pause/resume verb for plain sandboxes` }
    case 'sandbox_create': {
      const project = s(a.project)
      const name = s(a.name)
      if (!project || !name) return { error: 'project and name are required' }
      const argv = ['nexus', '--json', 'sandbox', 'create', `${project}/${name}`]
      if (b(a.remove_on_exit)) argv.push('--rm')
      const image = s(a.ref) || s(a.digest)
      if (s(a.rootfs_path)) argv.push('--rootfs', s(a.rootfs_path))
      else if (image) argv.push('--image', image)
      if (n(a.memory_mib)) argv.push('--memory', String(n(a.memory_mib)))
      if (n(a.vcpus)) argv.push('--vcpus', String(n(a.vcpus)))
      if (s(a.motive)) argv.push('--label', `motive=${s(a.motive)}`)
      if (b(a.nested_virt)) argv.push('--nested')
      return { argv, timeoutMs: 600_000, json: true }
    }
    case 'sandbox_exec': {
      const ref = s(a.ref)
      if (!ref) return { error: 'ref is required' }
      const guest = strs(a.argv)
      if (guest.length === 0) return { error: 'argv is required' }
      const env = envPairs(a.env)
      const cmd = env.length ? ['env', ...env, ...guest] : guest
      const argv = ['nexus', 'exec']
      if (s(a.cwd)) argv.push('--cwd', s(a.cwd))
      argv.push(ref, '--', ...cmd)
      const call: SandboxCall = { argv, timeoutMs: 600_000, json: false }
      if (s(a.stdin)) call.stdin = s(a.stdin)
      return call
    }
    case 'sandbox_run': {
      const project = s(a.project)
      const name = s(a.name)
      if (!project || !name) return { error: 'project and name are required' }
      const guest = strs(a.argv)
      if (guest.length === 0) return { error: 'argv is required' }
      if (s(a.rootfs_path)) return { error: 'sandbox_run: rootfs_path is not supported through the nexus CLI; pass ref or digest' }
      if (b(a.nested_virt)) return { error: 'sandbox_run: nested_virt is not supported through the nexus CLI run verb; use sandbox_create + sandbox_exec' }
      const image = s(a.ref) || s(a.digest)
      if (!image) return { error: 'sandbox_run needs ref or digest (an image)' }
      const env = envPairs(a.env)
      let cmd = env.length ? ['env', ...env, ...guest] : guest
      if (s(a.cwd)) cmd = ['sh', '-c', 'cd -- "$1" && shift && exec "$@"', 'sh', s(a.cwd), ...cmd]
      const argv = ['nexus', 'run', '--project', project, '--name', name]
      if (n(a.memory_mib)) argv.push('--memory', String(n(a.memory_mib)))
      if (n(a.vcpus)) argv.push('--vcpus', String(n(a.vcpus)))
      argv.push(image, '--', ...cmd)
      const call: SandboxCall = { argv, timeoutMs: 600_000, json: false }
      if (s(a.stdin)) call.stdin = s(a.stdin)
      return call
    }
    default:
      return { error: `unknown sandbox tool ${tool}` }
  }
}

const utf8Len = (str: string): number => {
  let len = 0
  for (let i = 0; i < str.length; i++) {
    const c = str.charCodeAt(i)
    if (c < 0x80) len += 1
    else if (c < 0x800) len += 2
    else if (c >= 0xd800 && c <= 0xdbff) { len += 4; i++ }
    else len += 3
  }
  return len
}

export function capList(list: unknown[], maxBytes = LIST_MAX_BYTES): string {
  const full = JSON.stringify(list)
  const total = utf8Len(full)
  if (total <= maxBytes) return full
  let lo = 1
  let hi = list.length
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2)
    if (utf8Len(JSON.stringify(list.slice(0, mid))) <= maxBytes) lo = mid
    else hi = mid - 1
  }
  const kept = list.slice(0, lo)
  return JSON.stringify({ sandboxes: kept, truncated: { bytes_omitted: total - utf8Len(JSON.stringify(kept)), total_bytes: total } })
}

const PICK_KEYS = ['id', 'project', 'name', 'handle', 'state', 'remove_on_exit', 'stop_reason']

const pick = (v: unknown): Record<string, unknown> => {
  const out: Record<string, unknown> = {}
  if (!v || typeof v !== 'object') return out
  const o = v as Record<string, unknown>
  for (const k of PICK_KEYS) {
    if (o[k] !== undefined && o[k] !== '' && o[k] !== false) out[k] = o[k]
  }
  return out
}

const parseEnvelope = (stdout: string): Record<string, any> | undefined => {
  const lines = stdout.split('\n')
  for (let i = lines.length - 1; i >= 0; i--) {
    const line = lines[i].trim()
    if (!line.startsWith('{')) continue
    try {
      const v = JSON.parse(line)
      if (v && typeof v === 'object') return v
    } catch {}
  }
  return undefined
}

const tail = (text: string): string => (text.length > 1500 ? text.slice(-1500) : text)

export function formatSandboxResult(tool: string, call: Extract<SandboxCall, { argv: string[] }>, r: CliRun): SandboxOutcome {
  if (!call.json) return { result: JSON.stringify({ exit_code: r.exitCode, stdout: r.stdout, stderr: r.stderr }) }
  const env = parseEnvelope(r.stdout)
  if (env?.kind === 'error' || (r.exitCode !== 0 && !env)) {
    const msg = (typeof env?.error?.message === 'string' && env.error.message) || r.stderr.trim() || r.stdout.trim() || `nexus exited ${r.exitCode}`
    return { deny: tail(msg) }
  }
  if (!env) return { deny: `nexus returned non-JSON output: ${r.stdout.slice(0, 400)}` }
  if (tool === 'sandbox_list') {
    const list = Array.isArray(env.data?.sandboxes) ? env.data.sandboxes : []
    return { result: capList(list.map(pick)) }
  }
  if (tool === 'sandbox_remove') return { result: '{"removed":true}' }
  return { result: JSON.stringify(pick(env.data)) }
}
