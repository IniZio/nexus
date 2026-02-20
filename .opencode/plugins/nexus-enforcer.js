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
  ABORT_WINDOW_MS: 3000,           // 3s window to detect aborts
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
    status: 'CONTINUOUS',
    abortDetectedAt: null,
    isRecovering: false
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
    this.enforcementTriggeredForThisIdlePeriod = false;
  }

  recordActivity() {
    this.state.lastActivity = Date.now();
    this.enforcementTriggeredForThisIdlePeriod = false;
    if (this.state.status === 'ENFORCING') {
      this.state.status = 'CONTINUOUS';
      writeState(this.state);
    }
  }

  checkIdle() {
    const idleTime = Date.now() - this.state.lastActivity;
    const isIdle = idleTime >= CONFIG.IDLE_THRESHOLD_MS;
    console.log(`[nexus-enforcer] checkIdle: idleTime=${idleTime}ms, threshold=${CONFIG.IDLE_THRESHOLD_MS}ms, isIdle=${isIdle}`);
    return isIdle;
  }

  checkCooldown() {
    const timeSinceEnforcement = Date.now() - this.state.lastEnforcement;
    const cooldownPeriod = CONFIG.COOLDOWN_MS * Math.pow(CONFIG.BACKOFF_MULTIPLIER, this.state.failureCount);
    const remaining = Math.max(0, cooldownPeriod - timeSinceEnforcement);
    const isCooldown = timeSinceEnforcement >= cooldownPeriod;
    console.log(`[nexus-enforcer] checkCooldown: timeSince=${timeSinceEnforcement}ms, period=${cooldownPeriod}ms, remaining=${remaining}ms, isCooldown=${isCooldown}`);
    return isCooldown;
  }

  checkAbort() {
    if (!this.state.abortDetectedAt) {
      console.log('[nexus-enforcer] checkAbort: no abort detected');
      return false;
    }
    const abortAge = Date.now() - this.state.abortDetectedAt;
    const withinWindow = abortAge <= CONFIG.ABORT_WINDOW_MS;
    console.log(`[nexus-enforcer] checkAbort: abortAge=${abortAge}ms, window=${CONFIG.ABORT_WINDOW_MS}ms, withinWindow=${withinWindow}`);
    if (withinWindow) {
      console.log('[nexus-enforcer] Abort detected within window - skipping enforcement');
      this.state.abortDetectedAt = null;
      writeState(this.state);
    }
    return withinWindow;
  }

  checkRecovery() {
    const isRecovering = this.state.isRecovering;
    console.log(`[nexus-enforcer] checkRecovery: isRecovering=${isRecovering}`);
    return isRecovering;
  }

  async showCountdown(client, iteration) {
    if (!client?.tui) {
      console.log('[nexus-enforcer] No tui available for countdown');
      return;
    }
    for (let i = CONFIG.COUNTDOWN_SECONDS; i > 0; i--) {
      console.log(`[nexus-enforcer] Countdown: ${i}...`);
      await client.tui.showToast({
        body: {
          title: 'BOULDER ENFORCEMENT',
          message: `Boulder enforcement in ${i}...`,
          variant: 'warning',
        duration: 1500
        }
      }).catch(async (error) => {
        await log('error', 'Countdown toast error', { error: error.message });
      });
      await new Promise(resolve => setTimeout(resolve, 1000));
    }
    console.log(`[nexus-enforcer] Countdown complete - triggering enforcement`);
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
    console.log('[nexus-enforcer] === DECISION GATE CHECKS ===');
    console.log(`[nexus-enforcer] iteration: ${this.state.iteration}`);
    console.log(`[nexus-enforcer] failureCount: ${this.state.failureCount}/${CONFIG.MAX_FAILURES}`);
    console.log(`[nexus-enforcer] stopRequested: ${this.state.stopRequested}`);
    console.log(`[nexus-enforcer] enforcementTriggeredForThisIdlePeriod: ${this.enforcementTriggeredForThisIdlePeriod}`);

    if (this.enforcementTriggeredForThisIdlePeriod) {
      console.log('[nexus-enforcer] GATE FAILED: Enforcement already triggered for this idle period');
      return false;
    }

    if (!this.checkIdle()) {
      console.log('[nexus-enforcer] GATE FAILED: Not idle');
      return false;
    }
    console.log('[nexus-enforcer] GATE PASSED: Idle threshold met');

    if (!this.checkCooldown()) {
      console.log('[nexus-enforcer] GATE FAILED: Cooldown active');
      return false;
    }
    console.log('[nexus-enforcer] GATE PASSED: Cooldown ready');

    if (this.state.failureCount >= CONFIG.MAX_FAILURES) {
      console.log('[nexus-enforcer] GATE FAILED: Max failures reached');
      return false;
    }
    console.log('[nexus-enforcer] GATE PASSED: Under max failures');

    if (this.state.stopRequested) {
      console.log('[nexus-enforcer] GATE FAILED: Stop requested');
      return false;
    }
    console.log('[nexus-enforcer] GATE PASSED: Not stopped');

    if (this.checkAbort()) {
      console.log('[nexus-enforcer] GATE FAILED: Abort detected');
      return false;
    }
    console.log('[nexus-enforcer] GATE PASSED: No abort');

    if (this.checkRecovery()) {
      console.log('[nexus-enforcer] GATE FAILED: Session recovering');
      return false;
    }
    console.log('[nexus-enforcer] GATE PASSED: Not recovering');

    console.log('[nexus-enforcer] === ALL GATES PASSED ===');
    return true;
  }

  async triggerEnforcement(client, reason = 'idle') {
    if (this.state.status === 'ENFORCING') {
      console.log('[nexus-enforcer] Enforcement already in progress');
      return false;
    }

    if (this.enforcementTriggeredForThisIdlePeriod) {
      console.log('[nexus-enforcer] Enforcement already triggered for this idle period');
      return false;
    }

    console.log(`[nexus-enforcer] Starting enforcement sequence - reason: ${reason}`);
    await this.showCountdown(client, this.state.iteration + 1);

    this.state.iteration++;
    this.state.status = 'ENFORCING';
    this.state.lastEnforcement = Date.now();
    this.enforcementTriggeredForThisIdlePeriod = true;
    writeState(this.state);

    console.log(`[nexus-enforcer] Enforcement triggered - iteration ${this.state.iteration}`);
    return true;
  }

  recordAbort() {
    this.state.abortDetectedAt = Date.now();
    writeState(this.state);
    console.log('[nexus-enforcer] Abort recorded');
  }

  setRecovering(recovering) {
    this.state.isRecovering = recovering;
    writeState(this.state);
    console.log(`[nexus-enforcer] Recovery state set: ${recovering}`);
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
    const idleTime = Date.now() - this.state.lastActivity;
    const cooldownPeriod = CONFIG.COOLDOWN_MS * Math.pow(CONFIG.BACKOFF_MULTIPLIER, this.state.failureCount);
    const cooldownRemaining = Math.max(0, cooldownPeriod - (Date.now() - this.state.lastEnforcement));

    return {
      iteration: this.state.iteration,
      lastActivity: this.state.lastActivity,
      timeSinceActivity: idleTime,
      isEnforcing: this.state.status === 'ENFORCING',
      failureCount: this.state.failureCount,
      stopRequested: this.state.stopRequested,
      abortDetectedAt: this.state.abortDetectedAt,
      isRecovering: this.state.isRecovering,
      idleTimeMs: idleTime,
      cooldownRemainingMs: cooldownRemaining
    };
  }
}

let enforcer = null;
let pollInterval = null;
let activityDetectedSinceLastPoll = false;

async function triggerEnforcementWithCountdown(client, enforcerInstance, reason = 'idle') {
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

  await log('info', `Polling detected ${reason} - triggering enforcement`);

  await enforcerInstance.showCountdown(client, enforcerInstance.state.iteration + 1);

  const triggered = await enforcerInstance.triggerEnforcement(client, reason);

  if (triggered) {
    await log('info', `Enforcement triggered - iteration ${enforcerInstance.state.iteration} (${reason})`);

    if (client?.tui) {
      await log('debug', 'Attempting to show toast');
      await client.tui.showToast({
        body: {
          title: `BOULDER ENFORCEMENT - Iteration ${enforcerInstance.state.iteration}`,
          message: reason === 'idle'
            ? 'The boulder never stops. You have been idle for 30+ seconds. Generate new ideas. Refine code. Research.'
            : 'The boulder never stops. Completion detected. Continue improving.',
          variant: 'warning',
          duration: 15000
        }
      }).catch(async (error) => {
        await log('error', 'Failed to show toast', { error: error.message });
      });
      await log('debug', 'Toast call completed');
    }

    const enforcementMsg = buildEnforcementMessage(enforcerInstance.state.iteration);
    return { triggered: true, message: enforcementMsg };
  }

  return { triggered: false, message: null };
}

function startPolling(client) {
  if (pollInterval) {
    clearInterval(pollInterval);
  }

  activityDetectedSinceLastPoll = false;

  pollInterval = setInterval(async () => {
    if (!enforcer || !client) return;

    if (activityDetectedSinceLastPoll) {
      activityDetectedSinceLastPoll = false;
      console.log('[nexus-enforcer] Polling: activity detected, skipping this cycle');
      return;
    }

    if (enforcer.shouldEnforce() && !enforcer.enforcementTriggeredForThisIdlePeriod) {
      console.log('[nexus-enforcer] Polling detected idle - triggering enforcement');
      await triggerEnforcementWithCountdown(client, enforcer, 'idle');
    }
  }, 5000);

  console.log('[nexus-enforcer] Polling started - checking every 5 seconds');
}

function stopPolling() {
  if (pollInterval) {
    clearInterval(pollInterval);
    pollInterval = null;
    console.log('[nexus-enforcer] Polling stopped');
  }
}

function markActivityForPoll() {
  activityDetectedSinceLastPoll = true;
}

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

  startPolling(client);

  process.on('exit', () => {
    stopPolling();
  });

  return {
    // Track all tool activity
    "tool.execute.before": async (input, output) => {
      if (!enforcer) return;
      enforcer.recordActivity();
      enforcer.clearStopFlag();
      markActivityForPoll();
    },

    // Check for completion keywords and inject enforcement
    "experimental.chat.system.transform": async (input, output) => {
      if (!enforcer || !isMainAgent(input)) return;
      
      enforcer.recordActivity();
      markActivityForPoll();

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
        await log('debug', 'About to call triggerEnforcement with countdown');
        const triggered = await enforcer.triggerEnforcement(client, 'completion');
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
      }).catch(async (error) => {
        await log('error', 'Failed to show toast', { error: error.message });
      });
    }
  },

    // Main idle detection - using 'event' hook (oh-my-opencode pattern)
    "event": async (input, output) => {
      if (!enforcer || !client) {
        await log('debug', 'Missing enforcer or client');
        return;
      }

      const eventType = input?.event?.type;
      const sessionID = input?.event?.properties?.sessionID;

      await log('debug', `Event received: type=${eventType}, sessionID=${sessionID}`);

      // Activity detected via event - mark for poll and reset enforcement flag
      markActivityForPoll();
      if (enforcer) {
        enforcer.recordActivity();
      }

      // Handle abort events
      if (eventType === 'agent.abort' || eventType === 'agent.stop') {
        await log('info', 'Abort/stop event detected - recording abort');
        enforcer.recordAbort();
        return;
      }

      // Handle recovery events
      if (eventType === 'session.recovering' || eventType === 'session.recover') {
        await log('info', 'Session recovery event detected');
        enforcer.setRecovering(true);
        return;
      }

      // Handle recovery complete
      if (eventType === 'session.recovered' || eventType === 'session.recovery.complete') {
        await log('info', 'Session recovery complete');
        enforcer.setRecovering(false);
        return;
      }

      if (eventType !== 'session.idle') {
        await log('debug', `Not session.idle event (${eventType}), skipping`);
        return;
      }

      await log('info', 'Session idle event received - processing');

      if (!isMainAgent(input)) {
        await log('debug', 'Not main agent, skipping');
        return;
      }

      // Check if enforcement should trigger
      if (!enforcer.shouldEnforce()) {
        await log('debug', 'Enforcement gates not passed');
        return;
      }

      // All gates passed - trigger enforcement
      await log('info', 'All gates passed - triggering enforcement');
      const triggered = await enforcer.triggerEnforcement(client, 'idle');

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
      }).catch(async (error) => {
        await log('error', 'Failed to show toast', { error: error.message });
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
          }).catch(async (error) => {
            await log('error', 'Failed to send system message', { error: error.message });
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
