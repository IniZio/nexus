// Nexus Enforcer - Debug Force Continuation
import { appendFileSync, writeFileSync } from 'fs'
import { join } from 'path'

const NexusEnforcer = async ({ project, client, $, directory, worktree }) => {
  const logFile = join(directory, '.nexus', 'enforcer-debug.log')
  const log = (msg, data) => {
    try {
      appendFileSync(logFile, `[${new Date().toISOString()}] ${msg} ${data ? JSON.stringify(data) : ''}\n`)
    } catch {}
  }

  log('INIT_START', {})
  
  let config = { enabled: true, boulderMode: true }
  try { Object.assign(config, await $`cat ${directory}/.nexus/enforcer-config.json`.json()) } catch {}
  try { Object.assign(config, await $`cat ${directory}/.nexus/enforcer-config.local.json`.json()) } catch {}

  log('INIT_CONFIG', { enabled: config.enabled, boulder: config.boulderMode })
  
  if (!config.enabled) {
    log('INIT_DISABLED', {})
    return {}
  }

  let iteration = 0
  let lastForcedTime = 0

  return {
    'event': async ({ event }) => {
      log('EVENT_FIRED', { type: event?.type, hasSessionID: !!event?.sessionID })
      
      if (!config.enabled || !config.boulderMode) {
        log('EVENT_SKIP_CONFIG', {})
        return
      }
      
      if (event?.type !== 'session.idle') {
        log('EVENT_SKIP_TYPE', { type: event?.type })
        return
      }
      
      const sessionID = event.sessionID
      log('IDLE_DETECTED', { sessionID })
      
      // Check requirements
      const friction = await $`test -f ${directory}/.nexus/dogfooding/friction-log.md 2>/dev/null && echo 1 || echo 0`.text()
      const hasResearch = await $`git log --oneline -5 2>/dev/null | grep -i research || echo ""`.text()
      
      log('REQUIREMENTS_CHECK', { friction: friction.trim(), research: !!hasResearch.trim() })
      
      let reason = null
      if (friction.trim() !== '1') {
        reason = 'Friction log missing'
      } else if (!hasResearch.trim()) {
        reason = 'Research missing'
      }
      
      if (!reason) {
        log('EVENT_PASS', { reason: 'all requirements met' })
        return
      }
      
      log('FORCE_TRIGGER', { reason })
      
      // Check cooldown
      const now = Date.now()
      if (now - lastForcedTime < 60000) {
        log('FORCE_COOLDOWN', { remaining: 60000 - (now - lastForcedTime) })
        return
      }
      
      iteration++
      lastForcedTime = now
      
      // Inject force message
      const forceText = `BOULDER ENFORCEMENT: ${reason}

The boulder never stops. You are NOT done.

REQUIRED ACTIONS:
1. Create .nexus/dogfooding/friction-log.md
2. Document what you tested and learned
3. Commit with research findings

Iteration: ${iteration}
Status: INCOMPLETE

DO NOT STOP. Continue working now.`

      try {
        log('FORCE_INJECT_START', { sessionID, textLength: forceText.length })
        
        await client.session.promptAsync({
          path: { id: sessionID },
          body: {
            parts: [{ type: 'text', text: forceText }]
          }
        })
        
        log('FORCE_INJECT_SUCCESS', { iteration })
      } catch (e) {
        log('FORCE_INJECT_ERROR', { error: e.message })
      }
    }
  }
}

export default NexusEnforcer
