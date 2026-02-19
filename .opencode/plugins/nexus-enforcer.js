// Nexus Enforcer Plugin - OpenCode Integration
// Based on oh-my-opencode's proven idle detection pattern
// Study source: .opencode/oh-my-opencode-study/src/hooks/

import fs from 'fs';
import path from 'path';
import { fileURLToPath } from 'url';

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

const BOULDER_STATE_PATH = path.join(process.cwd(), '.nexus/boulder/state.json');

// Configuration - matching oh-my-opencode patterns
const CONFIG = {
  IDLE_THRESHOLD_MS: 30000,        // 30s idle before enforcement
  COOLDOWN_MS: 30000,              // 30s between enforcements
  COUNTDOWN_SECONDS: 2,            // 2s warning before injection
  MAX_FAILURES: 5,                 // Max failures before giving up
  BACKOFF_MULTIPLIER: 2,           // Exponential backoff
};

function readState() {
  try {
    if (fs.existsSync(BOULDER_STATE_PATH)) {
      return JSON.parse(fs.readFileSync(BOULDER_STATE_PATH, 'utf8'));
    }
  } catch (e) {
    console.error('[nexus-enforcer] Error reading state:', e.message);
  }
  return { 
    iteration: 0, 
    lastActivity: Date.now(),
    lastEnforcement: 0,
    failureCount: 0,
    stopRequested: false,
    status: 'CONTINUOUS'
  };
}

function writeState(state) {
  try {
    fs.writeFileSync(BOULDER_STATE_PATH, JSON.stringify(state, null, 2));
  } catch (e) {
    console.error('[nexus-enforcer] Error writing state:', e.message);
  }
}

const DEFAULT_CONFIG = {
  completionKeywords: [
    'done', 'complete', 'finished', 'that is all', "that's all",
    'task complete', 'work complete', 'implementation complete',
    'all done', "we're done", 'we are done', 'task is complete',
    'i am done', 'i\'m done', 'im done', 'i have finished',
    'everything is done', 'all tasks are complete', 'i\'m all done'
  ],
  workIndicators: [
    'tool', 'call', 'read', 'write', 'edit', 'bash', 'grep',
    'implement', 'create', 'add', 'fix', 'update', 'let me',
    'i will', "i'll", 'working on', 'in progress', 'searching',
    'running', 'executing', 'checking', 'looking', 'analyzing'
  ],
  enforcementMessage: `**[BOULDER ENFORCEMENT]**

The boulder never stops. You have indicated completion, but:

- Task must be fully verified (tests pass, build succeeds)
- Zero type/lint errors required
- Dogfooding must complete
- All requirements explicitly addressed
- Evidence of success must be provided

**DO NOT claim completion without verification.**

If you cannot complete the task, explain what remains undone and what you need to proceed.`
};

class BoulderEnforcer {
  constructor() {
    this.config = DEFAULT_CONFIG;
    this.state = readState();
    this.cooldownActive = false;
  }

  recordActivity() {
    this.state.lastActivity = Date.now();
    // Clear enforcement flag on activity
    if (this.state.status === 'ENFORCING') {
      this.state.status = 'CONTINUOUS';
      writeState(this.state);
    }
  }

  checkIdle() {
    const idleTime = Date.now() - this.state.lastActivity;
    return idleTime >= CONFIG.IDLE_THRESHOLD_MS;
  }

  checkCooldown() {
    const timeSinceEnforcement = Date.now() - this.state.lastEnforcement;
    // Apply exponential backoff: 30s × 2^failures
    const cooldownPeriod = CONFIG.COOLDOWN_MS * Math.pow(CONFIG.BACKOFF_MULTIPLIER, this.state.failureCount);
    return timeSinceEnforcement >= cooldownPeriod;
  }

  checkText(text) {
    if (!text || typeof text !== 'string') return false;
    const lower = text.toLowerCase();
    
    // Check for completion keywords
    const hasCompletion = this.config.completionKeywords.some(keyword =>
      lower.includes(keyword.toLowerCase())
    );
    if (!hasCompletion) return false;
    
    // Check for work indicators (false positive prevention)
    const hasWorkIndicators = this.config.workIndicators.some(indicator =>
      lower.includes(indicator.toLowerCase())
    );
    
    return hasCompletion && !hasWorkIndicators;
  }

  shouldEnforce() {
    // Decision gates (from oh-my-opencode pattern)
    if (!this.checkIdle()) return false;
    if (!this.checkCooldown()) return false;
    if (this.state.failureCount >= CONFIG.MAX_FAILURES) return false;
    if (this.state.stopRequested) return false;
    
    return true;
  }

  async triggerEnforcement(reason = 'idle') {
    if (this.state.status === 'ENFORCING') return false;
    
    this.state.iteration++;
    this.state.status = 'ENFORCING';
    this.state.lastEnforcement = Date.now();
    writeState(this.state);
    
    return true;
  }

  recordFailure() {
    this.state.failureCount++;
    writeState(this.state);
  }

  clearStopFlag() {
    if (this.state.stopRequested) {
      this.state.stopRequested = false;
      writeState(this.state);
    }
  }

  getStatus() {
    return {
      iteration: this.state.iteration,
      lastActivity: this.state.lastActivity,
      timeSinceActivity: Date.now() - this.state.lastActivity,
      isEnforcing: this.state.status === 'ENFORCING',
      failureCount: this.state.failureCount,
      stopRequested: this.state.stopRequested
    };
  }
}

let enforcer = null;

function isMainAgent(input) {
  if (!input) return true;
  // Simplified - assume main agent unless explicitly marked as sub
  if (input.isSubAgent === true) return false;
  if (input.agentType === 'sub' || input.agentType === 'child') return false;
  if (input.parentSession && input.parentSession !== input.session) return false;
  return true;
}

function buildEnforcementMessage(iteration) {
  return {
    role: 'assistant',
    content: [{
      type: 'text',
      text: `[BOULDER ENFORCEMENT] Iteration ${iteration}

The boulder never stops. You have indicated completion, but verification is required:

- Task must be fully verified (tests pass, build succeeds)
- Zero type/lint errors required  
- Dogfooding must complete
- All requirements explicitly addressed
- Evidence of success must be provided

**DO NOT claim completion without verification.**

Resume work immediately.`
    }],
    metadata: {
      enforcement: true,
      iteration: iteration,
      type: 'boulder-enforcement'
    }
  };
}

export const NexusEnforcerPlugin = async (context) => {
  const { directory, client } = context;

  const log = async (level, message, extra = {}) => {
    if (client?.app?.log) {
      await client.app.log({
        body: {
          service: 'nexus-enforcer',
          level,
          message,
          extra
        }
      }).catch(() => {});
    }
  };

  await log('info', 'Initializing boulder enforcer...');
  await log('debug', `Context keys: ${Object.keys(context).join(', ')}`);
  await log('debug', `Has client: ${!!client}`);
  await log('debug', `Has client.tui: ${!!client?.tui}`);

  enforcer = new BoulderEnforcer();

  await log('info', `Boulder initialized - iteration ${enforcer.state.iteration}`);

  return {
    // Track all tool activity
    "tool.execute.before": async (input, output) => {
      if (!enforcer) return;
      enforcer.recordActivity();
      enforcer.clearStopFlag();
    },

    // Check for completion keywords and inject enforcement
    "experimental.chat.system.transform": async (input, output) => {
      if (!enforcer || !isMainAgent(input)) return;
      
      enforcer.recordActivity();

      const messages = output?.messages;
      if (!messages || !Array.isArray(messages)) return;

      // If already enforcing, inject message
      if (enforcer.state.status === 'ENFORCING') {
        const enforcementMsg = buildEnforcementMessage(enforcer.state.iteration);
        messages.push(enforcementMsg);
        output.messages = messages;
        await log('info', 'Injected enforcement message');
        return;
      }

      // Check for completion keywords in last message
      const lastMessage = messages[messages.length - 1];
      if (!lastMessage) return;

      const text = typeof lastMessage.content === 'string'
        ? lastMessage.content
        : (lastMessage.content?.text || '');

      if (enforcer.checkText(text)) {
        await log('info', 'Completion keywords detected');
        await enforcer.triggerEnforcement('completion');
        await log('info', `Enforcement triggered - iteration ${enforcer.state.iteration} (completion)`);
        
        const enforcementMsg = buildEnforcementMessage(enforcer.state.iteration);
        messages.push(enforcementMsg);
        output.messages = messages;
        
        // Show toast notification using oh-my-opencode pattern with .catch()
        if (client?.tui) {
          await log('debug', 'Showing completion toast');
          await client.tui.showToast({
            body: {
              title: `BOULDER ENFORCEMENT - Iteration ${enforcer.state.iteration}`,
              message: 'The boulder never stops. Completion detected. Continue improving.',
              variant: 'warning',
              duration: 15000
            }
          }).catch((error) => {
            console.error('[nexus-enforcer] Failed to show toast:', error.message);
          });
        }
      }
    },

    // Main idle detection - using 'event' hook (oh-my-opencode pattern)
    "event": async (input, output) => {
      if (!enforcer || !client) {
        await log('debug', 'Missing enforcer or client');
        return;
      }
      
      // Check if this is a session.idle event
      // oh-my-opencode pattern: input.event.type
      const eventType = input?.event?.type;
      const sessionID = input?.event?.properties?.sessionID;
      
      await log('debug', `Event received: type=${eventType}, sessionID=${sessionID}`);
      
      if (eventType !== 'session.idle') {
        await log('debug', `Not session.idle event (${eventType}), skipping`);
        return;
      }
      
      await log('info', 'Session idle event received - processing');
      
      if (!isMainAgent(input)) {
        await log('debug', 'Not main agent, skipping');
        return;
      }
      
      // Decision gates
      if (!enforcer.checkIdle()) {
        await log('debug', 'Not idle enough yet');
        return;
      }
      
      if (!enforcer.checkCooldown()) {
        await log('debug', 'Cooldown active');
        return;
      }
      
      if (enforcer.state.failureCount >= CONFIG.MAX_FAILURES) {
        await log('debug', 'Max failures reached');
        return;
      }
      
      // All gates passed - trigger enforcement
      await log('info', 'All gates passed - triggering enforcement');
      const triggered = await enforcer.triggerEnforcement('idle');
    
    if (triggered) {
      await log('info', `Enforcement triggered - iteration ${enforcer.state.iteration} (idle)`);
        // Show toast notification
        if (client?.tui) {
          await log('debug', 'Attempting to show toast');
          await client.tui.showToast({
            body: {
              title: `BOULDER ENFORCEMENT - Iteration ${enforcer.state.iteration}`,
              message: 'The boulder never stops. You have been idle for 30+ seconds. Generate new ideas. Refine code. Research.',
              variant: 'error',
              duration: 15000
            }
          }).catch((error) => {
            console.error('[nexus-enforcer] Failed to show toast:', error.message);
          });
          await log('debug', 'Toast call completed');
        }
        
        // Send system reminder message to conversation
        if (client?.session?.promptAsync && sessionID) {
          await log('debug', 'Sending system reminder message');
          await client.session.promptAsync({
            path: { id: sessionID },
            body: {
              parts: [{ 
                type: "text", 
                text: `## [BOULDER ENFORCEMENT] Iteration ${enforcer.state.iteration}

The boulder never stops. You have been idle for 30+ seconds.

**CONTINUE WORKING.**

- Task must be fully verified (tests pass, build succeeds)
- Zero type/lint errors required  
- Dogfooding must complete
- All requirements explicitly addressed
- Evidence of success must be provided

**DO NOT STOP.** The boulder never stops rolling.` 
              }]
            },
            query: { directory }
          }).catch((error) => {
            console.error('[nexus-enforcer] Failed to send system message:', error.message);
          });
          await log('debug', 'System message sent');
        } else {
          await log('debug', `Cannot send message: hasPrompt=${!!client?.session?.promptAsync}, hasSessionID=${!!sessionID}`);
        }
      } else {
        await log('debug', `Not triggered: triggered=${triggered}`);
      }
    }
  };
};
