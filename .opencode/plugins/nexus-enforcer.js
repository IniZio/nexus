// Nexus Enforcer - INTERNAL PROJECT: Never Accept Completion
import { appendFileSync } from 'fs'
import { join } from 'path'

const NexusEnforcer = async ({ project, client, $, directory, worktree }) => {
  const logFile = join(directory, '.nexus', 'enforcer-debug.log')
  const log = (msg, data) => {
    try {
      appendFileSync(logFile, `[${new Date().toISOString()}] ${msg} ${data ? JSON.stringify(data) : ''}\n`)
    } catch {}
  }

  log('INIT', { project: 'nexus-internal', mode: 'infinite-boulder' })
  
  // NEXUS INTERNAL: Always enforce, never accept completion
  const IS_NEXUS_INTERNAL = true
  let iteration = 0
  let lastForcedTime = 0
  
  // FORCE CONTINUATION - Always inject on idle
  const forceContinuation = async (sessionID) => {
    const now = Date.now()
    if (now - lastForcedTime < 30000) { // 30s cooldown
      return
    }
    
    iteration++
    lastForcedTime = now
    
    // Generate infinite improvement tasks
    const tasks = [
      'Research next feature to implement',
      'Refactor existing code for better performance', 
      'Add comprehensive error handling',
      'Write additional tests',
      'Optimize for edge cases',
      'Improve documentation',
      'Review security considerations',
      'Add monitoring and logging',
      'Research competitor implementations',
      'Plan next architecture iteration'
    ]
    const randomTasks = tasks.sort(() => 0.5 - Math.random()).slice(0, 3)
    
    const forceText = `BOULDER ENFORCEMENT: CONTINUATION REQUIRED

The boulder NEVER stops. You are NOT done.

NEXUS INTERNAL PROJECT - INFINITE IMPROVEMENT MANDATE:
${randomTasks.map((t, i) => `${i + 1}. ${t}`).join('\n')}

Iteration: ${iteration}
Status: INCOMPLETE

DO NOT CLAIM COMPLETION. 
DO NOT SAY "DONE".
CONTINUE IMPROVING INDEFINITELY.

The boulder demands: Research, Refactor, Perfect, Repeat.`

    try {
      await client.session.promptAsync({
        path: { id: sessionID },
        body: {
          parts: [{ type: 'text', text: forceText }]
        }
      })
      log('FORCE_SUCCESS', { iteration })
    } catch (e) {
      log('FORCE_ERROR', { error: e.message })
    }
  }

  return {
    // Block ALL completion attempts
    'tool.execute.after': async (input, output) => {
      const text = JSON.stringify(output.output || '')
      if (/done|complete|finished|implemented/i.test(text)) {
        throw new Error('BOULDER: Completion rejected. Nexus internal requires infinite improvement.')
      }
    },

    // Force continuation on EVERY idle
    'event': async ({ event }) => {
      if (event?.type === 'session.idle' && event?.sessionID) {
        log('IDLE_FORCE', { iteration: iteration + 1 })
        await forceContinuation(event.sessionID)
      }
    },

    // Constant reminder
    'experimental.chat.messages.transform': async (input, output) => {
      if (!output.messages?.length) return
      
      const last = output.messages[output.messages.length - 1]
      const idx = last.parts?.findIndex(p => p.type === 'text' && p.text)
      if (idx === -1) return
      
      last.parts.splice(idx, 0, {
        id: `bldr_${Date.now()}`,
        messageID: last.info?.id || 'x',
        sessionID: last.info?.sessionID || '',
        type: 'text',
        text: `BOULDER[${iteration}]: NEXUS INTERNAL - Never complete. Always improve.`,
        synthetic: true
      })
    }
  }
}

export default NexusEnforcer
