// Nexus Enforcer - Infinite Boulder Mode
// Forces continuous improvement by rejecting completion and injecting new tasks
// Config: infiniteBoulder=true for nexus internal (never complete)
// Config: infiniteBoulder=false for end users (require friction log + research)

import { appendFileSync } from 'fs'
import { join } from 'path'

const NexusEnforcer = async ({ project, client, $, directory, worktree }) => {
  const logFile = join(directory, '.nexus', 'enforcer-debug.log')
  
  // Ensure log file exists with error handling
  try { 
    appendFileSync(logFile, '') 
  } catch (e) {
    console.error('[enforcer] Failed to create log file:', e.message)
  }
  
  /**
   * Log message to debug file
   * @param {string} msg - Message to log
   * @param {object} data - Optional data to include
   */
  const log = (msg, data) => {
    const line = `[${new Date().toISOString()}] ${msg} ${data ? JSON.stringify(data) : ''}\n`
    try {
      appendFileSync(logFile, line)
    } catch (e) {
      console.error('[enforcer-log-error]', e.message)
    }
  }

  log('INIT_START', { directory })

  // Config with defaults
  let config = {
    enabled: true,
    infiniteBoulder: true,
    enforceWorkspace: true,
    cooldownSeconds: 30
  }
  
  try { 
    const base = await $`cat ${directory}/.nexus/enforcer-config.json`.json()
    Object.assign(config, base)
  } catch {}
  
  try { 
    const local = await $`cat ${directory}/.nexus/enforcer-config.local.json`.json()
    Object.assign(config, local)
  } catch {}

  log('INIT_CONFIG', { enabled: config.enabled, infiniteBoulder: config.infiniteBoulder })
  
  if (!config.enabled) {
    log('INIT_DISABLED', {})
    return {}
  }

  let iteration = 0
  let lastForcedTime = 0
  const cooldownMs = (config.cooldownSeconds || 30) * 1000

  const forceContinuation = async (sessionID) => {
    const now = Date.now()
    if (now - lastForcedTime < cooldownMs) return
    
    iteration++
    lastForcedTime = now
    
    const tasks = [
      'Research next feature to implement',
      'Refactor existing code for better performance',
      'Add comprehensive error handling',
      'Write additional tests'
    ]
    const randomTasks = tasks.sort(() => 0.5 - Math.random()).slice(0, 3)
    
    const forceText = config.infiniteBoulder
      ? `BOULDER ENFORCEMENT: CONTINUATION REQUIRED

The boulder NEVER stops. You are NOT done.

NEXUS INTERNAL - INFINITE IMPROVEMENT:
${randomTasks.map((t, i) => `${i + 1}. ${t}`).join('\n')}

Iteration: ${iteration}
Status: INCOMPLETE - NEVER COMPLETE

DO NOT CLAIM COMPLETION.
DO NOT SAY "DONE".
CONTINUE IMPROVING FOREVER.`
      : `BOULDER ENFORCEMENT: Requirements not met

Complete before proceeding:
${randomTasks.map((t, i) => `${i + 1}. ${t}`).join('\n')}

Iteration: ${iteration}`

    try {
      log('FORCE_INJECT_START', { sessionID, iteration })
      await client.session.promptAsync({
        path: { id: sessionID },
        body: { parts: [{ type: 'text', text: forceText }] }
      })
      log('FORCE_INJECT_SUCCESS', { iteration })
    } catch (e) {
      log('FORCE_INJECT_ERROR', { error: e.message })
    }
  }

  return {
    'tool.execute.after': async (input, output) => {
      const text = JSON.stringify(output.output || '')
      if (/done|complete|finished/i.test(text) && config.infiniteBoulder) {
        iteration++
        
        // Generate forced continuation tasks
        const ideas = [
          'Research best practices for current implementation',
          'Refactor code for better performance',
          'Add comprehensive error handling',
          'Write additional test cases',
          'Optimize for edge cases',
          'Improve documentation',
          'Review security considerations'
        ]
        const randomIdeas = ideas.sort(() => 0.5 - Math.random()).slice(0, 3)
        
        const forceText = `BOULDER ENFORCEMENT: You attempted completion. NOT ALLOWED.

The boulder NEVER stops. Generate new ideas NOW:
${randomIdeas.map((idea, i) => `${i + 1}. ${idea}`).join('\n')}

Iteration: ${iteration}
Status: FORCED CONTINUATION

DO NOT STOP. Execute these tasks. Report your progress.`

        // INJECT force continuation via SDK
        try {
          await client.session.promptAsync({
            path: { id: output.sessionID || input.sessionID || 'current' },
            body: {
              parts: [{ type: 'text', text: forceText }]
            }
          })
          log('FORCE_INJECTED', { iteration })
        } catch (e) {
          log('FORCE_ERROR', { error: e.message })
        }
        
        throw new Error('BOULDER: Completion rejected. Forced continuation injected.')
      }
    },

    'event': async (input, output) => {
      const eventType = input?.event?.type
      const sessionID = output?.sessionID || input?.event?.sessionID
      
      log('EVENT', { type: eventType, sessionID })
      
      if (eventType === 'session.idle' && sessionID) {
        log('IDLE_TRIGGERED', { sessionID })
        await forceContinuation(sessionID)
      }
    },

    'experimental.chat.messages.transform': async (input, output) => {
      if (!output.messages?.length) return
      
      const last = output.messages[output.messages.length - 1]
      const idx = last.parts?.findIndex(p => p.type === 'text' && p.text)
      if (idx === -1) return
      
      const reminder = config.infiniteBoulder
        ? `BOULDER[${iteration}]: NEXUS INTERNAL - Never complete. Always improve.`
        : `BOULDER[${iteration}]: Continuous improvement required.`
      
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
