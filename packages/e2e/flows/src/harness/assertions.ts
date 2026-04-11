import type { Capability } from '@nexus/sdk';

export function assertCapabilitiesArray(capabilities: unknown): void {
  if (!Array.isArray(capabilities)) {
    throw new Error('Expected capabilities to be an array');
  }
}

export function e2eStrictRuntime(): boolean {
  if (process.env.NEXUS_E2E_STRICT_RUNTIME === '0') {
    return false;
  }
  if (process.env.NEXUS_E2E_STRICT_RUNTIME === '1') {
    return true;
  }
  return process.env.CI === 'true';
}

export function skipTest(reason: string): true {
  console.warn(`[e2e skipped] ${reason}`);
  return true;
}

export function isLinuxTapUnsupported(error: unknown): boolean {
  const message = String((error as { message?: unknown })?.message ?? error ?? '');
  return message.includes('TAP devices are only supported on Linux');
}

export function isRuntimeUnavailable(error: unknown): boolean {
  const message = String((error as { message?: unknown })?.message ?? error ?? '');
  return (
    isLinuxTapUnsupported(error) ||
    message.includes('runtime preflight failed') ||
    message.includes('seatbelt runtime requires limactl') ||
    message.includes('backend selection failed') ||
    message.includes('no required backend available') ||
    message.includes('lima start failed') ||
    message.includes('runtime create failed')
  );
}

export function assertCapabilityOrSkip(capabilities: Capability[], name: string, reason: string): boolean {
  const found = capabilities.find((cap) => cap.name === name);
  if (found?.available) {
    return true;
  }
  if (e2eStrictRuntime()) {
    throw new Error(`E2E strict: ${reason} (missing capability ${name})`);
  }
  skipTest(reason);
  return false;
}

export function onWorkspaceCreateRuntimeError(error: unknown, skipReason: string): boolean {
  if (e2eStrictRuntime()) {
    throw error instanceof Error ? error : new Error(String(error));
  }
  if (isRuntimeUnavailable(error)) {
    skipTest(skipReason);
    return true;
  }
  return false;
}

export function onDaemonStartError(error: unknown, skipReason: string): void {
  if (e2eStrictRuntime()) {
    throw error instanceof Error ? error : new Error(String(error));
  }
  const detail = error instanceof Error ? error.message : String(error);
  skipTest(`${skipReason}: ${detail}`);
}
