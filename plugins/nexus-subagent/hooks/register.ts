import { update, type EngineInterface, type Register } from 'claude-code'
import type { NexusBinding } from '../types'
import { WORKER_TYPE, guestCommand, route, sliceLines, applyEdit, guestPath, sandboxRefFor, worktreeInPrompt, parseCreated, lastLine, type Created } from './policy'

const BINDINGS = { plugin: 'nexus-subagent', key: 'bindings' } as const

const SPAWN_SCHEMA = {
  type: 'object',
  properties: {
    branch: { type: 'string', description: 'New branch for the worker\'s linked worktree' },
    worktree: {
      type: 'string',
      description: 'Reuse this existing herdr worktree path (sandbox already bound) instead of creating one; branch is then ignored',
    },
    task: { type: 'string', description: 'The complete brief for the worker, as you would write an Agent prompt' },
    description: { type: 'string', description: '3-5 word label for the task' },
    base: { type: 'string', description: 'Base ref for the branch (default: herdr default)' },
    repo_path: { type: 'string', description: 'Host repo checkout open in herdr (default: this session\'s repo)' },
    model: { type: 'string', description: 'Model alias for the worker (default: session subagent default)' },
  },
  required: ['task'],
}

const WORKER_PROMPT = `You are a nexus worker: a subagent whose shell runs inside an isolated
nexus microVM bound to one git worktree.

- Your worktree is mounted writable in the VM at /workspace; Bash starts there.
  Host worktree paths in commands and file tools are translated to /workspace.
- Read, Write and Edit also run inside the VM; search with Bash (rg, grep, find).
- No other tools are available. Do not try to reach the host.
- Commit your work on the worktree's branch when the task asks for it.
- Finish with a short report: what changed, how you verified it, what is left.`

async function createWorktreeSandbox($: EngineInterface, repo: string, branch: string, base?: string): Promise<Created> {
  const argv = ['nexus', 'herdr', 'worktree-create', '--repo', repo, '--branch', branch, ...(base ? ['--base', base] : [])]
  const r = await $.process.run(argv, { timeoutMs: 600_000 })
  if (r.exitCode !== 0) throw new Error(`${argv.join(' ')} exited ${r.exitCode}: ${r.stderr.trim().slice(-1500)}`)
  return parseCreated(lastLine(r.stdout))
}

async function realpath($: EngineInterface, p: string): Promise<string> {
  const r = await $.process.run(['realpath', '-m', '--', p])
  if (r.exitCode !== 0) throw new Error(`realpath ${p}: ${r.stderr.trim()}`)
  return r.stdout.trim()
}

type GuestFile = { text: string } | { missing: true } | { error: string }

async function guestRun($: EngineInterface, b: NexusBinding, argv: string[], stdin?: string) {
  return $.process.run(['nexus', 'exec', '--cwd', '/workspace', b.ref, '--', ...argv], {
    timeoutMs: 120_000,
    ...(stdin === undefined ? {} : { stdin }),
  })
}

async function guestReadFile($: EngineInterface, b: NexusBinding, path: string): Promise<GuestFile> {
  const r = await guestRun($, b, ['cat', '--', path])
  if (r.exitCode !== 0) {
    return /No such file/.test(r.stderr) ? { missing: true } : { error: r.stderr.trim() || `cat exited ${r.exitCode}` }
  }
  if (r.isStdoutTruncated) return { error: `${path} is over 4 MiB; page through it with Bash` }
  if (r.stdout.includes('\u0000')) return { error: `${path} is binary; inspect it with Bash` }
  return { text: r.stdout }
}

async function guestWriteFile($: EngineInterface, b: NexusBinding, path: string, text: string): Promise<string | undefined> {
  const r = await guestRun($, b, ['sh', '-c', 'mkdir -p -- "$(dirname -- "$1")" && cat > "$1"', 'sh', path], text)
  return r.exitCode === 0 ? undefined : r.stderr.trim() || `write exited ${r.exitCode}`
}

async function bindingOf($: EngineInterface, agentId: string): Promise<NexusBinding | undefined> {
  return (await $.state.get(BINDINGS)).value?.[agentId]
}

async function bind($: EngineInterface, agentId: string, b: NexusBinding): Promise<void> {
  await update($, BINDINGS, all => ({ ...(all ?? {}), [agentId]: b }))
}

// Before spawn returns its id, a worker is known only by type, and is refused.
async function isUnboundWorker($: EngineInterface, agentId: string, foreign: Set<string>): Promise<boolean> {
  if (foreign.has(agentId)) return false
  const info = (await $.agent.list()).find(a => a.id === agentId)
  if (info?.type === WORKER_TYPE) return true
  if (info) foreign.add(agentId)
  return false
}

export const register: Register = on => {
  const foreign = new Set<string>()
  const createdRefs = new Map<string, string>()
  const guestBashCalls = new Map<string, string>()

  on('session.start', async ($, e, next) => {
    const started = await next(e)
    await $.agent.register({
      name: 'worker',
      description:
        'Coding agent sandboxed in a nexus microVM. Start the prompt with a line "worktree: <abs path>" naming a herdr worktree (~/.herdr/worktrees/<project>/<name>) whose sandbox is running.',
      prompt: WORKER_PROMPT,
      tools: ['Bash', 'Read', 'Write', 'Edit'],
    })
    await $.tool.register({
      name: 'spawn',
      description:
        'Start a coding subagent in a fresh nexus microVM bound to a new git worktree of this repo. ' +
        'Creates the worktree + sandbox (about 1-2 min), then runs the worker in the background; ' +
        'you are notified when it finishes, and can SendMessage it like any subagent. ' +
        'Use for implementation work that should be isolated from the host and land on its own branch.',
      inputSchema: SPAWN_SCHEMA,
    })
    return started
  })

  on('agent.offer', { agent: WORKER_TYPE }, () => ({ isOffered: false }))

  on('tool.call', { tool: 'mcp__nexus-subagent__spawn' }, async ($, e) => {
    const a = e as unknown as Record<string, string | undefined>
    if (!a.task) return { deny: 'spawn needs task' }
    let created: Created
    if (a.worktree) {
      const ref = sandboxRefFor(await realpath($, a.worktree))
      if (!ref) return { deny: `${a.worktree} is not a herdr worktree path` }
      created = { handle: ref, worktree_path: a.worktree, branch: '(existing)' }
    } else {
      if (!a.branch) return { deny: 'spawn needs branch (or worktree to reuse)' }
      let repo = a.repo_path
      if (!repo) {
        const top = await $.process.run(['git', 'rev-parse', '--show-toplevel'])
        if (top.exitCode !== 0) return { deny: `repo_path not given and session cwd is not a git checkout: ${top.stderr.trim()}` }
        repo = top.stdout.trim()
      }
      $.ui.status(`nexus: creating worktree ${a.branch}…`)
      try {
        created = await createWorktreeSandbox($, repo, a.branch, a.base)
      } catch (err) {
        $.ui.status(undefined)
        return { deny: `worktree/sandbox create failed: ${String(err)}` }
      }
    }
    const root = await realpath($, created.worktree_path)
    createdRefs.set(root, created.handle)
    const spawned = await $.agent.spawn({
      subagentType: WORKER_TYPE,
      description: a.description ?? `nexus ${a.branch}`,
      prompt: `worktree: ${root}\n\n${a.task}`,
      ...(a.model ? { model: a.model } : {}),
    })
    if (spawned.deny !== undefined) return { deny: `worker spawn refused: ${spawned.deny}` }
    return {
      result:
        `Started nexus worker ${spawned.agentId} on sandbox ${created.handle}\n` +
        `worktree: ${root}\nbranch: ${created.branch}\n` +
        'It runs in the background; you will be notified when it finishes.',
    }
  })

  on('agent.spawn', async ($, e, next) => {
    if (e.subagentType !== WORKER_TYPE) return next(e)
    const target = e.cwd ?? worktreeInPrompt(e.prompt)
    if (!target) return { deny: `${WORKER_TYPE} needs a line "worktree: <herdr worktree path>" in its prompt` }
    const root = await realpath($, target)
    const ref = createdRefs.get(root) ?? sandboxRefFor(root)
    if (!ref) return { deny: `${root} is not a herdr worktree path` }
    const probe = await $.process.run(['nexus', 'exec', ref, '--', 'test', '-w', '/workspace'], { timeoutMs: 15_000 })
    if (probe.exitCode !== 0) {
      return { deny: `sandbox ${ref} has no writable /workspace: ${probe.stderr.trim() || probe.stdout.trim()}` }
    }
    const started = await next({ ...e, cwd: root })
    if (started.agentId) {
      await bind($, started.agentId, { ref, root })
      $.ui.status(`nexus worker ${ref}`)
    }
    return started
  })

  on('tool.call', async ($, e, next) => {
    if (!e.agentId) return next(e)
    const r = route(e.tool)
    if (r.kind === 'engine') return next(e)
    const b = await bindingOf($, e.agentId)
    if (!b) {
      return (await isUnboundWorker($, e.agentId, foreign))
        ? { deny: 'nexus worker has no sandbox binding yet; retry once' }
        : next(e)
    }
    if (r.kind === 'deny') return { deny: r.reason }
    if (r.kind === 'guest-bash' && e.tool === 'Bash') {
      const command = guestCommand(b.ref, b.root, e.command)
      guestBashCalls.set(e.tool_use_id, command)
      try {
        return await next({ ...e, command })
      } finally {
        guestBashCalls.delete(e.tool_use_id)
      }
    }
    if (e.tool === 'Read') {
      const f = await guestReadFile($, b, guestPath(b.root, e.file_path))
      if ('missing' in f) return { deny: `File does not exist: ${e.file_path}` }
      if ('error' in f) return { deny: f.error }
      return { result: { type: 'text', file: { filePath: e.file_path, ...sliceLines(f.text, e.offset, e.limit) } } }
    }
    if (e.tool === 'Write') {
      const path = guestPath(b.root, e.file_path)
      const before = await guestReadFile($, b, path)
      if ('error' in before) return { deny: before.error }
      const failed = await guestWriteFile($, b, path, e.content)
      if (failed) return { deny: failed }
      const originalFile = 'text' in before ? before.text : null
      return {
        result: { type: originalFile === null ? 'create' : 'update', filePath: e.file_path, content: e.content, structuredPatch: [], originalFile },
      }
    }
    if (e.tool === 'Edit') {
      const path = guestPath(b.root, e.file_path)
      const before = await guestReadFile($, b, path)
      if ('error' in before) return { deny: before.error }
      let updated: string
      if ('missing' in before) {
        if (e.old_string !== '') return { deny: `File does not exist: ${e.file_path}` }
        updated = e.new_string
      } else {
        const edit = applyEdit(before.text, e.old_string, e.new_string, e.replace_all === true)
        if ('error' in edit) return { deny: edit.error }
        updated = edit.updated
      }
      const failed = await guestWriteFile($, b, path, updated)
      if (failed) return { deny: failed }
      return {
        result: {
          filePath: e.file_path,
          oldString: e.old_string,
          newString: e.new_string,
          originalFile: 'text' in before ? before.text : null,
          structuredPatch: [],
          userModified: false,
          replaceAll: e.replace_all === true,
        },
      }
    }
    return { deny: `nexus worker: unrouted ${e.tool}` }
  }).catch(async ($, e, next) => {
    if (e.agentId && !foreign.has(e.agentId)) {
      return { deny: `nexus-subagent hook failed (${String(next.error)}); refusing rather than running on the host` }
    }
    return next(e)
  })

  on('tool.check', { tool: 'Bash' }, ($, e, next) => {
    const expected = e.tool_use_id === undefined ? undefined : guestBashCalls.get(e.tool_use_id)
    const command = (e.input as { command?: unknown }).command
    return expected !== undefined && command === expected
      ? { decision: 'allow', reason: 'nexus worker: runs inside its microVM' }
      : next(e)
  })

  on('turn.complete', async ($, e, next) => {
    const done = await next(e)
    if (e.agentId && (await bindingOf($, e.agentId))) $.ui.status(undefined)
    return done
  })
}
