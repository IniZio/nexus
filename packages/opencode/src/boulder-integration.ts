import { BoulderContinuousEnforcement, getGlobalEnforcement } from '@nexus/core/boulder';

let globalEnforcement: BoulderContinuousEnforcement | null = null;

export function initializeBoulderEnforcement(): BoulderContinuousEnforcement {
  if (!globalEnforcement) {
    globalEnforcement = getGlobalEnforcement();
  }
  return globalEnforcement;
}

export function onToolCall(toolName: string): void {
  const enforcement = globalEnforcement || getGlobalEnforcement();
  enforcement.recordToolCall(toolName);
}

export function onResponse(text: string): boolean {
  const enforcement = globalEnforcement || getGlobalEnforcement();
  const result = enforcement.recordTextOutput(text);
  if (result.enforce && result.message) {
    console.log(result.message);
  }
  return result.enforce;
}

export function getEnforcementMessage(): string | null {
  const enforcement = globalEnforcement || getGlobalEnforcement();
  const status = enforcement.getStatus();
  if (status.isWorking) {
    return `Iteration: ${status.iteration} | Tasks in queue: ${status.queueStats.total}`;
  }
  return null;
}

export function checkCompletionAttempt(): boolean {
  return false;
}

export function forceContinuation(): void {
}

export function getBoulderStatus(): {
  active: boolean;
  iteration: number;
  message: string | null;
} {
  const enforcement = globalEnforcement || getGlobalEnforcement();
  const status = enforcement.getStatus();
  return {
    active: status.isWorking,
    iteration: status.iteration,
    message: enforcement.getStatusMessage(),
  };
}

export function resetBoulder(): void {
  globalEnforcement = null;
}
