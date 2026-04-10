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
