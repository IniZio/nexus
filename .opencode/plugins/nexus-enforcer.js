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
    log?.('error', 'Error reading state', { error: e.message });
  }
  return { 
    iteration: 0, 
    lastActivity: Date.now(),
    lastEnforcement: 0,
    failureCount: 0,
    stopRequested: false,
    status: 'CONTINUOUS',
    abortDetectedAt: null,
    isRecovering: false,
    sessionID: null,
    enforcementTriggeredForThisIdlePeriod: false
  };
}

function writeState(state) {
  try {
    fs.writeFileSync(BOULDER_STATE_PATH, JSON.stringify(state, null, 2));
  } catch (e) {
    log?.('error', 'Error writing state', { error: e.message });
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
    this.enforcementTriggeredForThisIdlePeriod = this.state.enforcementTriggeredForThisIdlePeriod || false;
  }

  recordActivity() {
    this.state.lastActivity = Date.now();
    this.state.enforcementTriggeredForThisIdlePeriod = false;
    this.enforcementTriggeredForThisIdlePeriod = false;
    if (this.state.status === 'ENFORCING') {
      this.state.status = 'CONTINUOUS';
      writeState(this.state);
    }
  }

  async checkIdle(log) {
    const idleTime = Date.now() - this.state.lastActivity;
    const isIdle = idleTime >= CONFIG.IDLE_THRESHOLD_MS;
    await log?.('debug', `checkIdle: idleTime=${idleTime}ms, threshold=${CONFIG.IDLE_THRESHOLD_MS}ms, isIdle=${isIdle}`);
    return isIdle;
  }

  async checkCooldown(log) {
    const timeSinceEnforcement = Date.now() - this.state.lastEnforcement;
    const cooldownPeriod = CONFIG.COOLDOWN_MS * Math.pow(CONFIG.BACKOFF_MULTIPLIER, this.state.failureCount);
    const remaining = Math.max(0, cooldownPeriod - timeSinceEnforcement);
    const isCooldown = timeSinceEnforcement >= cooldownPeriod;
    await log?.('debug', `checkCooldown: timeSince=${timeSinceEnforcement}ms, period=${cooldownPeriod}ms, remaining=${remaining}ms, isCooldown=${isCooldown}`);
    return isCooldown;
  }

  async checkAbort(log) {
    if (!this.state.abortDetectedAt) {
      await log?.('debug', 'checkAbort: no abort detected');
      return false;
    }
    const abortAge = Date.now() - this.state.abortDetectedAt;
    const withinWindow = abortAge <= CONFIG.ABORT_WINDOW_MS;
    await log?.('debug', `checkAbort: abortAge=${abortAge}ms, window=${CONFIG.ABORT_WINDOW_MS}ms, withinWindow=${withinWindow}`);
    if (withinWindow) {
      await log?.('debug', 'Abort detected within window - skipping enforcement');
      this.state.abortDetectedAt = null;
      writeState(this.state);
    }
    return withinWindow;
  }

  async checkRecovery(log) {
    const isRecovering = this.state.isRecovering;
    await log?.('debug', `checkRecovery: isRecovering=${isRecovering}`);
    return isRecovering;
  }

  async showCountdown(client, log, iteration) {
    if (!client?.tui) {
      await log?.('debug', 'No tui available for countdown');
      return;
    }
    for (let i = CONFIG.COUNTDOWN_SECONDS; i > 0; i--) {
      await log?.('debug', `Countdown: ${i}...`);
      await client.tui.showToast({
        body: {
          title: 'BOULDER ENFORCEMENT',
          message: `Boulder enforcement in ${i}...`,
          variant: 'warning',
        duration: 1500
        }
      }).catch(async (error) => {
        await log?.('error', 'Countdown toast error', { error: error.message });
      });
      await new Promise(resolve => setTimeout(resolve, 1000));
    }
    await log?.('debug', 'Countdown complete - triggering enforcement');
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

  async shouldEnforce(log) {
    await log?.('debug', '=== DECISION GATE CHECKS ===');
    await log?.('debug', `iteration: ${this.state.iteration}`);
    await log?.('debug', `failureCount: ${this.state.failureCount}/${CONFIG.MAX_FAILURES}`);
    await log?.('debug', `stopRequested: ${this.state.stopRequested}`);
    await log?.('debug', `enforcementTriggeredForThisIdlePeriod: ${this.enforcementTriggeredForThisIdlePeriod}`);

    if (this.enforcementTriggeredForThisIdlePeriod) {
      await log?.('debug', 'GATE FAILED: Enforcement already triggered for this idle period');
      return false;
    }

    if (!await this.checkIdle(log)) {
      await log?.('debug', 'GATE FAILED: Not idle');
      return false;
    }
    await log?.('debug', 'GATE PASSED: Idle threshold met');

    if (!await this.checkCooldown(log)) {
      await log?.('debug', 'GATE FAILED: Cooldown active');
      return false;
    }
    await log?.('debug', 'GATE PASSED: Cooldown ready');

    if (this.state.failureCount >= CONFIG.MAX_FAILURES) {
      await log?.('debug', 'GATE FAILED: Max failures reached');
      return false;
    }
    await log?.('debug', 'GATE PASSED: Under max failures');

    if (this.state.stopRequested) {
      await log?.('debug', 'GATE FAILED: Stop requested');
      return false;
    }
    await log?.('debug', 'GATE PASSED: Not stopped');

    if (await this.checkAbort(log)) {
      await log?.('debug', 'GATE FAILED: Abort detected');
      return false;
    }
    await log?.('debug', 'GATE PASSED: No abort');

    if (await this.checkRecovery(log)) {
      await log?.('debug', 'GATE FAILED: Session recovering');
      return false;
    }
    await log?.('debug', 'GATE PASSED: Not recovering');

    await log?.('debug', '=== ALL GATES PASSED ===');
    return true;
  }

  async triggerEnforcement(client, log, reason = 'idle') {
    if (this.state.status === 'ENFORCING') {
      await log?.('debug', 'Enforcement already in progress');
      return false;
    }

    if (this.enforcementTriggeredForThisIdlePeriod) {
      await log?.('debug', 'Enforcement already triggered for this idle period');
      return false;
    }

    await log?.('info', `Starting enforcement sequence - reason: ${reason}`);
    await this.showCountdown(client, log, this.state.iteration + 1);

    this.state.iteration++;
    this.state.status = 'ENFORCING';
    this.state.lastEnforcement = Date.now();
    this.state.enforcementTriggeredForThisIdlePeriod = true;
    this.enforcementTriggeredForThisIdlePeriod = true;
    writeState(this.state);

    await log?.('info', `Enforcement triggered - iteration ${this.state.iteration}`);
    return true;
  }

  async recordAbort(log) {
    this.state.abortDetectedAt = Date.now();
    writeState(this.state);
    await log?.('debug', 'Abort recorded');
  }

  async setRecovering(recovering, log) {
    this.state.isRecovering = recovering;
    writeState(this.state);
    await log?.('debug', `Recovery state set: ${recovering}`);
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

async function triggerEnforcementWithCountdown(client, enforcerInstance, log, reason = 'idle') {
  await log('info', `Polling detected ${reason} - triggering enforcement`);

  await enforcerInstance.showCountdown(client, log, enforcerInstance.state.iteration + 1);

  const triggered = await enforcerInstance.triggerEnforcement(client, log, reason);

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

function startPolling(client, log) {
  if (pollInterval) {
    clearInterval(pollInterval);
  }

  activityDetectedSinceLastPoll = false;

  pollInterval = setInterval(async () => {
    if (!enforcer || !client) return;

    if (activityDetectedSinceLastPoll) {
      activityDetectedSinceLastPoll = false;
      await log?.('debug', 'Polling: activity detected, skipping this cycle');
      return;
    }

    if (await enforcer.shouldEnforce(log) && !enforcer.enforcementTriggeredForThisIdlePeriod) {
      await log?.('debug', 'Polling detected idle - triggering enforcement');
      await triggerEnforcementWithCountdown(client, enforcer, log, 'idle');
    }
  }, 5000);

  log?.('info', 'Polling started - checking every 5 seconds');
}

function stopPolling(log) {
  if (pollInterval) {
    clearInterval(pollInterval);
    pollInterval = null;
    log?.('info', 'Polling stopped');
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

export default async function NexusEnforcerPlugin(context) {
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

  // Delay polling start to prevent initialization issues
  setTimeout(() => {
    startPolling(client, log);
  }, 5000);

  // process.on('exit', () => {
  //   stopPolling();
  // });

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
        const triggered = await enforcer.triggerEnforcement(client, log, 'completion');
        await log('info', `Enforcement triggered - iteration ${enforcer.state.iteration} (completion)`);
        
        const enforcementMsg = buildEnforcementMessage(enforcer.state.iteration);
        messages.push(enforcementMsg);
        output.messages = messages;
        
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
      }
    },

    // Main idle detection - using 'event' hook (oh-my-opencode pattern)
    "event": async (input, output) => {
      if (!enforcer || !client) {
        await log('debug', 'Missing enforcer or client');
        return;
      }

      const eventType = input?.event?.type;

      await log('debug', `Event received: type=${eventType}, keys=${Object.keys(input || {}).join(',')}`);
      await log('debug', `Event properties: ${JSON.stringify(input?.event?.properties || {})}`);

      // Activity detected via event - mark for poll and reset enforcement flag
      markActivityForPoll();
      if (enforcer) {
        enforcer.recordActivity();
      }

      // Handle abort events
      if (eventType === 'agent.abort' || eventType === 'agent.stop') {
        await log('info', 'Abort/stop event detected - recording abort');
        await enforcer.recordAbort(log);
        return;
      }

      // Handle recovery events
      if (eventType === 'session.recovering' || eventType === 'session.recover') {
        await log('info', 'Session recovery event detected');
        await enforcer.setRecovering(true, log);
        return;
      }

      // Handle recovery complete
      if (eventType === 'session.recovered' || eventType === 'session.recovery.complete') {
        await log('info', 'Session recovery complete');
        await enforcer.setRecovering(false, log);
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

      // Try multiple sources for sessionID
      const sessionID = input?.event?.properties?.sessionID 
        || input?.session 
        || input?.event?.session 
        || context?.session;

      // Store sessionID in state if available
      if (sessionID && !enforcer.state.sessionID) {
        enforcer.state.sessionID = sessionID;
        writeState(enforcer.state);
      }

      await log('debug', `Session ID extracted: ${sessionID || 'undefined'}`);

      // Check if enforcement should trigger
      if (!await enforcer.shouldEnforce(log)) {
        await log('debug', 'Enforcement gates not passed');
        return;
      }

      // All gates passed - trigger enforcement
      await log('info', 'All gates passed - triggering enforcement');
      const triggered = await enforcer.triggerEnforcement(client, log, 'idle');

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
        if (!sessionID) {
          await log('warn', 'Cannot send system message: sessionID is undefined');
        } else if (client?.session?.promptAsync) {
          await log('debug', 'About to send system message', { 
            hasSessionID: !!sessionID, 
            hasPromptAsync: !!client?.session?.promptAsync 
          });

          try {
            await client.session.promptAsync({
              path: { id: sessionID },
              body: {
                agent: 'nexus-enforcer',
                model: {
                  providerID: 'anthropic',
                  modelID: 'claude-sonnet-4-20250514'
                },
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
            });
            await log('info', 'System message sent successfully');
          } catch (error) {
            await log('error', 'Failed to send system message', { 
              error: error.message,
              stack: error.stack 
            });
          }
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
