import { describe, expect, test } from 'claude-code/testing'
import { isReadOnlyBash, orchestrateDeny, parseOrchestrateArg, guestCommand, guestPath, lastLine, parseCreated, route, sliceLines, applyEdit, sandboxRefFor } from './policy'

describe('policy', () => {
  test('maps a herdr worktree path to its sandbox handle', () => {
    expect(sandboxRefFor('/home/u/.herdr/worktrees/nexus/sandbox-less')).toBe('nexus/sandbox-less')
    expect(sandboxRefFor('/home/u/magic/nexus')).toBe(undefined)
  })

  test('wraps Bash so it runs in the writable guest worktree with quoting intact', () => {
    expect(guestCommand('p/n', '/h/wt', "cd /h/wt/sub && echo 'hi'")).toBe(
      `nexus exec --cwd /workspace 'p/n' -- bash -c 'cd /workspace/sub && echo '\\''hi'\\'''`,
    )
  })

  test('maps host worktree paths onto /workspace and leaves other guest paths alone', () => {
    expect(guestPath('/h/wt', '/h/wt/a/b.go')).toBe('/workspace/a/b.go')
    expect(guestPath('/h/wt', 'a/b.go')).toBe('/workspace/a/b.go')
    expect(guestPath('/h/wt', '/h/wtx/c')).toBe('/h/wtx/c')
    expect(guestPath('/h/wt', '/tmp/x')).toBe('/tmp/x')
  })

  test('routes file tools into the guest and refuses the rest', () => {
    expect(route('Bash')).toEqual({ kind: 'guest-bash' })
    expect(route('Edit')).toEqual({ kind: 'guest-file' })
    expect(route('Read')).toEqual({ kind: 'guest-file' })
    expect(route('SubagentHandback')).toEqual({ kind: 'engine' })
    expect(route('WebFetch').kind).toBe('deny')
    expect(route('NotebookEdit').kind).toBe('deny')
  })

  test('slices a file the way Read pages it', () => {
    expect(sliceLines('a\nb\nc\n', 2, 1)).toEqual({ content: 'b', numLines: 1, startLine: 2, totalLines: 3 })
    expect(sliceLines('')).toEqual({ content: '', numLines: 0, startLine: 1, totalLines: 0 })
  })

  test('edits like Edit: unique match, replace_all, and $ in replacements', () => {
    expect(applyEdit('x y x', 'x', 'z', false)).toEqual({ error: expect.stringContaining('Found 2 matches') })
    expect(applyEdit('x y x', 'x', 'z', true)).toEqual({ updated: 'z y z' })
    expect(applyEdit('a b', 'b', '$&$1', false)).toEqual({ updated: 'a $&$1' })
    expect(applyEdit('a', 'q', 'r', false)).toEqual({ error: 'String to replace not found in file.' })
  })

  test('parses the delegate_worktree_create result', () => {
    const c = parseCreated('{"branch":"b","handle":"p/b","worktree_path":"/h/.herdr/worktrees/p/b","sandbox_id":"sb-1"}')
    expect(c.handle).toBe('p/b')
    expect(() => parseCreated('{"handle":"p/b","worktree_path":""}')).toThrow()
  })

  test('takes the JSON result from the last stdout line', () => {
    expect(lastLine('progress\n{"handle":"p/b"}\n\n')).toBe('{"handle":"p/b"}')
    expect(lastLine('')).toBe('')
  })

  test('lets a ticket orchestrator use Agent and messaging but not host tools', () => {
    expect(route('Agent')).toEqual({ kind: 'engine' })
    expect(route('SendMessage')).toEqual({ kind: 'engine' })
    expect(route('WebFetch').kind).toBe('deny')
  })

  test('classifies read-only Bash for orchestrate mode', () => {
    for (const c of [
      'git status',
      'git log --oneline -5 | head -3',
      'git diff HEAD~1 && ls -la',
      'git branch -a',
      'git branch --list "feat/*"',
      'cat a.go | wc -l',
      "rg 'a;b' src",
      'find . -name "*.go" 2>/dev/null',
      'ls missing 2>&1',
    ]) expect(isReadOnlyBash(c)).toBe(true)
    for (const c of [
      'rm -rf x',
      'git commit -m x',
      'git branch newbranch',
      'git branch -D old',
      'git diff --output=out.patch',
      'git -C /tmp status',
      'cat a > b',
      'echo hi',
      'ls; rm x',
      'ls && touch x',
      'ls $(rm x)',
      'ls `rm x`',
      'ls "$(rm x)"',
      'find . -delete',
      'find . -exec rm {} +',
      'rg --pre ./x foo',
      'sleep 1 &',
      'FOO=1 ls',
      'cat <(rm x)',
      "ls 'unterminated",
    ]) expect(isReadOnlyBash(c)).toBe(false)
  })

  test('denies main-thread mutations in orchestrate mode with a spawn hint', () => {
    for (const t of ['Edit', 'Write', 'NotebookEdit', 'Agent']) {
      expect(orchestrateDeny(t)).toContain('mcp__nexus-subagent__spawn')
    }
    expect(orchestrateDeny('Bash', 'rm -rf /')).toContain('mcp__nexus-subagent__spawn')
    expect(orchestrateDeny('Bash', 'git status')).toBe(undefined)
    expect(orchestrateDeny('Bash')).toContain('read-only')
    for (const t of ['Read', 'Glob', 'Grep', 'mcp__nexus-subagent__spawn', 'SendMessage', 'AskUserQuestion']) {
      expect(orchestrateDeny(t)).toBe(undefined)
    }
  })

  test('parses the orchestrate argument', () => {
    expect(parseOrchestrateArg('ON')).toBe('on')
    expect(parseOrchestrateArg(' off ')).toBe('off')
    expect(parseOrchestrateArg(undefined)).toBe('status')
    expect(parseOrchestrateArg('maybe')).toBe(undefined)
  })

  test('sandbox_* tools are not denied in orchestrate mode (mirror spawn/teardown)', () => {
    // They mirror spawn: sanctioned route, run in a VM via the nexus CLI, not on the host.
    for (const t of ['spawn', 'teardown', 'sandbox_create', 'sandbox_exec', 'sandbox_remove', 'sandbox_run', 'sandbox_list', 'sandbox_start', 'sandbox_stop']) {
      expect(orchestrateDeny('mcp__nexus-subagent__' + t)).toBe(undefined)
    }
  })

  test('nexus workers cannot call sandbox_exec', () => {
    expect(route('mcp__nexus-subagent__sandbox_exec').kind).toBe('deny')
  })
})
