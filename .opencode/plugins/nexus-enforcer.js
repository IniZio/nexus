// Nexus Enforcer - Debug Version
import { appendFileSync, writeFileSync } from 'fs'
import { join } from 'path'

const NexusEnforcer = async ({ project, client, $, directory, worktree }) => {
  const logFile = join(directory, '.nexus', 'enforcer-debug.log')
  const log = (msg, data) => {
    try {
      appendFileSync(logFile, `[${new Date().toISOString()}] ${msg} ${data ? JSON.stringify(data) : ''}\n`)
    } catch {}
  }

  let config = { enabled: true, enforceWorkspace: true, enforceDogfooding: true, boulderMode: true }
  
  const loadConfig = async () => {
    try { Object.assign(config, await $`cat ${directory}/.nexus/enforcer-config.json`.json()) } catch {}
    try { Object.assign(config, await $`cat ${directory}/.nexus/enforcer-config.local.json`.json()) } catch {}
  }

  await loadConfig()
  log('INIT', { enabled: config.enabled, boulder: config.boulderMode })
  
  if (!config.enabled) {
    log('DISABLED', {})
    return {}
  }

  const isSelfEdit = (path) => path?.includes('.opencode/plugins') || path?.includes('enforcer-config')
  let iteration = 0

  // Session start notification
  const sessionStart = async () => {
    iteration++
    log('SESSION_START', { iteration })
    
    try {
      await client.tui.showToast({
        body: {
          title: 'BOULDER ACTIVE',
          message: `Session ${iteration}. Continuous improvement required.`,
          variant: 'info',
          duration: 5000
        }
      })
    } catch (e) {
      log('TOAST_ERROR', { error: e.message })
    }
    
    const ideas = [
      'Review code for improvements',
      'Refactor technical debt',
      'Add error handling'
    ]
    
    try {
      writeFileSync(`${directory}/.nexus/CONTINUATION_REQUIRED.txt`, 
        `BOULDER SESSION ${iteration}\n\nTASKS:\n${ideas.map((i, x) => `${x+1}. ${i}`).join('\n')}\n\nComplete before done.`)
    } catch {}
  }
  
  sessionStart().catch(() => {})

  // Interval checking
  let lastActiveTime = Date.now()
  let idleInterval = null
  
  const startIdleCheck = () => {
    if (idleInterval) return
    
    idleInterval = setInterval(async () => {
      if (!config.enabled || !config.boulderMode) return
      
      const idleTime = Date.now() - lastActiveTime
      
      if (idleTime > 30000) { // 30 seconds
        iteration++
        log('IDLE_ALERT', { idleTime, iteration })
        
        // Show toast to user
        try {
          await client.tui.showToast({
            body: {
              title: 'BOULDER ALERT',
              message: `Idle ${Math.round(idleTime/1000)}s. Generate new ideas now.`,
              variant: 'warning',
              duration: 8000
            }
          })
        } catch {}
        
        lastActiveTime = Date.now()
      }
    }, 10000)
  }
  
  startIdleCheck()

  return {
    'tool.execute.before': async (input, output) => {
      await loadConfig()
      lastActiveTime = Date.now()
      
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
      lastActiveTime = Date.now()
      
      if (!config.enabled || !config.enforceDogfooding) return
      
      const text = JSON.stringify(output.output || '')
      if (!/done|complete|finished/i.test(text)) return
      
      const friction = await $`test -f ${directory}/.nexus/dogfooding/friction-log.md 2>/dev/null && echo 1 || echo 0`.text()
      if (friction.trim() !== '1') {
        throw new Error('BLOCK: Friction log required')
      }
      
      const research = await $`git log --oneline -3 2>/dev/null | grep -i research || echo ""`.text()
      if (!research.trim()) {
        throw new Error('BLOCK: Research required')
      }
    },

    'experimental.chat.messages.transform': async (input, output) => {
      log('TRANSFORM_HOOK', { hasMessages: !!output.messages, count: output.messages?.length })
      
      if (!config.enabled || !output.messages?.length) {
        log('TRANSFORM_SKIP', { reason: 'no messages' })
        return
      }
      
      const last = output.messages[output.messages.length - 1]
      log('TRANSFORM_LAST', { hasParts: !!last.parts, partsCount: last.parts?.length })
      
      if (!last.parts?.length) {
        log('TRANSFORM_SKIP', { reason: 'no parts' })
        return
      }
      
      const idx = last.parts.findIndex(p => p.type === 'text' && p.text)
      log('TRANSFORM_IDX', { idx })
      
      if (idx === -1) {
        log('TRANSFORM_SKIP', { reason: 'no text part' })
        return
      }
      
      const hasCont = await $`test -f ${directory}/.nexus/CONTINUATION_REQUIRED.txt 2>/dev/null && echo 1 || echo 0`.text()
      
      const reminder = hasCont.trim() === '1' 
        ? `BOULDER[${iteration}]: Tasks pending. Check CONTINUATION_REQUIRED.txt`
        : `BOULDER[${iteration}]: Active. Idle 30s triggers new ideas.`
      
      log('TRANSFORM_INSERT', { reminder })
      
      try {
        last.parts.splice(idx, 0, {
          id: `bldr_${Date.now()}`,
          messageID: last.info?.id || 'x',
          sessionID: last.info?.sessionID || '',
          type: 'text',
          text: reminder,
          synthetic: true
        })
        log('TRANSFORM_SUCCESS', { inserted: true })
      } catch (e) {
        log('TRANSFORM_ERROR', { error: e.message })
      }
    }
  }
}

export default NexusEnforcer
