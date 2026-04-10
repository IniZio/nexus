export function assertCapabilitiesArray(capabilities: unknown): void {
  if (!Array.isArray(capabilities)) {
    throw new Error('Expected capabilities to be an array');
  }
}

export function skipTest(reason: string): true {
  // eslint-disable-next-line no-console
  console.warn(`[e2e skipped] ${reason}`);
  return true;
}

export function isLinuxTapUnsupported(error: unknown): boolean {
  const message = String((error as { message?: unknown })?.message ?? error ?? '');
  return message.includes('TAP devices are only supported on Linux');
}
