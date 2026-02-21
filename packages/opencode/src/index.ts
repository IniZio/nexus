import fs from 'fs';
import path from 'path';

interface ToolDefinition {
  description: string;
  args: Record<string, unknown>;
  execute: (args: Record<string, unknown>, ctx: { directory: string }) => Promise<string>;
}

export type { ToolDefinition };

function tool(def: ToolDefinition): ToolDefinition {
  return def;
}

export interface BoulderState {
  iteration: number;
  lastActivity: number;
  lastEnforcement: number;
  failureCount: number;
  stopRequested: boolean;
  status: 'CONTINUOUS' | 'ENFORCING' | 'PAUSED';
  abortDetectedAt: number | null;
  isRecovering: boolean;
  sessionID: string | null;
  enforcementTriggeredForThisIdlePeriod: boolean;
  directory?: string;
}

export interface Config {
  IDLE_THRESHOLD_MS: number;
  COOLDOWN_MS: number;
  COUNTDOWN_SECONDS: number;
  ABORT_WINDOW_MS: number;
  MAX_FAILURES: number;
  BACKOFF_MULTIPLIER: number;
}

export interface EnforcerConfig {
  completionKeywords: string[];
  workIndicators: string[];
  enforcementMessage: string;
}

export interface PluginContext {
  directory: string;
  client?: {
    app?: {
      log?: (args: { body: { service: string; level: string; message: string; extra?: Record<string, unknown> } }) => Promise<void>;
    };
    tui?: {
      showToast: (args: { body: { title: string; message: string; variant: string; duration: number } }) => Promise<void>;
    };
    session?: {
      promptAsync?: (args: { path: { id: string }; body: { parts: Array<{ type: string; text: string }>; agent?: string; model?: { providerID: string; modelID: string } }; query: { directory: string } }) => Promise<unknown>;
    };
  };
  session?: string;
}

export interface HookInput {
  tool?: string;
  text?: string;
  event?: {
    type?: string;
    properties?: {
      sessionID?: string;
    };
    session?: string;
  };
  session?: string;
  source?: string;
  role?: string;
  actor?: string;
  isSubAgent?: boolean;
  agentType?: string;
  parentSession?: string;
}

export interface MessageContent {
  text?: string;
  type?: string;
}

export interface HookOutput {
  messages?: Array<{ content: string | MessageContent }>;
  response?: {
    content: Array<{ type: string; text: string }>;
  };
}

export interface OutputMessages {
  messages: Array<{ content: string | MessageContent }>;
}

type LogFunction = ((level: string, message: string, extra?: Record<string, unknown>) => Promise<void>) | null;

function getBoulderStatePath(directory: string): string {
  return path.join(directory, '.nexus/boulder/state.json');
}

function getPauseFlagPath(directory: string): string {
  return path.join(directory, '.nexus/boulder/pause.flag');
}

function isPaused(directory: string): boolean {
  const PAUSE_FLAG_PATH = getPauseFlagPath(directory);
  try {
    return fs.existsSync(PAUSE_FLAG_PATH);
  } catch {
    return false;
  }
}

const CONFIG: Config = {
  IDLE_THRESHOLD_MS: 30000,
  COOLDOWN_MS: 30000,
  COUNTDOWN_SECONDS: 2,
  ABORT_WINDOW_MS: 3000,
  MAX_FAILURES: 5,
  BACKOFF_MULTIPLIER: 2,
};

function readState(directory: string): BoulderState {
  const BOULDER_STATE_PATH = getBoulderStatePath(directory);
  try {
    if (fs.existsSync(BOULDER_STATE_PATH)) {
      return JSON.parse(fs.readFileSync(BOULDER_STATE_PATH, 'utf8'));
    }
  } catch (e: unknown) {
    const error = e as Error;
    console?.log?.('error', 'Error reading state', { error: error.message });
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
    enforcementTriggeredForThisIdlePeriod: false,
  };
}

function writeState(directory: string, state: BoulderState): void {
  const BOULDER_STATE_PATH = getBoulderStatePath(directory);
  const stateDir = path.dirname(BOULDER_STATE_PATH);
  try {
    if (!fs.existsSync(stateDir)) {
      fs.mkdirSync(stateDir, { recursive: true });
    }
    fs.writeFileSync(BOULDER_STATE_PATH, JSON.stringify(state, null, 2));
  } catch (e: unknown) {
    const error = e as Error;
    console?.log?.('error', 'Error writing state', { error: error.message });
  }
}

const DEFAULT_CONFIG: EnforcerConfig = {
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
- All requirements explicitly addressed
- Evidence of success must be provided
- If you were asking to proceed with what is not destructive and not multi-choice, just proceed.
- If you claimed new change "should work" or "should be fine", that means critical issue in your testing since it's probably mocked. Step back and verify with real tests and real environment.

**DO NOT claim completion without verification.**

If you cannot complete the task, explain what remains undone and what you need to proceed.`
};

class BoulderEnforcer {
  config: EnforcerConfig;
  state: BoulderState;
  cooldownActive: boolean;
  enforcementTriggeredForThisIdlePeriod: boolean;
  toolInProgress: boolean;
  permissionPending: boolean;
  directory: string;

  constructor(directory: string) {
    this.directory = directory;
    this.config = DEFAULT_CONFIG;
    this.state = readState(directory);
    this.cooldownActive = false;
    this.enforcementTriggeredForThisIdlePeriod = this.state.enforcementTriggeredForThisIdlePeriod || false;
    this.toolInProgress = false;
    this.permissionPending = false;
  }

  reloadState(): void {
    const diskState = readState(this.directory);
    if (diskState.status !== undefined) {
      this.state.status = diskState.status;
    }
    if (diskState.stopRequested !== undefined) {
      this.state.stopRequested = diskState.stopRequested;
    }
    if (diskState.failureCount !== undefined) {
      this.state.failureCount = diskState.failureCount;
    }
  }

  recordActivity(): void {
    this.state.lastActivity = Date.now();
    this.state.enforcementTriggeredForThisIdlePeriod = false;
    this.enforcementTriggeredForThisIdlePeriod = false;
    if (this.state.status === 'ENFORCING') {
      this.state.status = 'CONTINUOUS';
      writeState(this.directory, this.state);
    }
  }

  startToolExecution(_toolName: string): void {
    this.toolInProgress = true;
    this.recordActivity();
  }

  endToolExecution(_toolName: string): void {
    this.toolInProgress = false;
    this.recordActivity();
  }

  setPermissionPending(pending: boolean): void {
    this.permissionPending = pending;
    if (!pending) {
      this.recordActivity();
    }
  }

  pause(): string {
    this.state.status = 'PAUSED';
    this.state.lastActivity = Date.now();
    writeState(this.directory, this.state);
    const PAUSE_FLAG_PATH = getPauseFlagPath(this.directory);
    try {
      const flagDir = path.dirname(PAUSE_FLAG_PATH);
      if (!fs.existsSync(flagDir)) {
        fs.mkdirSync(flagDir, { recursive: true });
      }
      fs.writeFileSync(PAUSE_FLAG_PATH, 'PAUSED');
    } catch {
    }
    return 'Boulder paused. Will auto-resume on your next message.';
  }

  resume(): string {
    this.state.status = 'CONTINUOUS';
    this.state.lastActivity = Date.now();
    this.state.iteration += 1;
    writeState(this.directory, this.state);
    const PAUSE_FLAG_PATH = getPauseFlagPath(this.directory);
    try {
      if (fs.existsSync(PAUSE_FLAG_PATH)) {
        fs.unlinkSync(PAUSE_FLAG_PATH);
      }
    } catch {
    }
    return `Boulder resumed at iteration ${this.state.iteration}. The boulder never stops!`;
  }

  isPaused(): boolean {
    return this.state.status === 'PAUSED' || isPaused(this.directory);
  }

  async checkIdle(log: LogFunction): Promise<boolean> {
    if (this.toolInProgress || this.permissionPending) {
      await log?.('debug', `checkIdle: BLOCKED - toolInProgress=${this.toolInProgress}, permissionPending=${this.permissionPending}`);
      return false;
    }
    const idleTime = Date.now() - this.state.lastActivity;
    const isIdle = idleTime >= CONFIG.IDLE_THRESHOLD_MS;
    await log?.('debug', `checkIdle: idleTime=${idleTime}ms, threshold=${CONFIG.IDLE_THRESHOLD_MS}ms, isIdle=${isIdle}`);
    return isIdle;
  }

  async checkCooldown(log: LogFunction): Promise<boolean> {
    const timeSinceEnforcement = Date.now() - this.state.lastEnforcement;
    const cooldownPeriod = CONFIG.COOLDOWN_MS * Math.pow(CONFIG.BACKOFF_MULTIPLIER, this.state.failureCount);
    const remaining = Math.max(0, cooldownPeriod - timeSinceEnforcement);
    const isCooldown = timeSinceEnforcement >= cooldownPeriod;
    await log?.('debug', `checkCooldown: timeSince=${timeSinceEnforcement}ms, period=${cooldownPeriod}ms, remaining=${remaining}ms, isCooldown=${isCooldown}`);
    return isCooldown;
  }

  async checkAbort(log: LogFunction): Promise<boolean> {
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
      writeState(this.directory, this.state);
    }
    return withinWindow;
  }

  async checkRecovery(log: LogFunction): Promise<boolean> {
    const isRecovering = this.state.isRecovering;
    await log?.('debug', `checkRecovery: isRecovering=${isRecovering}`);
    return isRecovering;
  }

  async showCountdown(client: PluginContext['client'], log: LogFunction, _iteration: number): Promise<void> {
    if (!client?.tui) {
      await log?.('debug', 'No tui available for countdown');
      return;
    }
    for (let i = CONFIG.COUNTDOWN_SECONDS; i > 0; i--) {
      await log?.('debug', `Countdown: ${i}...`);
      try {
        await client.tui.showToast({
          body: {
            title: 'BOULDER ENFORCEMENT',
            message: `Boulder enforcement in ${i}...`,
            variant: 'warning',
            duration: 1500
          }
        });
      } catch (error: unknown) {
        const err = error as Error;
        await log?.('error', 'Countdown toast error', { error: err.message });
      }
      await new Promise(resolve => setTimeout(resolve, 1000));
    }
    await log?.('debug', 'Countdown complete - triggering enforcement');
  }

  checkText(text: string | undefined | null): boolean {
    if (!text || typeof text !== 'string') return false;
    const lower = text.toLowerCase();
    
    const hasCompletion = this.config.completionKeywords.some(keyword =>
      lower.includes(keyword.toLowerCase())
    );
    if (!hasCompletion) return false;
    
    const hasWorkIndicators = this.config.workIndicators.some(indicator =>
      lower.includes(indicator.toLowerCase())
    );
    
    return hasCompletion && !hasWorkIndicators;
  }

  async shouldEnforce(log: LogFunction): Promise<boolean> {
    this.reloadState();

    if (isPaused(this.directory)) {
      await log?.('debug', 'GATE FAILED: Pause flag file exists');
      return false;
    }

    await log?.('debug', '=== DECISION GATE CHECKS ===');
    await log?.('debug', `iteration: ${this.state.iteration}`);
    await log?.('debug', `failureCount: ${this.state.failureCount}/${CONFIG.MAX_FAILURES}`);
    await log?.('debug', `stopRequested: ${this.state.stopRequested}`);
    await log?.('debug', `status: ${this.state.status}`);
    await log?.('debug', `enforcementTriggeredForThisIdlePeriod: ${this.enforcementTriggeredForThisIdlePeriod}`);

    if (this.state.status === 'PAUSED') {
      await log?.('debug', 'GATE FAILED: Boulder is paused');
      return false;
    }

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

  async triggerEnforcement(client: PluginContext['client'], log: LogFunction, reason: string = 'idle'): Promise<boolean> {
    const timeSinceLastEnforcement = Date.now() - this.state.lastEnforcement;
    if (timeSinceLastEnforcement < 30000) {
      await log?.('debug', `BLOCKED: Only ${timeSinceLastEnforcement}ms since last enforcement (need 30000ms)`);
      return false;
    }

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
    writeState(this.directory, this.state);

    await log?.('info', `Enforcement triggered - iteration ${this.state.iteration}`);
    return true;
  }

  async recordAbort(log: LogFunction): Promise<void> {
    this.state.abortDetectedAt = Date.now();
    writeState(this.directory, this.state);
    await log?.('debug', 'Abort recorded');
  }

  async setRecovering(recovering: boolean, log: LogFunction): Promise<void> {
    this.state.isRecovering = recovering;
    writeState(this.directory, this.state);
    await log?.('debug', `Recovery state set: ${recovering}`);
  }

  recordFailure(): void {
    this.state.failureCount++;
    writeState(this.directory, this.state);
  }

  clearStopFlag(): void {
    if (this.state.stopRequested) {
      this.state.stopRequested = false;
      writeState(this.directory, this.state);
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
      cooldownRemainingMs: cooldownRemaining,
      toolInProgress: this.toolInProgress,
      permissionPending: this.permissionPending
    };
  }
}

let enforcer: BoulderEnforcer | null = null;
let pollInterval: ReturnType<typeof setInterval> | null = null;
let activityDetectedSinceLastPoll = false;
let enforcementLock = false;

interface EnforcementResult {
  triggered: boolean;
  message: { role: string; content: Array<{ type: string; text: string }>; metadata: { enforcement: boolean; iteration: number; type: string } } | null;
}

async function triggerEnforcementWithCountdown(
  directory: string,
  client: PluginContext['client'],
  enforcerInstance: BoulderEnforcer,
  log: LogFunction,
  reason: string = 'idle',
  sessionID: string | null = null
): Promise<EnforcementResult> {
  if (enforcementLock) {
    await log?.('debug', 'Enforcement already in progress (lock active), skipping');
    return { triggered: false, message: null };
  }
  
  enforcementLock = true;
  
  try {
    await log?.('info', `Polling detected ${reason} - triggering enforcement`);

    await enforcerInstance.showCountdown(client, log, enforcerInstance.state.iteration + 1);

    const triggered = await enforcerInstance.triggerEnforcement(client, log, reason);

    if (triggered) {
      await log?.('info', `Enforcement triggered - iteration ${enforcerInstance.state.iteration} (${reason})`);

      if (client?.tui) {
        await log?.('debug', 'Attempting to show toast');
        try {
          await client.tui.showToast({
            body: {
              title: `BOULDER ENFORCEMENT - Iteration ${enforcerInstance.state.iteration}`,
              message: reason === 'idle'
                ? 'The boulder never stops. You have been idle for 30+ seconds. Generate new ideas. Refine code. Research.'
                : 'The boulder never stops. Completion detected. Continue improving.',
              variant: 'warning',
              duration: 15000
            }
          });
        } catch (error: unknown) {
          const err = error as Error;
          await log?.('error', 'Failed to show toast', { error: err.message });
        }
        await log?.('debug', 'Toast call completed');
      }

      const sessionIDForMessage = sessionID || enforcerInstance.state.sessionID;
      if (client?.session?.promptAsync && sessionIDForMessage) {
        await log?.('debug', `Sending system message to session: ${sessionIDForMessage}`);
        try {
          await client.session.promptAsync({
            path: { id: sessionIDForMessage },
            body: {
              parts: [{
                type: "text",
                text: `## [BOULDER ENFORCEMENT] Iteration ${enforcerInstance.state.iteration}

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
          await log?.('info', 'System message sent successfully');
        } catch (error: unknown) {
          const err = error as Error;
          await log?.('error', 'Failed to send system message', { error: err.message });
        }
      } else {
        await log?.('warn', 'Cannot send system message', { 
          hasPromptAsync: !!client?.session?.promptAsync, 
          sessionID: sessionIDForMessage 
        });
      }

      const enforcementMsg = buildEnforcementMessage(enforcerInstance.state.iteration);
      return { triggered: true, message: enforcementMsg };
    }

    return { triggered: false, message: null };
  } finally {
    enforcementLock = false;
  }
}

function startPolling(directory: string, client: PluginContext['client'], log: LogFunction): void {
  if (pollInterval) {
    clearInterval(pollInterval);
  }

  activityDetectedSinceLastPoll = false;

  pollInterval = setInterval(async () => {
    if (!enforcer || !client) return;

    const freshState = readState(directory);
    if (freshState.status === 'PAUSED') {
      await log?.('debug', 'Fresh state check: Boulder is PAUSED, skipping enforcement');
      return;
    }
    enforcer.state.status = freshState.status;

    if (activityDetectedSinceLastPoll) {
      activityDetectedSinceLastPoll = false;
      await log?.('debug', 'Polling: activity detected, skipping this cycle');
      return;
    }

    const timeSinceEnforcement = Date.now() - enforcer.state.lastEnforcement;
    const cooldownPeriod = CONFIG.COOLDOWN_MS * Math.pow(CONFIG.BACKOFF_MULTIPLIER, enforcer.state.failureCount);
    if (timeSinceEnforcement < cooldownPeriod) {
      await log?.('debug', `Skipping: in cooldown (${timeSinceEnforcement}ms < ${cooldownPeriod}ms), lastEnforcement=${enforcer.state.lastEnforcement}`);
      return;
    }

    if (await enforcer.shouldEnforce(log) && !enforcer.enforcementTriggeredForThisIdlePeriod) {
      await log?.('debug', 'Polling detected idle - triggering enforcement');
      await triggerEnforcementWithCountdown(directory, client, enforcer, log, 'idle', enforcer.state.sessionID);
    }
  }, 5000);

  log?.('info', 'Polling started - checking every 5 seconds');
}

function _stopPolling(log: LogFunction): void {
  if (pollInterval) {
    clearInterval(pollInterval);
    pollInterval = null;
    log?.('info', 'Polling stopped');
  }
}

function markActivityForPoll(): void {
  activityDetectedSinceLastPoll = true;
}

interface InputContext {
  isSubAgent?: boolean;
  agentType?: string;
  parentSession?: string;
  session?: string;
}

function isMainAgent(input?: InputContext): boolean {
  if (!input) return true;
  if (input.isSubAgent === true) return false;
  if (input.agentType === 'sub' || input.agentType === 'child') return false;
  if (input.parentSession && input.parentSession !== input.session) return false;
  return true;
}

function buildEnforcementMessage(iteration: number) {
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

export default async function NexusOpenCodePlugin(context: PluginContext): Promise<{
  tool: Record<string, ToolDefinition>;
  "tool.execute.before": (input: HookInput, output: HookOutput) => Promise<void>;
  "tool.execute.after": (input: HookInput, output: HookOutput) => Promise<void>;
  "message": (input: HookInput, output: HookOutput) => Promise<void>;
  "experimental.chat.system.transform": (input: HookInput, output: OutputMessages) => Promise<void>;
  "event": (input: HookInput, output: HookOutput) => Promise<void>;
  "chat.input": (input: HookInput, output: HookOutput) => Promise<void>;
}> {
  const { directory, client } = context;

  const log: LogFunction = async (level: string, message: string, extra: Record<string, unknown> = {}) => {
    if (client?.app?.log) {
      try {
        await client.app.log({
          body: {
            service: 'nexus-enforcer',
            level,
            message,
            extra
          }
        });
      } catch {}
    }
  };

  await log?.('info', 'Initializing boulder enforcer...');
  await log?.('debug', `Context keys: ${Object.keys(context).join(', ')}`);
  await log?.('debug', `Has client: ${!!client}`);
  await log?.('debug', `Has client.tui: ${!!client?.tui}`);

  enforcer = new BoulderEnforcer(directory);

  await log?.('info', `Boulder initialized - iteration ${enforcer.state.iteration}`);

  setTimeout(() => {
    startPolling(directory, client, log);
  }, 5000);

  return {
    tool: {
      nexus_boulder_pause: tool({
        description: 'Pause the boulder enforcement system',
        args: {},
        async execute(_args, ctx) {
          let state = readState(ctx.directory);
          state.status = 'PAUSED';
          state.stopRequested = true;
          writeState(ctx.directory, state);
          const pauseFlagPath = path.join(ctx.directory, '.nexus/boulder/pause.flag');
          const flagDir = path.dirname(pauseFlagPath);
          if (!fs.existsSync(flagDir)) {
            fs.mkdirSync(flagDir, { recursive: true });
          }
          fs.writeFileSync(pauseFlagPath, 'PAUSED');
          return '✅ Boulder paused. Use /boulder-resume to continue.';
        }
      }),
      
      nexus_boulder_resume: tool({
        description: 'Resume the boulder enforcement system',
        args: {},
        async execute(_args, ctx) {
          let state = readState(ctx.directory);
          state.status = 'CONTINUOUS';
          state.stopRequested = false;
          state.iteration += 1;
          writeState(ctx.directory, state);
          const pauseFlagPath = path.join(ctx.directory, '.nexus/boulder/pause.flag');
          if (fs.existsSync(pauseFlagPath)) {
            fs.unlinkSync(pauseFlagPath);
          }
          return `▶️ Boulder resumed at iteration ${state.iteration}. The boulder never stops!`;
        }
      }),
      
      nexus_boulder_status: tool({
        description: 'Check boulder enforcement status',
        args: {},
        async execute(_args, ctx) {
          let state = readState(ctx.directory);
          return `Boulder Status:
- Status: ${state.status}
- Iteration: ${state.iteration}
- Last Activity: ${new Date(state.lastActivity).toISOString()}`;
        }
      })
    },

    "tool.execute.before": async (input: HookInput, _output: HookOutput) => {
      if (!enforcer) return;
      const toolName = input?.tool || 'unknown';
      enforcer.startToolExecution(toolName);
      enforcer.clearStopFlag();
      markActivityForPoll();
    },

    "tool.execute.after": async (input: HookInput, _output: HookOutput) => {
      if (!enforcer) return;
      const toolName = input?.tool || 'unknown';
      enforcer.endToolExecution(toolName);
      enforcer.recordActivity();
      markActivityForPoll();
    },

    "message": async (_input: HookInput, _output: HookOutput) => {
      if (!enforcer) return;
      enforcer.recordActivity();
      markActivityForPoll();
    },

    "experimental.chat.system.transform": async (input: HookInput, output: OutputMessages) => {
      if (!enforcer || !isMainAgent(input)) return;
      
      enforcer.recordActivity();
      markActivityForPoll();

      const messages = output?.messages;
      if (!messages || !Array.isArray(messages)) return;

      if (enforcer.state.status === 'ENFORCING') {
        const enforcementMsg = buildEnforcementMessage(enforcer.state.iteration);
        messages.push(enforcementMsg as { content: string | MessageContent });
        output.messages = messages;
        await log?.('info', 'Injected enforcement message');
        return;
      }

      const lastMessage = messages[messages.length - 1];
      if (!lastMessage) return;

      const text = typeof lastMessage.content === 'string'
        ? lastMessage.content
        : (lastMessage.content as MessageContent)?.text || '';

      if (enforcer.checkText(text)) {
        await log?.('info', 'Completion keywords detected');
        await log?.('debug', 'About to call triggerEnforcement with countdown');
        const _triggered = await enforcer.triggerEnforcement(client, log, 'completion');
        await log?.('info', `Enforcement triggered - iteration ${enforcer.state.iteration} (completion)`);
        
        const enforcementMsg = buildEnforcementMessage(enforcer.state.iteration);
        messages.push(enforcementMsg as { content: string | MessageContent });
        output.messages = messages;
        
        if (client?.tui) {
          await log?.('debug', 'Showing completion toast');
          try {
            await client.tui.showToast({
              body: {
                title: `BOULDER ENFORCEMENT - Iteration ${enforcer.state.iteration}`,
                message: 'The boulder never stops. Completion detected. Continue improving.',
                variant: 'warning',
                duration: 15000
              }
            });
          } catch (error: unknown) {
            const err = error as Error;
            await log?.('error', 'Failed to show toast', { error: err.message });
          }
        }
      }
    },

    "event": async (input: HookInput, _output: HookOutput) => {
      if (!enforcer || !client) {
        await log?.('debug', 'Missing enforcer or client');
        return;
      }

      const eventType = input?.event?.type;

      await log?.('debug', `Event received: type=${eventType}, keys=${Object.keys(input || {}).join(',')}`);
      await log?.('debug', `Event properties: ${JSON.stringify(input?.event?.properties || {})}`);

      markActivityForPoll();
      if (enforcer) {
        enforcer.recordActivity();
      }

      if (eventType === 'agent.abort' || eventType === 'agent.stop') {
        await log?.('info', 'Abort/stop event detected - recording abort');
        await enforcer.recordAbort(log);
        return;
      }

      if (eventType === 'session.recovering' || eventType === 'session.recover') {
        await log?.('info', 'Session recovery event detected');
        await enforcer.setRecovering(true, log);
        return;
      }

      if (eventType === 'session.recovered' || eventType === 'session.recovery.complete') {
        await log?.('info', 'Session recovery complete');
        await enforcer.setRecovering(false, log);
        return;
      }

      if (eventType !== 'session.idle') {
        await log?.('debug', `Not session.idle event (${eventType}), skipping`);
        return;
      }

      await log?.('info', 'Session idle event received - processing');

      if (!isMainAgent(input)) {
        await log?.('debug', 'Not main agent, skipping');
        return;
      }

      const sessionID = input?.event?.properties?.sessionID 
        || input?.session 
        || input?.event?.session 
        || context?.session;

      if (sessionID) {
        enforcer.state.sessionID = sessionID;
        writeState(directory, enforcer.state);
        await log?.('debug', `Updated sessionID to: ${sessionID}`);
      }

      await log?.('debug', `Session ID extracted: ${sessionID || 'undefined'}`);

      if (enforcementLock) {
        await log?.('debug', 'Enforcement lock active (another enforcement in progress), skipping event-based enforcement');
        return;
      }

      if (isPaused(directory)) {
        await log?.('info', 'Boulder is PAUSED (flag file) - skipping enforcement');
        return;
      }

      const currentState = readState(directory);
      if (currentState.status === 'PAUSED') {
        await log?.('info', 'Boulder is PAUSED - skipping enforcement');
        return;
      }

      if (!await enforcer.shouldEnforce(log)) {
        await log?.('debug', 'Enforcement gates not passed');
        return;
      }

      await log?.('info', 'All gates passed - triggering enforcement');
      
      enforcementLock = true;
      
      try {
        const triggered = await enforcer.triggerEnforcement(client, log, 'idle');

      if (triggered) {
        await log?.('info', `Enforcement triggered - iteration ${enforcer.state.iteration} (idle)`);

        if (client?.tui) {
          await log?.('debug', 'Attempting to show toast');
          try {
            await client.tui.showToast({
              body: {
                title: `BOULDER ENFORCEMENT - Iteration ${enforcer.state.iteration}`,
                message: 'The boulder never stops. You have been idle for 30+ seconds. Generate new ideas. Refine code. Research.',
                variant: 'error',
                duration: 15000
              }
            });
          } catch (error: unknown) {
            const err = error as Error;
            await log?.('error', 'Failed to show toast', { error: err.message });
          }
          await log?.('debug', 'Toast call completed');
        }

        const sessionIDForMessage = sessionID || enforcer.state.sessionID;

        if (!sessionIDForMessage) {
          await log?.('warn', 'Cannot send system message: no sessionID available');
        } else if (client?.session?.promptAsync) {
          await log?.('debug', 'About to send system message', { 
            hasSessionID: !!sessionIDForMessage, 
            hasPromptAsync: !!client?.session?.promptAsync 
          });

          try {
            await client.session.promptAsync({
              path: { id: sessionIDForMessage },
              body: {
                agent: 'nexus-enforcer',
                model: {
                  providerID: 'kimi-for-coding',
                  modelID: 'k2p5'
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
            await log?.('info', 'System message sent successfully');
          } catch (error: unknown) {
            const err = error as Error;
            await log?.('error', 'Failed to send system message', { 
              error: err.message,
              stack: err.stack 
            });
          }
          await log?.('debug', 'System message sent');
        } else {
          await log?.('debug', `Cannot send message: hasPrompt=${!!client?.session?.promptAsync}, hasSessionID=${!!sessionID}`);
        }
      } else {
        await log?.('debug', `Not triggered: triggered=${triggered}`);
      }
      } finally {
        enforcementLock = false;
      }
    },

    "chat.input": async (input: HookInput, output: HookOutput) => {
      if (!enforcer) return;
      
      const text = input?.text || '';
      
      await log?.('debug', `chat.input received: text="${text?.substring(0, 50)}...", input keys=${Object.keys(input || {}).join(',')}`);
      await log?.('debug', `chat.input input.source=${input?.source}, input.role=${input?.role}, input.actor=${input?.actor}`);
      
      if (text.startsWith('/boulder-pause')) {
        const message = enforcer.pause();
        const responseText = `✅ **Boulder Paused**\n\n${message}\n\nYou can continue when ready. The boulder will auto-resume when you send your next message.`;
        if (output.messages) {
          output.messages.push({
            content: responseText
          });
        }
        await log?.('info', 'Boulder paused via command');
        return;
      }
      
      if (text.startsWith('/boulder-resume')) {
        const message = enforcer.resume();
        const responseText = `▶️ **Boulder Resumed**\n\n${message}`;
        if (output.messages) {
          output.messages.push({
            content: responseText
          });
        }
        await log?.('info', 'Boulder resumed via command');
        return;
      }
      
      if (text.startsWith('/boulder-status')) {
        const state = enforcer.state;
        let statusText = "";
        switch (state.status) {
          case 'ENFORCING':
            statusText = "🟢 Active - Enforcing boulder methodology";
            break;
          case 'PAUSED':
            statusText = "⏸️ Paused - Use /boulder-resume to continue";
            break;
          default:
            statusText = "⚪ Continuous Mode";
        }
        const responseText = `**Boulder Status**\n\n- Status: ${statusText}\n- Iteration: ${state.iteration}\n- Failures: ${state.failureCount}`;
        if (output.messages) {
          output.messages.push({
            content: responseText
          });
        }
        await log?.('info', 'Boulder status via command');
        return;
      }
      
      const isAgentMessage = text.includes('[BOULDER ENFORCEMENT]') || 
                            text.includes('The boulder never stops') ||
                            text.includes('✅ **Boulder Paused**') ||
                            text.includes('▶️ **Boulder Resumed**');
      
      if (isAgentMessage) {
        await log?.('debug', 'chat.input: Detected agent message, NOT auto-resuming');
      } else if (enforcer.isPaused()) {
        const message = enforcer.resume();
        await log?.('info', `Boulder auto-resumed on user message: ${message}`);
        
        const PAUSE_FLAG_PATH = getPauseFlagPath(directory);
        try {
          if (fs.existsSync(PAUSE_FLAG_PATH)) {
            fs.unlinkSync(PAUSE_FLAG_PATH);
            await log?.('debug', 'Pause flag file removed');
          }
        } catch (e: unknown) {
          const err = e as Error;
          await log?.('error', 'Failed to remove pause flag file', { error: err.message });
        }
        
        if (client?.tui) {
          try {
            await client.tui.showToast({
              body: {
                title: 'Boulder Auto-Resumed',
                message: `The boulder never stops. Now at iteration ${enforcer.state.iteration}.`,
                variant: 'info',
                duration: 5000
              }
            });
          } catch {}
        }
      }
      
      enforcer.recordActivity();
      enforcer.clearStopFlag();
    }
  }
};
