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
