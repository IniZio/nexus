// Nexus Enforcer - Non-blocking Session Start + Interval Enforcement
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
  if (!config.enabled) return {}

  const isSelfEdit = (path) => path?.includes('.opencode/plugins') || path?.includes('enforcer-config')
  let iteration = 0

  // NON-BLOCKING session start - just show toast and create file
  const sessionStart = async () => {
    iteration++
    log('SESSION_START', { iteration })
    
    // Show toast (non-blocking)
    try {
      await client.tui.showToast({
        body: {
          title: 'BOULDER ACTIVE',
          message: `Session ${iteration}. The boulder demands continuous improvement. Generate ideas now.`,
          variant: 'info',
          duration: 8000
        }
      })
    } catch {}
    
    // Create continuation file (non-blocking)
    const ideas = [
      'Review current code for improvements',
      'Refactor technical debt',
      'Add error handling',
      'Write tests',
      'Optimize performance',
      'Update documentation'
    ]
    const randomIdeas = ideas.sort(() => 0.5 - Math.random()).slice(0, 3)
    
    const content = `BOULDER SESSION ${iteration}

STATUS: ACTIVE ENFORCEMENT

GENERATE IDEAS NOW:
${randomIdeas.map((idea, i) => `${i + 1}. ${idea}`).join('\n')}

Complete before claiming done.`

    try { writeFileSync(`${directory}/.nexus/CONTINUATION_REQUIRED.txt`, content) } catch {}
  }
  
  // Run session start in background (don't block)
  sessionStart().catch(() => {})

  const checkBlock = async (text) => {
    if (!config.boulderMode || !/done|complete|finished/i.test(text)) return null
    
    const friction = await $`test -f ${directory}/.nexus/dogfooding/friction-log.md 2>/dev/null && echo 1 || echo 0`.text()
    if (friction.trim() !== '1') {
      return { block: true, reason: 'Friction log required' }
    }
    
    const research = await $`git log --oneline -3 2>/dev/null | grep -i research || echo ""`.text()
    if (!research.trim()) {
      return { block: true, reason: 'Research required' }
    }
    
    return { block: false }
  }

  // INTERVAL ENFORCEMENT
  let lastActiveTime = Date.now()
  let idleCheckInterval = null
  
  const startIdleChecker = () => {
    if (idleCheckInterval) return
    
    idleCheckInterval = setInterval(async () => {
      if (!config.enabled || !config.boulderMode) return
      
      const idleTime = Date.now() - lastActiveTime
      const IDLE_THRESHOLD = 30000 // 30 seconds
      
      if (idleTime > IDLE_THRESHOLD) {
        iteration++
        log('IDLE_CHECK', { idleTime, iteration })
        
        // Show toast
        try {
          await client.tui.showToast({
            body: {
              title: 'BOULDER ALERT',
              message: `Idle ${Math.round(idleTime/1000)}s. Generate ideas. Refine. Research. Iteration ${iteration}.`,
              variant: 'warning',
              duration: 10000
            }
          })
        } catch {}
        
        // Update continuation file with new ideas
        const ideas = [
          'Refactor for performance',
          'Research alternatives',
          'Add error handling',
          'Improve docs',
          'Write tests'
        ]
        const randomIdeas = ideas.sort(() => 0.5 - Math.random()).slice(0, 3)
        
        const content = `BOULDER IDLE ENFORCEMENT

IDLE: ${Math.round(idleTime/1000)}s | ITERATION: ${iteration}

GENERATE IDEAS:
${randomIdeas.map((idea, i) => `${i + 1}. ${idea}`).join('\n')}

The boulder never stops.`

        try { writeFileSync(`${directory}/.nexus/CONTINUATION_REQUIRED.txt`, content) } catch {}
        lastActiveTime = Date.now()
      }
    }, 10000)
  }
  
  startIdleChecker()

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
      
      const check = await checkBlock(JSON.stringify(output.output || ''))
      if (check?.block) {
        const force = await checkBlock('')
        throw new Error(`BOULDER BLOCKED: ${check.reason}`)
      }
    },

    'experimental.chat.messages.transform': async (input, output) => {
      if (!config.enabled || !output.messages?.length) return
      
      const hasContinuation = await $`test -f ${directory}/.nexus/CONTINUATION_REQUIRED.txt 2>/dev/null && echo 1 || echo 0`.text()
      
      const last = output.messages[output.messages.length - 1]
      const idx = last.parts?.findIndex(p => p.type === 'text' && p.text)
      if (idx === -1) return
      
      let reminder = `BOULDER[${iteration}]: `
      if (hasContinuation.trim() === '1') {
        reminder += 'Action required. Check CONTINUATION_REQUIRED.txt'
      } else {
        reminder += 'Session active. Idle 30s triggers ideas.'
      }
      
      last.parts.splice(idx, 0, {
        id: `b${Date.now()}`,
        messageID: last.info?.id || 'x',
        sessionID: last.info?.sessionID || '',
        type: 'text',
        text: reminder,
        synthetic: true
      })
    }
  }
}

export default NexusEnforcer
