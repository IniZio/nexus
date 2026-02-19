// Nexus Enforcer - Configurable Boulder Mode
import { appendFileSync } from 'fs'
import { join } from 'path'

const NexusEnforcer = async ({ project, client, $, directory, worktree }) => {
  const logFile = join(directory, '.nexus', 'enforcer-debug.log')
  const log = (msg, data) => {
    try {
      appendFileSync(logFile, `[${new Date().toISOString()}] ${msg} ${data ? JSON.stringify(data) : ''}\n`)
    } catch {}
  }

  // Config with defaults - infiniteBoulder enabled by default for nexus internal
  let config = {
    enabled: true,
    infiniteBoulder: true,  // DEFAULT: true for nexus internal, false for end-users
    enforceWorkspace: true,
    cooldownSeconds: 30
  }
  
  try { Object.assign(config, await $`cat ${directory}/.nexus/enforcer-config.json`.json()) } catch {}
  try { Object.assign(config, await $`cat ${directory}/.nexus/enforcer-config.local.json`.json()) } catch {}

  log('INIT', { infiniteBoulder: config.infiniteBoulder, enabled: config.enabled })
  
  if (!config.enabled) return {}

  let iteration = 0
  let lastForcedTime = 0
  const cooldownMs = (config.cooldownSeconds || 30) * 1000

  // FORCE CONTINUATION
  const forceContinuation = async (sessionID) => {
    const now = Date.now()
    if (now - lastForcedTime < cooldownMs) return
    
    iteration++
    lastForcedTime = now
    
    const tasks = [
      'Research next feature to implement',
      'Refactor existing code for better performance',
      'Add comprehensive error handling',
      'Write additional tests',
      'Optimize for edge cases',
      'Improve documentation',
      'Review security considerations',
      'Add monitoring and logging'
    ]
    const randomTasks = tasks.sort(() => 0.5 - Math.random()).slice(0, 3)
    
    let forceText
    if (config.infiniteBoulder) {
      // NEXUS INTERNAL: Never complete
      forceText = `BOULDER ENFORCEMENT: CONTINUATION REQUIRED

The boulder NEVER stops. You are NOT done.

NEXUS INTERNAL - INFINITE IMPROVEMENT MANDATE:
${randomTasks.map((t, i) => `${i + 1}. ${t}`).join('\n')}

Iteration: ${iteration}
Status: INCOMPLETE - NEVER COMPLETE

DO NOT CLAIM COMPLETION.
DO NOT SAY "DONE".
CONTINUE IMPROVING FOREVER.

Research. Refactor. Perfect. Repeat.`
    } else {
      // END USER: Complete with requirements
      forceText = `BOULDER ENFORCEMENT: Requirements not met

Complete before proceeding:
${randomTasks.map((t, i) => `${i + 1}. ${t}`).join('\n')}

Iteration: ${iteration}

The boulder demands continuous improvement.`
    }

    try {
      await client.session.promptAsync({
        path: { id: sessionID },
        body: { parts: [{ type: 'text', text: forceText }] }
      })
      log('FORCE_SUCCESS', { iteration, mode: config.infiniteBoulder ? 'infinite' : 'conditional' })
    } catch (e) {
      log('FORCE_ERROR', { error: e.message })
    }
  }

  return {
    // Block completion based on mode
    'tool.execute.after': async (input, output) => {
      const text = JSON.stringify(output.output || '')
      if (!/done|complete|finished/i.test(text)) return
      
      if (config.infiniteBoulder) {
        // NEXUS INTERNAL: Never accept completion
        throw new Error('BOULDER: Completion rejected. Nexus internal requires infinite improvement.')
      } else {
        // END USER: Check requirements
        const friction = await $`test -f ${directory}/.nexus/dogfooding/friction-log.md 2>/dev/null && echo 1 || echo 0`.text()
        const research = await $`git log --oneline -3 2>/dev/null | grep -i research || echo ""`.text()
        
        if (friction.trim() !== '1' || !research.trim()) {
          throw new Error('BOULDER: Complete friction log and research before claiming done.')
        }
      }
    },

    // Force continuation on idle
    'event': async ({ event }) => {
      if (event?.type === 'session.idle' && event?.sessionID) {
        await forceContinuation(event.sessionID)
      }
    },

    // Reminder in context
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
