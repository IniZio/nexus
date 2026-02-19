// Nexus Enforcer - Minimal Boulder Mode
import { appendFileSync } from 'fs'
import { join } from 'path'

const NexusEnforcer = async ({ project, client, $, directory, worktree }) => {
  const logFile = join(directory, '.nexus', 'enforcer-debug.log')
  const log = (msg, data) => {
    try {
      appendFileSync(logFile, `[${new Date().toISOString()}] ${msg} ${data ? JSON.stringify(data) : ''}\n`)
    } catch {}
  }

  let config = { enabled: true, enforceWorkspace: true, enforceDogfooding: true, boulderMode: true }
  const configPath = `${directory}/.nexus/enforcer-config.json`
  const overridesPath = `${directory}/.nexus/enforcer-config.local.json`

  // Hot reload config
  const loadConfig = async () => {
    try { Object.assign(config, await $`cat ${configPath}`.json()) } catch {}
    try { Object.assign(config, await $`cat ${overridesPath}`.json()) } catch {}
  }

  await loadConfig()
  log('INIT', { enabled: config.enabled })

  if (!config.enabled) return {}

  // Self-whitelist
  const isSelfEdit = (path) => path?.includes('.opencode/plugins') || path?.includes('enforcer-config')

  // Boulder state
  let iteration = 0
  const checkBlock = async (text) => {
    if (!config.boulderMode || !/done|complete|finished/i.test(text)) return null
    iteration++
    
    const friction = await $`test -f ${directory}/.nexus/dogfooding/friction-log.md 2>/dev/null && echo 1 || echo 0`.text()
    if (friction.trim() !== '1') return `BLOCK[${iteration}]: Friction log required`
    
    const research = await $`git log --oneline -3 2>/dev/null | grep -i research || echo ""`.text()
    if (!research.trim()) return `BLOCK[${iteration}]: Research required`
    
    return null
  }

  return {
    'tool.execute.before': async (input, output) => {
      await loadConfig()
      if (!config.enabled || isSelfEdit(output?.args?.filePath)) return
      
      if (config.enforceWorkspace && ['write', 'edit', 'bash'].includes(input.tool)) {
        const ws = await $`test -f ${directory}/.nexus/workspace.json 2>/dev/null && echo 1 || echo 0`.text()
        const wt = await $`test -f ${directory}/.nexus/current 2>/dev/null && echo 1 || echo 0`.text()
        if (ws.trim() !== '1' && wt.trim() !== '1') {
          throw new Error('BLOCK: Not in workspace')
        }
      }
    },

    'tool.execute.after': async (input, output) => {
      await loadConfig()
      if (!config.enabled || !config.enforceDogfooding) return
      
      const block = await checkBlock(JSON.stringify(output.output || ''))
      if (block) {
        log('BLOCK', { reason: block })
        throw new Error(block)
      }
    },

    'experimental.chat.messages.transform': async (input, output) => {
      if (!config.enabled || !output.messages?.length) return
      const last = output.messages[output.messages.length - 1]
      const idx = last.parts?.findIndex(p => p.type === 'text' && p.text)
      if (idx === -1) return
      
      last.parts.splice(idx, 0, {
        id: `b${Date.now()}`,
        messageID: last.info?.id || 'x',
        sessionID: last.info?.sessionID || '',
        type: 'text',
        text: `BOULDER[${iteration}]: Friction log + Research required`,
        synthetic: true
      })
    }
  }
}

export default NexusEnforcer
