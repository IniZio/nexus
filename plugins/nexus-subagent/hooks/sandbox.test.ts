import { describe, expect, test } from 'claude-code/testing'
import { SANDBOX_TOOLS, SANDBOX_TOOL_NAMES, SANDBOX_TOOL_PREFIX, buildSandboxCall, capList, formatSandboxResult } from './sandbox'

const call = (tool: string, args: Record<string, unknown>) => buildSandboxCall(tool, args) as any

describe('sandbox tools', () => {
  test('registers the seven tools in order, no pause/resume', () => {
    expect(SANDBOX_TOOL_NAMES).toEqual(['sandbox_create', 'sandbox_list', 'sandbox_start', 'sandbox_stop', 'sandbox_remove', 'sandbox_exec', 'sandbox_run'])
    expect(SANDBOX_TOOL_PREFIX).toBe('mcp__nexus-subagent__')
    const run = SANDBOX_TOOLS[6]
    expect(run.description.endsWith('Needs ref or digest (an image).')).toBe(true)
    expect((run.inputSchema as any).required).toEqual(['project', 'name', 'argv'])
    expect('rootfs_path' in (run.inputSchema as any).properties).toBe(false)
    expect((SANDBOX_TOOLS[0].inputSchema as any).required).toEqual(['project', 'name', 'remove_on_exit'])
  })

  test('maps lifecycle verbs', () => {
    expect(call('sandbox_list', {})).toEqual({ argv: ['nexus', '--json', 'ls'], timeoutMs: 60000, json: true })
    expect(call('sandbox_start', { ref: 'p/n' })).toEqual({ argv: ['nexus', '--json', 'start', 'p/n'], timeoutMs: 300000, json: true })
    expect(call('sandbox_stop', { ref: 'p/n' }).argv).toEqual(['nexus', '--json', 'stop', 'p/n'])
    expect(call('sandbox_remove', { ref: 'p/n' }).argv).toEqual(['nexus', '--json', 'rm', 'p/n'])
    expect(call('sandbox_start', {})).toEqual({ error: 'ref is required' })
    expect(call('sandbox_pause', { ref: 'x' }).error).toBe('sandbox_pause is not supported: the nexus CLI has no pause/resume verb for plain sandboxes')
    expect(call('sandbox_bogus', {})).toEqual({ error: 'unknown sandbox tool sandbox_bogus' })
  })

  test('maps create', () => {
    expect(call('sandbox_create', { project: 'p' })).toEqual({ error: 'project and name are required' })
    expect(call('sandbox_create', { project: 'p', name: 'n' }).argv).toEqual(['nexus', '--json', 'sandbox', 'create', 'p/n'])
    const c = call('sandbox_create', { project: 'p', name: 'n', remove_on_exit: true, ref: 'img', memory_mib: 1024, vcpus: 2, motive: 'm1', nested_virt: true })
    expect(c.argv).toEqual(['nexus', '--json', 'sandbox', 'create', 'p/n', '--rm', '--image', 'img', '--memory', '1024', '--vcpus', '2', '--label', 'motive=m1', '--nested'])
    expect(c.timeoutMs).toBe(600000)
    expect(call('sandbox_create', { project: 'p', name: 'n', rootfs_path: '/r', digest: 'sha256:a' }).argv.slice(5)).toEqual(['--rootfs', '/r'])
    expect(call('sandbox_create', { project: 'p', name: 'n', digest: 'sha256:a' }).argv.slice(5)).toEqual(['--image', 'sha256:a'])
    expect(call('sandbox_create', { project: 'p', name: 'n', memory_mib: 'x' }).argv.length).toBe(5)
  })

  test('maps exec', () => {
    const c = call('sandbox_exec', { ref: 'p/n', argv: ['ls', '-l'], cwd: '/w', env: { A: '1', B: 2 }, stdin: 'hi' })
    expect(c).toEqual({ argv: ['nexus', 'exec', '--cwd', '/w', 'p/n', '--', 'env', 'A=1', 'ls', '-l'], timeoutMs: 600000, json: false, stdin: 'hi' })
    expect(call('sandbox_exec', { ref: 'p/n', argv: ['ls'] }).argv).toEqual(['nexus', 'exec', 'p/n', '--', 'ls'])
    expect('stdin' in call('sandbox_exec', { ref: 'p/n', argv: ['ls'] })).toBe(false)
    expect(call('sandbox_exec', { argv: ['ls'] })).toEqual({ error: 'ref is required' })
    expect(call('sandbox_exec', { ref: 'p/n', argv: [1] })).toEqual({ error: 'argv is required' })
  })

  test('maps run', () => {
    const c = call('sandbox_run', { project: 'p', name: 'n', ref: 'img', memory_mib: 256, vcpus: 2, argv: ['echo', 'x'], stdin: 'in' })
    expect(c).toEqual({ argv: ['nexus', 'run', '--project', 'p', '--name', 'n', '--memory', '256', '--vcpus', '2', 'img', '--', 'echo', 'x'], timeoutMs: 600000, json: false, stdin: 'in' })
    const w = call('sandbox_run', { project: 'p', name: 'n', digest: 'sha256:a', argv: ['ls'], env: { K: 'v' }, cwd: '/w' })
    expect(w.argv).toEqual(['nexus', 'run', '--project', 'p', '--name', 'n', 'sha256:a', '--', 'sh', '-c', 'cd -- "$1" && shift && exec "$@"', 'sh', '/w', 'env', 'K=v', 'ls'])
    expect(call('sandbox_run', { name: 'n', ref: 'i', argv: ['a'] })).toEqual({ error: 'project and name are required' })
    expect(call('sandbox_run', { project: 'p', name: 'n', ref: 'i' })).toEqual({ error: 'argv is required' })
    expect(call('sandbox_run', { project: 'p', name: 'n', ref: 'i', argv: ['a'], rootfs_path: '/r' }).error).toBe('sandbox_run: rootfs_path is not supported through the nexus CLI; pass ref or digest')
    expect(call('sandbox_run', { project: 'p', name: 'n', ref: 'i', argv: ['a'], nested_virt: true }).error).toBe('sandbox_run: nested_virt is not supported through the nexus CLI run verb; use sandbox_create + sandbox_exec')
    expect(call('sandbox_run', { project: 'p', name: 'n', argv: ['a'] })).toEqual({ error: 'sandbox_run needs ref or digest (an image)' })
  })
})

describe('sandbox results', () => {
  const list = call('sandbox_list', {})
  const ok = (stdout: string, exitCode = 0) => ({ exitCode, stdout, stderr: '' })

  test('formats list from a real envelope', () => {
    const out = '{"schema_version":1,"kind":"sandbox.list","data":{"sandboxes":[{"id":"sb-1","project":"p","name":"n","handle":"p/n","state":"running","remove_on_exit":false,"stop_reason":""}]}}\n'
    expect(formatSandboxResult('sandbox_list', list, ok(out))).toEqual({
      result: '[{"id":"sb-1","project":"p","name":"n","handle":"p/n","state":"running"}]',
    })
  })

  test('formats single sandbox and remove', () => {
    const out = '{"schema_version":1,"kind":"sandbox","data":{"id":"sb-1","project":"p","name":"n","handle":"p/n","state":"stopped","extra":1}}'
    expect(formatSandboxResult('sandbox_stop', call('sandbox_stop', { ref: 'x' }), ok(out))).toEqual({
      result: '{"id":"sb-1","project":"p","name":"n","handle":"p/n","state":"stopped"}',
    })
    expect(formatSandboxResult('sandbox_remove', call('sandbox_remove', { ref: 'x' }), ok('{"kind":"sandbox.removed","data":{}}'))).toEqual({ result: '{"removed":true}' })
  })

  test('denies on error envelope and failures', () => {
    const env = '{"schema_version":1,"kind":"error","error":{"code":"sandbox_not_found","message":"boom"}}'
    expect(formatSandboxResult('sandbox_list', list, ok(env, 1))).toEqual({ deny: 'boom' })
    expect(formatSandboxResult('sandbox_list', list, { exitCode: 2, stdout: '', stderr: ' bad \n' })).toEqual({ deny: 'bad' })
    expect(formatSandboxResult('sandbox_list', list, ok('', 3))).toEqual({ deny: 'nexus exited 3' })
    expect(formatSandboxResult('sandbox_list', list, ok('hello'))).toEqual({ deny: 'nexus returned non-JSON output: hello' })
  })

  test('formats exec as data even on non-zero exit', () => {
    const c = call('sandbox_exec', { ref: 'p/n', argv: ['false'] })
    expect(formatSandboxResult('sandbox_exec', c, { exitCode: 3, stdout: 'o', stderr: 'e' })).toEqual({
      result: '{"exit_code":3,"stdout":"o","stderr":"e"}',
    })
  })

  test('capList truncates to a fitting prefix', () => {
    const items = [{ a: 'x'.repeat(20) }, { a: 'y'.repeat(20) }, { a: 'z'.repeat(20) }]
    expect(capList(items)).toBe(JSON.stringify(items))
    const capped = JSON.parse(capList(items, 60))
    expect(capped.sandboxes.length).toBe(2)
    expect(capped.truncated.total_bytes).toBe(JSON.stringify(items).length)
    expect(capped.truncated.bytes_omitted).toBe(JSON.stringify(items).length - JSON.stringify(items.slice(0, 2)).length)
    expect(JSON.parse(capList(items, 1)).sandboxes.length).toBe(1)
  })
})
