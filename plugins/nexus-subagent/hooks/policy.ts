export const WORKER_TYPE = 'nexus-subagent:worker'

const HERDR_WORKTREE = /\/\.herdr\/worktrees\/([^/]+)\/([^/]+)\/?$/

export type Route =
  | { kind: 'guest-bash' }
  | { kind: 'engine' }
  | { kind: 'guest-file' }
  | { kind: 'deny'; reason: string }

export function sandboxRefFor(worktree: string): string | undefined {
  const m = HERDR_WORKTREE.exec(worktree)
  return m ? `${m[1]}/${m[2]}` : undefined
}

export function worktreeInPrompt(prompt: string): string | undefined {
  return /^worktree:\s*(\S+)\s*$/m.exec(prompt)?.[1]
}

export function shellQuote(s: string): string {
  return `'${s.replaceAll("'", `'\\''`)}'`
}

export const GUEST_WORKTREE = '/workspace'

export function toGuest(root: string, text: string): string {
  return text.replaceAll(root, GUEST_WORKTREE)
}

export function guestCommand(ref: string, root: string, command: string): string {
  return `nexus exec --cwd ${GUEST_WORKTREE} ${shellQuote(ref)} -- bash -c ${shellQuote(toGuest(root, command))}`
}

export function route(tool: string): Route {
  switch (tool) {
    case 'Bash':
      return { kind: 'guest-bash' }
    case 'SubagentHandback':
    case 'Agent':
    case 'Task':
    case 'SendMessage':
    case 'TodoWrite':
    case 'TaskCreate':
    case 'TaskUpdate':
    case 'TaskList':
    case 'TaskGet':
      return { kind: 'engine' }
    case 'Read':
    case 'Write':
    case 'Edit':
      return { kind: 'guest-file' }
    default:
      return { kind: 'deny', reason: `${tool} is not available to a nexus worker` }
  }
}

export type Created = { handle: string; worktree_path: string; branch: string; sandbox_id?: string }

export function parseCreated(text: string): Created {
  const json = text.slice(text.indexOf('{'), text.lastIndexOf('}') + 1)
  let v: Partial<Created>
  try {
    v = JSON.parse(json) as Partial<Created>
  } catch {
    throw new Error(`worktree create returned non-JSON : ${text.slice(0, 400)}`)
  }
  if (!v.handle || !v.worktree_path) throw new Error(`worktree create returned no handle/worktree_path: ${text.slice(0, 400)}`)
  return v as Created
}

export function lastLine(text: string): string {
  return text.trimEnd().split('\n').at(-1) ?? ''
}

export const READ_DEFAULT_LIMIT = 2000

export type ReadSlice = { content: string; numLines: number; startLine: number; totalLines: number }

export function sliceLines(text: string, offset?: number, limit?: number): ReadSlice {
  const body = text.endsWith('\n') ? text.slice(0, -1) : text
  const lines = text === '' ? [] : body.split('\n')
  const totalLines = lines.length
  const startLine = Math.max(1, offset ?? 1)
  const page = lines.slice(startLine - 1, startLine - 1 + (limit ?? READ_DEFAULT_LIMIT))
  return { content: page.join('\n'), numLines: page.length, startLine, totalLines }
}

export type EditOutcome = { updated: string } | { error: string }

export function applyEdit(original: string, oldString: string, newString: string, replaceAll: boolean): EditOutcome {
  if (oldString === newString) return { error: 'No changes to make: old_string and new_string are exactly the same.' }
  const count = oldString === '' ? 0 : original.split(oldString).length - 1
  if (count === 0) return { error: 'String to replace not found in file.' }
  if (count > 1 && !replaceAll) {
    return { error: `Found ${count} matches of the string to replace, but replace_all is false. Set replace_all or add context to make it unique.` }
  }
  return { updated: replaceAll ? original.split(oldString).join(newString) : original.replace(oldString, () => newString) }
}

export function guestPath(root: string, path: string): string {
  if (path === root || path.startsWith(`${root}/`)) return GUEST_WORKTREE + path.slice(root.length)
  return path.startsWith('/') ? path : `${GUEST_WORKTREE}/${path}`
}

export const ORCHESTRATE_SPAWN_HINT = 'use mcp__nexus-subagent__spawn to hand the ticket to a sandboxed ticket orchestrator'

const READONLY_CMDS = new Set(['ls', 'cat', 'rg', 'grep', 'find', 'wc', 'head', 'tail'])
const READONLY_GIT = new Set(['status', 'log', 'diff', 'show', 'branch'])
const FIND_MUTATORS = new Set(['-exec', '-execdir', '-ok', '-okdir', '-delete', '-fprint', '-fprint0', '-fprintf', '-fls'])
const BRANCH_LIST_FLAGS = new Set(['-l', '--list', '--contains', '--no-contains', '--merged', '--no-merged', '--points-at'])
const BRANCH_SAFE_FLAGS = new Set(['-a', '--all', '-r', '--remotes', '-v', '-vv', '--verbose', '--show-current', '--no-color', '--color', '-i', '--ignore-case', '--format', '--sort', ...BRANCH_LIST_FLAGS])

// Splits a shell command into segments of words at unquoted && || | ; newline.
// Returns undefined for anything that can hide a write or a nested command.
function splitSegments(command: string): string[][] | undefined {
  const segments: string[][] = []
  let words: string[] = []
  let cur = ''
  let inWord = false
  const endWord = () => {
    if (inWord) words.push(cur)
    cur = ''
    inWord = false
  }
  const endSegment = () => {
    endWord()
    if (words.length) segments.push(words)
    words = []
  }
  for (let i = 0; i < command.length; i++) {
    const c = command[i] as string
    if (c === "'") {
      const j = command.indexOf("'", i + 1)
      if (j < 0) return undefined
      cur += command.slice(i + 1, j)
      inWord = true
      i = j
    } else if (c === '"') {
      inWord = true
      i++
      while (i < command.length && command[i] !== '"') {
        const d = command[i] as string
        if (d === '`' || (d === '$' && command[i + 1] === '(')) return undefined
        if (d === '\\' && i + 1 < command.length) i++
        cur += command[i]
        i++
      }
      if (i >= command.length) return undefined
    } else if (c === '\\') {
      if (i + 1 >= command.length) return undefined
      cur += command[++i]
      inWord = true
    } else if (c === '`' || (c === '$' && command[i + 1] === '(') || ((c === '<' || c === '>') && command[i + 1] === '(')) {
      return undefined
    } else if (c === '>') {
      const m = /^>{0,2}\s*(&\d|\/dev\/null(?![\w/.-]))/.exec(command.slice(i))
      if (!m) return undefined
      i += m[0].length - 1
      if (inWord && /^\d$/.test(cur)) {
        cur = ''
        inWord = false
      }
    } else if (c === '&') {
      if (command[i + 1] !== '&') return undefined
      i++
      endSegment()
    } else if (c === '|') {
      if (command[i + 1] === '|') i++
      endSegment()
    } else if (c === ';' || c === '\n') {
      endSegment()
    } else if (c === ' ' || c === '\t') {
      endWord()
    } else {
      cur += c
      inWord = true
    }
  }
  endSegment()
  return segments
}

function readonlyWords(w: string[]): boolean {
  const [cmd, ...args] = w
  if (cmd === undefined) return true
  if (cmd === 'git') {
    const [sub, ...rest] = args
    if (sub === undefined || !READONLY_GIT.has(sub)) return false
    if (rest.some(a => a === '--output' || a.startsWith('--output=') || a === '--ext-diff')) return false
    if (sub === 'branch') {
      const listing = rest.some(a => BRANCH_LIST_FLAGS.has(a))
      for (const a of rest) {
        if (a.startsWith('-')) {
          if (!BRANCH_SAFE_FLAGS.has(a.split('=')[0] as string)) return false
        } else if (!listing) return false
      }
    }
    return true
  }
  if (!READONLY_CMDS.has(cmd)) return false
  if (cmd === 'find') return !args.some(a => FIND_MUTATORS.has(a))
  if (cmd === 'rg') return !args.some(a => a === '--pre' || a.startsWith('--pre=') || a.startsWith('--hostname-bin'))
  return true
}

export function isReadOnlyBash(command: string): boolean {
  const segs = splitSegments(command)
  return segs !== undefined && segs.every(readonlyWords)
}

const MAIN_THREAD_EDIT_TOOLS = new Set(['Edit', 'Write', 'NotebookEdit', 'MultiEdit'])

export function orchestrateDeny(tool: string, command?: unknown): string | undefined {
  if (MAIN_THREAD_EDIT_TOOLS.has(tool)) {
    return `orchestrate mode is on: the main session does not edit files; ${ORCHESTRATE_SPAWN_HINT}.`
  }
  if (tool === 'Agent' || tool === 'Task') {
    return `orchestrate mode is on: built-in Agent spawns are disabled on the main session; ${ORCHESTRATE_SPAWN_HINT}.`
  }
  if (tool === 'Bash') {
    if (typeof command === 'string' && isReadOnlyBash(command)) return undefined
    return `orchestrate mode is on: only read-only Bash (git status/log/diff/show/branch, ls, cat, rg, grep, find, wc, head, tail) runs on the main session; ${ORCHESTRATE_SPAWN_HINT}.`
  }
  return undefined
}

export function parseOrchestrateArg(arg: string | undefined): 'on' | 'off' | 'status' | undefined {
  const a = (arg ?? '').trim().toLowerCase()
  return a === '' ? 'status' : a === 'on' || a === 'off' || a === 'status' ? a : undefined
}
