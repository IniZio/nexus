/**
 * Passive Boulder Enforcer
 * 
 * Only triggers on EXPLICIT completion attempts.
 * Does NOT interfere with normal work.
 */

export class PassiveBoulderEnforcer {
  private iteration: number = 0;
  private lastToolCall: number = Date.now();
  
  /**
   * Record tool call - completely passive
   * NEVER triggers enforcement
   */
  recordToolCall(_toolName: string): void {
    this.lastToolCall = Date.now();
    // Passive - just record, never enforce
  }

  /**
   * Check text for explicit completion
   * Only returns true for CLEAR completion statements
   */
  checkForCompletion(text: string): boolean {
    const lower = text.toLowerCase().trim();
    
    // EXPLICIT completion patterns only
    const explicitCompletions = [
      /^i am done$/,
      /^i'm done$/,
      /^we are done$/,
      /^we're done$/,
      /^task complete$/,
      /^work complete$/,
      /^implementation complete$/,
      /^that is all$/,
      /^that's all$/,
      /^finished$/,
      /^completed$/,
      /^done$/,
    ];
    
    // Must match exactly (standalone statements)
    const isExplicitCompletion = explicitCompletions.some(pattern => 
      pattern.test(lower)
    );
    
    // FALSE POSITIVE prevention
    // If text contains work-related words, it's NOT completion
    const workWords = [
      'tool', 'call', 'read', 'write', 'edit', 'bash', 'grep',
      'implement', 'create', 'add', 'fix', 'update',
      'let me', 'i will', 'i\'ll', 'working on',
      'file', 'directory', 'path', 'code',
    ];
    
    const containsWorkWords = workWords.some(word => 
      lower.includes(word)
    );
    
    return isExplicitCompletion && !containsWorkWords;
  }

  /**
   * Increment iteration - only called on actual completion attempt
   */
  incrementIteration(): void {
    this.iteration++;
  }

  /**
   * Get enforcement message
   */
  getEnforcementMessage(): string {
    return (
      `BOULDER ENFORCEMENT: Completion attempt detected.\n\n` +
      `The boulder NEVER stops. Continue improving:\n` +
      `1. Add more test coverage\n` +
      `2. Optimize performance\n` +
      `3. Handle edge cases\n\n` +
      `Iteration: ${this.iteration}\n` +
      `Status: CONTINUOUS\n\n` +
      `DO NOT STOP.`
    );
  }

  getIteration(): number {
    return this.iteration;
  }
}

export function createPassiveEnforcer(): PassiveBoulderEnforcer {
  return new PassiveBoulderEnforcer();
}
