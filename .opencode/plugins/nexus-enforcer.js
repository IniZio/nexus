// Nexus Enforcer - Active Interval Enforcement
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

  // FORCE CONTINUATION
  const forceContinuation = async (reason) => {
    iteration++
    const tasks = []
    
    if (reason.includes('Friction')) {
      tasks.push('1. Create .nexus/dogfooding/friction-log.md')
      tasks.push('2. Document what you tested, what failed, what you learned')
      tasks.push('3. Log pain points and friction encountered')
    }
    
    if (reason.includes('Research')) {
      tasks.push('1. Search for best practices on current topic')
      tasks.push('2. Read at least 2 documentation sources')
      tasks.push('3. Document findings in commit or notes')
    }
    
    const continuationFile = `${directory}/.nexus/CONTINUATION_REQUIRED.txt`
    const content = `BOULDER BLOCKED: ${reason}

YOU MUST COMPLETE BEFORE PROCEEDING:
${tasks.join('\n')}

STATUS: INCOMPLETE
ITERATION: ${iteration}

The boulder never stops. Complete these tasks or explain why you cannot.`

    try { writeFileSync(continuationFile, content) } catch {}
    return { iteration, tasks, reason }
  }

  const checkBlock = async (text) => {
    if (!config.boulderMode || !/done|complete|finished/i.test(text)) return null
    
    const friction = await $`test -f ${directory}/.nexus/dogfooding/friction-log.md 2>/dev/null && echo 1 || echo 0`.text()
    if (friction.trim() !== '1') {
      return { block: true, reason: 'Friction log required', type: 'friction' }
    }
    
    const research = await $`git log --oneline -3 2>/dev/null | grep -i research || echo ""`.text()
    if (!research.trim()) {
      return { block: true, reason: 'Research required', type: 'research' }
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
        
        // Show toast notification
        try {
          await client.tui.showToast({
            body: {
              title: 'BOULDER ALERT',
              message: `Idle for ${Math.round(idleTime/1000)}s. Generate new ideas. Refine code. Research improvements.`,
              variant: 'warning',
              duration: 10000
            }
          })
        } catch {}
        
        // Create forced continuation with NEW IDEAS
        const ideas = [
          'Refactor existing code for better performance',
          'Research alternative implementations',
          'Add comprehensive error handling',
          'Improve documentation',
          'Write additional tests',
          'Optimize for edge cases',
          'Add monitoring/logging',
          'Review security considerations'
        ]
        const randomIdeas = ideas.sort(() => 0.5 - Math.random()).slice(0, 3)
        
        const continuationFile = `${directory}/.nexus/CONTINUATION_REQUIRED.txt`
        const content = `BOULDER INTERVAL ENFORCEMENT

IDLE TIME: ${Math.round(idleTime / 1000)}s
ITERATION: ${iteration}

YOU ARE IDLE. THE BOULDER DEMANDS CONTINUOUS IMPROVEMENT.

GENERATE NEW IDEAS NOW:
${randomIdeas.map((idea, i) => `${i + 1}. ${idea}`).join('\n')}

STATUS: MUST IMPLEMENT BEFORE PROCEEDING

The boulder never stops. Complete these or explain why you cannot.`

        try { writeFileSync(continuationFile, content) } catch {}
        
        lastActiveTime = Date.now()
      }
    }, 10000)
  }
  
  startIdleChecker()

  return {
    'tool.execute.before': async (input, output) => {
      await loadConfig()
      lastActiveTime = Date.now() // Reset idle timer on activity
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
      lastActiveTime = Date.now() // Reset idle timer on activity
      if (!config.enabled || !config.enforceDogfooding) return
      
      const check = await checkBlock(JSON.stringify(output.output || ''))
      if (check?.block) {
        const force = await forceContinuation(check.reason)
        log('FORCE', { iteration: force.iteration, tasks: force.tasks.length })
        
        throw new Error(
          `BOULDER[${force.iteration}] BLOCKED: ${check.reason}\n\n` +
          `AUTO-GENERATED TASKS:\n${force.tasks.join('\n')}\n\n` +
          `Complete these tasks before claiming completion.`
        )
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
        reminder += 'COMPLETION BLOCKED. Check .nexus/CONTINUATION_REQUIRED.txt'
      } else {
        reminder += 'Idle 30s triggers new ideas. Keep iterating.'
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
