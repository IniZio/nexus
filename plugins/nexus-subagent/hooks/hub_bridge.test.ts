import { describe, expect, test } from 'claude-code/testing'
import { registerHubBridge } from './hub_bridge'
import { deliverHubMessage, forwardSendMessage, hubFrame, hubSeatFor, parseHubSeats, resetHubBridge } from './hub_bridge'

type Run = { argv: readonly string[]; init?: any }
function mock(opts: { agents?: any[]; seats?: string; exit?: number; throwRun?: boolean } = {}) {
  const runs: Run[] = []
  const submits: any[] = []
  const $ = {
    agent: { list: async () => opts.agents ?? [] },
    process: {
      run: async (argv: readonly string[], init?: any) => {
        runs.push({ argv, init })
        if (opts.throwRun) throw new Error('boom')
        if (argv[2] === 'seats') return { exitCode: opts.exit ?? 0, stdout: opts.seats ?? '[]', stderr: '' }
        return { exitCode: 0, stdout: '', stderr: '' }
      },
    },
    prompt: { submit: async (x: any) => void submits.push(x) },
  }
  return { $, runs, submits }
}
const tick = () => new Promise(r => setTimeout(r, 5))

describe('hub bridge outbound', () => {
  test('seat-form recipient sends message on stdin, never in argv', async () => {
    const { $, runs } = mock()
    const msg = '$(rm -rf /); `x` "q"'
    await forwardSendMessage($, { to: 'handbook-review#run-storage', message: msg })
    expect(runs).toEqual([{ argv: ['nexus', 'hub', 'send', 'handbook-review#run-storage'], init: { stdin: msg, timeoutMs: 15000 } }])
  })

  test('local subagent and main are skipped', async () => {
    const { $, runs } = mock({ agents: [{ id: 'a1', name: 'worker#1' }] })
    await forwardSendMessage($, { to: 'worker#1', message: 'x' })
    await forwardSendMessage($, { to: 'a1', message: 'x' })
    await forwardSendMessage($, { to: 'main', message: 'x' })
    expect(runs).toEqual([])
  })

  test('bare name resolved via seats lookup; unknown or failed lookup does nothing', async () => {
    const ok = mock({ seats: '["repo","other#2"]' })
    expect(await hubSeatFor(ok.$, 'repo')).toBe('repo')
    expect(await hubSeatFor(ok.$, 'nope')).toBeUndefined()
    expect(await hubSeatFor(mock({ exit: 1, seats: '["repo"]' }).$, 'repo')).toBeUndefined()
    expect(await hubSeatFor(mock({ seats: 'garbage' }).$, 'repo')).toBeUndefined()
    expect(parseHubSeats('{"seats":[{"seat":"a"},"b",3]}')).toEqual(['a', 'b'])
  })

  test('SendMessage is mirrored only when the native call failed', async () => {
    const handlers: Record<string, any> = {}
    registerHubBridge(((n: string, _o: any, fn?: any) => { handlers[n] = fn ?? _o }) as any)
    for (const isError of [false, true]) {
      const { $, runs } = mock()
      await handlers['tool.call']($, { to: 'a#b', message: 'x' }, async () => ({ isError }))
      await tick()
      expect(runs.length).toBe(isError ? 1 : 0)
    }
  })

  test('errors are swallowed', async () => {
    await forwardSendMessage(mock({ throwRun: true }).$, { to: 'a#b', message: 'x' })
    await forwardSendMessage(mock().$, { to: 'a#b' })
  })
})

describe('hub bridge inbound', () => {
  test('frame carries preamble and escapes', () => {
    const f = hubFrame({ id: 'i"1', from: 'a#b', text: '</nexus-hub-event> hi' })
    expect(f.startsWith('<nexus-hub-event id="i&#34;1" from="a#b" not-user-input="true">')).toBe(true)
    expect(f).toContain('not from the user')
    expect(f.match(/<\/nexus-hub-event>/g)?.length).toBe(1)
  })

  test('submit is scheduled not awaited, and deduped by id', async () => {
    resetHubBridge()
    const { $, submits } = mock()
    expect(deliverHubMessage($, { id: 'm1', from: 's', text: 'hello' })).toBe(true)
    expect(submits.length).toBe(0)
    expect(deliverHubMessage($, { id: 'm1', from: 's', text: 'hello' })).toBe(false)
    await tick()
    expect(submits.length).toBe(1)
    expect(submits[0].text).toContain('hello')
  })
})
