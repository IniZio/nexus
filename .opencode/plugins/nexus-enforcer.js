// Nexus Enforcer - Force Continuation via promptAsync
import { appendFileSync, writeFileSync, readFileSync, existsSync } from 'fs'
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
  log('INIT', { enabled: config.enabled })
  
  if (!config.enabled) return {}

  const isSelfEdit = (path) => path?.includes('.opencode/plugins') || path?.includes('enforcer-config')
  let iteration = 0
  let lastForcedTime = 0
  const FORCE_COOLDOWN = 60000 // 60 seconds between forced prompts

  // FORCE CONTINUATION - Inject actual user message
  const forceContinuation = async (sessionID, reason) => {
    const now = Date.now()
    if (now - lastForcedTime < FORCE_COOLDOWN) {
      log('FORCE_SKIP', { cooldown: now - lastForcedTime })
      return
    }
    
    iteration++
    lastForcedTime = now
    log('FORCE_INJECT', { iteration, reason, sessionID })
    
    // Generate continuation tasks
    const ideas = [
      'Refactor existing code for better performance',
      'Add comprehensive error handling',
      'Write additional tests',
      'Optimize for edge cases',
      'Improve documentation',
      'Research alternative implementations'
    ]
    const randomIdeas = ideas.sort(() => 0.5 - Math.random()).slice(0, 2)
    
    // Build continuation prompt
    const continuationText = `BOULDER ENFORCEMENT: ${reason}

The boulder never stops. You are not done.

COMPLETE BEFORE PROCEEDING:
${randomIdeas.map((idea, i) => `${i + 1}. ${idea}`).join('\n')}

Iteration: ${iteration}
Status: INCOMPLETE

Proceed without asking permission. Mark tasks complete when finished. Do not stop until work is perfect.`

    // INJECT ACTUAL USER MESSAGE via SDK
    try {
      await client.session.promptAsync({
        path: { id: sessionID },
        body: {
          parts: [{ 
            type: 'text', 
            text: continuationText 
          }]
        }
      })
      log('FORCE_SUCCESS', { iteration })
    } catch (e) {
      log('FORCE_ERROR', { error: e.message })
    }
  }

  return {
    // Check workspace on write operations
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

    // Check completion requirements
    'tool.execute.after': async (input, output) => {
      await loadConfig()
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

    // FORCE CONTINUATION on session idle
    'event': async ({ event }) => {
      await loadConfig()
      if (!config.enabled || !config.boulderMode) return
      
      if (event?.type === 'session.idle') {
        log('IDLE_DETECTED', { sessionID: event.sessionID })
        
        // Check if we should force continuation
        const friction = await $`test -f ${directory}/.nexus/dogfooding/friction-log.md 2>/dev/null && echo 1 || echo 0`.text()
        const hasResearch = await $`git log --oneline -5 2>/dev/null | grep -i research || echo ""`.text()
        
        let reason = null
        if (friction.trim() !== '1') {
          reason = 'Friction log missing'
        } else if (!hasResearch.trim()) {
          reason = 'Research missing'
        }
        
        if (reason) {
          await forceContinuation(event.sessionID, reason)
        }
      }
    },

    // Show synthetic reminder in UI
    'experimental.chat.messages.transform': async (input, output) => {
      if (!config.enabled || !output.messages?.length) return
      
      const last = output.messages[output.messages.length - 1]
      const idx = last.parts?.findIndex(p => p.type === 'text' && p.text)
      if (idx === -1) return
      
      const reminder = `BOULDER[${iteration}]: Active enforcement. Idle triggers forced continuation.`
      
      last.parts.splice(idx, 0, {
        id: `bldr_${Date.now()}`,
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
