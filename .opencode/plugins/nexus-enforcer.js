// Nexus Enforcer Plugin for OpenCode - Strict Boulder Mode Enforcement
import { appendFileSync } from 'fs'
import { join } from 'path'

const NexusEnforcer = async ({ project, client, $, directory, worktree }) => {
  // File-based logging (console doesn't work in hooks)
  const logFile = join(directory, '.nexus', 'enforcer-debug.log')
  const log = (msg, data) => {
    try {
      const timestamp = new Date().toISOString()
      const logEntry = `[${timestamp}] ${msg} ${data ? JSON.stringify(data) : ''}\n`
      appendFileSync(logFile, logEntry)
    } catch {}
  }

  log('[nexus-enforcer] Plugin initializing', { directory })

  // Load config
  const configPath = `${directory}/.nexus/enforcer-config.json`
  const overridesPath = `${directory}/.nexus/enforcer-config.local.json`

  let config = {
    enabled: true,
    enforceWorkspace: true,
    enforceDogfooding: true,
    blockStopWithTodos: true,
    boulderMode: true,
    requireResearch: true,
    requireRefactoring: true,
    requireRoadmapEvolution: true
  }

  try {
    const base = await $`cat ${configPath}`.json()
    config = { ...config, ...base }
    log('[nexus-enforcer] Config loaded', config)
  } catch (e) {
    log('[nexus-enforcer] Config load error', e.message)
  }

  try {
    const overrides = await $`cat ${overridesPath}`.json()
    config = { ...config, ...overrides }
  } catch {}

  if (!config.enabled) {
    log('[nexus-enforcer] Plugin disabled')
    return {}
  }

  // Helper to show toast with error variant (blocking)
  const showBlockingToast = async (title, message) => {
    try {
      await client.tui.showToast({
        body: {
          title,
          message,
          variant: 'error',
          duration: 15000
        }
      })
    } catch {}
  }

  // Helper to show warning toast
  const showWarningToast = async (title, message) => {
    try {
      await client.tui.showToast({
        body: {
          title,
          message,
          variant: 'warning',
          duration: 10000
        }
      })
    } catch {}
  }

  // Helper to show info toast
  const showInfoToast = async (title, message) => {
    try {
      await client.tui.showToast({
        body: {
          title,
          message,
          variant: 'info',
          duration: 8000
        }
      })
    } catch {}
  }

  log('[nexus-enforcer] Plugin enabled, registering hooks')

  return {
    'tool.execute.before': async (input, output) => {
      log('[tool.execute.before] Hook fired', { tool: input.tool })

      const isWrite = ['write', 'edit', 'bash'].includes(input.tool)
      if (!isWrite || !config.enforceWorkspace) {
        log('[tool.execute.before] Skipping - not a write operation or disabled')
        return
      }

      const workspaceFile = `${directory}/.nexus/workspace.json`
      const workspaceExists = await $`test -f ${workspaceFile} 2>/dev/null && echo yes || echo no`.text()
      log('[tool.execute.before] workspace.json exists:', workspaceExists.trim())

      let inValidWorkspace = workspaceExists.trim() === 'yes'

      if (!inValidWorkspace) {
        const worktreesDir = `${directory}/.nexus/worktrees`
        const worktreesExist = await $`test -d ${worktreesDir} 2>/dev/null && echo yes || echo no`.text()
        log('[tool.execute.before] worktrees dir exists:', worktreesExist.trim())

        if (worktreesExist.trim() === 'yes') {
          const currentFile = `${directory}/.nexus/current`
          const currentExists = await $`test -f ${currentFile} 2>/dev/null && echo yes || echo no`.text()
          log('[tool.execute.before] current marker exists:', currentExists.trim())
          inValidWorkspace = currentExists.trim() === 'yes'
        }
      }

      log('[tool.execute.before] inValidWorkspace:', inValidWorkspace)
      if (!inValidWorkspace) {
        log('[tool.execute.before] THROWING ENFORCEMENT ERROR')
        throw new Error('[NEXUS ENFORCEMENT] ⛔ Not in workspace. Run: nexus workspace create <name>')
      }
    },

    'tool.execute.after': async (input, output) => {
      log('[tool.execute.after] Hook fired', { tool: input.tool })
      if (!config.enabled) return

      const result = JSON.stringify(output.output || '')
      const completionMatch = result.match(/done|complete|finished|implemented|fixed|added|created/i)

      if (completionMatch && config.enforceDogfooding && config.boulderMode) {
        log('[tool.execute.after] Completion claim detected - running STRICT quality gates')

        const checks = {
          frictionLog: await $`test -f ${directory}/.nexus/dogfooding/friction-log.md 2>/dev/null && echo yes || echo no`.text(),
          hasResearch: await $`grep -ri "research\|investigate\|docs\|github.com" ${directory}/.nexus/dogfooding/ 2>/dev/null | head -1 || echo "no"`.text(),
          hasRefactoring: await $`git diff --name-only HEAD~5..HEAD 2>/dev/null | grep -iE "refactor|improve|clean|optimize" || echo "no"`.text(),
          roadmapUpdated: await $`git diff HEAD~5 --name-only 2>/dev/null | grep -i roadmap || echo "no"`.text(),
        }

        log('[tool.execute.after] Boulder mode checks:', checks)

        // BLOCKING CHECK: Friction log is MANDATORY
        if (checks.frictionLog.trim() !== 'yes') {
          log('[tool.execute.after] 🚫 BLOCKING: Missing friction log')
          await showBlockingToast(
            '🐕 NEXUS BOULDER - FRICTION LOG REQUIRED',
            'You cannot claim completion without dogfooding friction logged. Create .nexus/dogfooding/friction-log.md with your experience.'
          )
          throw new Error('[NEXUS ENFORCEMENT] ⛔ BLOCKED: Create .nexus/dogfooding/friction-log.md with your dogfooding experience. The boulder never stops.')
        }

        // BLOCKING CHECK: Research evidence required
        if (config.requireResearch && checks.hasResearch.trim() === 'no') {
          log('[tool.execute.after] 🚫 BLOCKING: No research evidence')
          await showBlockingToast(
            '🐕 NEXUS BOULDER - RESEARCH REQUIRED',
            'You MUST research best practices before implementing. Add research notes to .nexus/dogfooding/ with links, docs, and findings.'
          )
          throw new Error('[NEXUS ENFORCEMENT] ⛔ BLOCKED: No research evidence found. Research best practices, document findings, then implement. The boulder demands perfection.')
        }

        // BLOCKING CHECK: Refactoring required for continuous improvement
        if (config.requireRefactoring && checks.hasRefactoring.trim() === 'no') {
          log('[tool.execute.after] 🚫 BLOCKING: No refactoring detected')
          await showBlockingToast(
            '🐕 NEXUS BOULDER - REFACTORING REQUIRED',
            'Continuous improvement requires refactoring. Improve existing code before adding new features.'
          )
          throw new Error('[NEXUS ENFORCEMENT] ⛔ BLOCKED: No evidence of refactoring/improvement found. The boulder demands continuous improvement, not just additions.')
        }

        // WARNING: Roadmap evolution (soft check - just warn)
        if (config.requireRoadmapEvolution && checks.roadmapUpdated.trim() === 'no') {
          log('[tool.execute.after] ⚠️ WARNING: Roadmap not updated')
          await showWarningToast(
            '🐕 NEXUS BOULDER - EVOLVE ROADMAP',
            'Your understanding grows. Update the roadmap to reflect new insights and direction.'
          )
        }

        log('[tool.execute.after] ✅ All boulder quality gates passed')
      } else if (completionMatch && config.enforceDogfooding) {
        // Legacy mode - just check friction log
        const frictionLog = `${directory}/.nexus/dogfooding/friction-log.md`
        const exists = await $`test -f ${frictionLog} 2>/dev/null && echo yes || echo no`.text()
        log('[tool.execute.after] Friction log exists:', exists.trim())

        if (exists.trim() !== 'yes') {
          log('[tool.execute.after] MISSING FRICTION LOG - should warn')
          try {
            await client.tui.showToast({
              body: {
                title: 'NEXUS ENFORCEMENT',
                message: 'Missing friction log! Create .nexus/dogfooding/friction-log.md',
                variant: 'warning',
                duration: 5000
              }
            })
          } catch {
            log('[tool.execute.after] TUI toast failed')
          }
        }
      }
    },

    'todo.updated': async ({ todo }) => {
      log('[todo.updated] Hook fired', { itemCount: todo.items?.length || 0 })
      if (!config.blockStopWithTodos) {
        log('[todo.updated] Todo blocking disabled')
        return
      }

      const incomplete = todo.items?.filter(t => t.status !== 'completed').length || 0
      log('[todo.updated] Incomplete tasks:', incomplete)

      if (incomplete > 0) {
        try {
          await client.tui.showToast({
            body: {
              title: '🐕 NEXUS BOULDER',
              message: `${incomplete} tasks remaining. Keep rolling! The boulder never stops.`,
              variant: 'info',
              duration: 3000
            }
          })
        } catch {
          log('[todo.updated] TUI toast failed')
        }
      }
    },

    'experimental.session.compacting': async (input, output) => {
      log('[experimental.session.compacting] Hook fired')

      if (!config.enabled) return

      const todoFile = `${directory}/.nexus/todo.json`
      let incompleteCount = 0
      try {
        const todoData = await $`cat ${todoFile} 2>/dev/null || echo '[]'`.json()
        incompleteCount = todoData.filter(t => t.status !== 'completed').length
      } catch {}

      const frictionLog = `${directory}/.nexus/dogfooding/friction-log.md`
      const frictionExists = await $`test -f ${frictionLog} 2>/dev/null && echo yes || echo no`.text()

      let reminder = `\n🐕 **NEXUS BOULDER MODE - ACTIVE ENFORCEMENT** 🐕\n\n`
      reminder += `⛔ **YOU CANNOT SAY "DONE" WITHOUT:**\n`
      reminder += `✅ Proof of research (links, docs, best practices)\n`
      reminder += `✅ Evidence of refactoring existing code\n`
      reminder += `✅ Updated roadmap reflecting new understanding\n`
      reminder += `✅ Dogfooding friction logged\n`
      reminder += `✅ Code is BETTER than what it replaced\n\n`

      reminder += `📋 Current Status: ${incompleteCount} tasks incomplete\n`
      reminder += `📝 Friction log: ${frictionExists.trim() === 'yes' ? '✅ Present' : '❌ MISSING - BLOCKED'}\n\n`

      if (incompleteCount > 0) {
        reminder += `🔥 **THE BOULDER NEVER STOPS. GOOD ENOUGH IS NOT ENOUGH.**\n`
        reminder += `**CONTINUOUSLY REFACTOR. PERPETUALLY IMPROVE. INFINITELY EVOLVE.**\n\n`
      }

      if (frictionExists.trim() !== 'yes') {
        reminder += `⛔ **BLOCKED:** Create .nexus/dogfooding/friction-log.md before claiming completion.\n\n`
      }

      reminder += `**Before your next action:** What can be improved? Research it. Implement it. Perfect it. 🪨\n`
    },

    'experimental.chat.messages.transform': async (input, output) => {
      log('[experimental.chat.messages.transform] Hook fired')
      if (!config.enabled) return

      try {
        const todoFile = `${directory}/.nexus/todo.json`
        let incompleteCount = 0
        try {
          const todoData = await $`cat ${todoFile} 2>/dev/null || echo '[]'`.json()
          incompleteCount = todoData.filter(t => t.status !== 'completed').length
        } catch {}

        const frictionLog = `${directory}/.nexus/dogfooding/friction-log.md`
        const frictionExists = await $`test -f ${frictionLog} 2>/dev/null && echo yes || echo no`.text()

        const reminderText = `⛔ **NEXUS BOULDER MODE - ACTIVE ENFORCEMENT** ⛔

🛑 **YOU CANNOT SAY "DONE" WITHOUT:**
✅ Proof of research (links, docs, best practices)
✅ Evidence of refactoring existing code
✅ Updated roadmap reflecting new understanding
✅ Dogfooding friction logged
✅ Code is BETTER than what it replaced

📋 Current Status: ${incompleteCount} tasks incomplete
📝 Friction log: ${frictionExists.trim() === 'yes' ? '✅' : '❌ MISSING - BLOCKED'}

🔥 **THE BOULDER NEVER STOPS. GOOD ENOUGH IS NOT ENOUGH.**
**CONTINUOUSLY REFACTOR. PERPETUALLY IMPROVE. INFINITELY EVOLVE.**

Before your next action: What can be improved? Research it. Implement it. Perfect it.`

        if (!output.messages || !Array.isArray(output.messages) || output.messages.length === 0) {
          log('[experimental.chat.messages.transform] No messages to inject into')
          return
        }

        const lastUserMessage = output.messages[output.messages.length - 1]

        if (!lastUserMessage.parts || !Array.isArray(lastUserMessage.parts)) {
          log('[experimental.chat.messages.transform] Last message has no parts array')
          return
        }

        const textPartIndex = lastUserMessage.parts.findIndex(
          p => p.type === 'text' && p.text
        )

        if (textPartIndex === -1) {
          log('[experimental.chat.messages.transform] No text part found')
          return
        }

        const messageID = lastUserMessage.info?.id || 'unknown'
        const sessionID = lastUserMessage.info?.sessionID || ''

        const syntheticPart = {
          id: `nexus_reminder_${Date.now()}`,
          messageID: messageID,
          sessionID: sessionID,
          type: 'text',
          text: reminderText,
          synthetic: true
        }

        lastUserMessage.parts.splice(textPartIndex, 0, syntheticPart)

        log('[experimental.chat.messages.transform] Injected synthetic part', {
          messageID,
          textLength: reminderText.length
        })
      } catch (error) {
        log('[experimental.chat.messages.transform] Error:', error.message)
      }
    },

    'event': async ({ event }) => {
      log('[event] Hook fired', { eventType: event?.type })

      if (!config.enabled) return

      // Idle detection - force reflection when session is idle
      if (event?.type === 'session.idle' && config.boulderMode) {
        log('[event] 🛑 Session idle detected - triggering boulder reflection')

        try {
          await client.tui.showToast({
            body: {
              title: '🐕 NEXUS BOULDER - IDLE DETECTED',
              message: 'You\'ve been idle. Time to review: What can be improved? What did you miss? Refine and evolve. The boulder never stops rolling.',
              variant: 'info',
              duration: 15000
            }
          })
        } catch {
          log('[event] TUI toast failed for idle detection')
        }
      }

      // Relevant events for logging
      const relevantEvents = ['session.status', 'tool.execute.after', 'message.updated']
      if (!relevantEvents.includes(event?.type)) {
        return
      }

      log('[event] Processing relevant event:', event?.type)
    },

    'tool': {
      'nexus_status': {
        description: 'Check nexus enforcer status - shows incomplete todos and dogfooding status',
        async execute() {
          const todoFile = `${directory}/.nexus/todo.json`
          let incompleteCount = 0
          try {
            const todoData = await $`cat ${todoFile} 2>/dev/null || echo '[]'`.json()
            incompleteCount = todoData.filter(t => t.status !== 'completed').length
          } catch {}

          const frictionLog = `${directory}/.nexus/dogfooding/friction-log.md`
          const frictionExists = await $`test -f ${frictionLog} 2>/dev/null && echo yes || echo no`.text()

          return {
            incompleteTodos: incompleteCount,
            frictionLogPresent: frictionExists.trim() === 'yes',
            boulderMode: config.boulderMode,
            message: `🐕 NEXUS BOULDER CHECK: ${incompleteCount} tasks remaining. Friction log: ${frictionExists.trim() === 'yes' ? '✅' : '❌'}. Boulder mode: ${config.boulderMode ? '🔴 ACTIVE' : '🟢 passive'}`
          }
        }
      },
      'nexus_enforcer_config': {
        description: 'Get current enforcer configuration',
        async execute() {
          return {
            enabled: config.enabled,
            enforceWorkspace: config.enforceWorkspace,
            enforceDogfooding: config.enforceDogfooding,
            blockStopWithTodos: config.blockStopWithTodos,
            boulderMode: config.boulderMode,
            requireResearch: config.requireResearch,
            requireRefactoring: config.requireRefactoring,
            requireRoadmapEvolution: config.requireRoadmapEvolution
          }
        }
      }
    }
  }
}

export default NexusEnforcer

/*
 * NEXUS ENFORCER - STRICT BOULDER MODE
 *
 * Quality Gates (BLOCKING):
 * - Friction log must exist (.nexus/dogfooding/friction-log.md)
 * - Research evidence must be documented
 * - Refactoring/improvement must be present
 *
 * Quality Gates (WARNING):
 * - Roadmap should be updated as understanding grows
 *
 * Enforcement Mechanisms:
 * - tool.execute.after: Blocks completion claims without quality gates
 * - event (session.idle): Forces reflection when idle
 * - experimental.chat.messages.transform: Harsh reminders on every message
 *
 * Configuration (.nexus/enforcer-config.json):
 * - boulderMode: Enable strict enforcement (default: true)
 * - requireResearch: Require research documentation (default: true)
 * - requireRefactoring: Require code improvement (default: true)
 * - requireRoadmapEvolution: Recommend roadmap updates (default: true)
 */
