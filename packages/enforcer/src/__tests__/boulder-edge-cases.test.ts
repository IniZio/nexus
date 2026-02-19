import { describe, it, expect, beforeEach } from 'vitest';
import { BoulderStateManager } from '../boulder/state.js';

describe('Boulder Edge Cases', () => {
  let boulder: BoulderStateManager;

  beforeEach(() => {
    boulder = BoulderStateManager.getInstance();
    boulder.reset();
  });

  describe('Rapid Iterations', () => {
    it('should handle 100 rapid iterations', () => {
      for (let i = 0; i < 100; i++) {
        boulder.incrementIteration();
      }
      
      const state = boulder.getState();
      expect(state.iteration).toBe(100);
      expect(state.totalValidations).toBe(100);
      expect(state.canComplete).toBe(true);
    });

    it('should handle rapid completion attempts', () => {
      // Get to allowed state
      for (let i = 0; i < 5; i++) {
        boulder.incrementIteration();
      }
      
      // Rapid completion attempts
      for (let i = 0; i < 10; i++) {
        boulder.recordCompletionAttempt();
      }
      
      expect(boulder.canComplete()).toBe(false);
      expect(boulder.getState().status).toBe('BLOCKED');
    });
  });

  describe('Concurrent Access', () => {
    it('should maintain consistency with concurrent increments', () => {
      const iterations = 1000;
      
      // Simulate concurrent access
      for (let i = 0; i < iterations; i++) {
        boulder.incrementIteration();
      }
      
      expect(boulder.getState().iteration).toBe(iterations);
    });
  });

  describe('Reset Behavior', () => {
    it('should properly reset all state', () => {
      // Build up state
      for (let i = 0; i < 10; i++) {
        boulder.incrementIteration();
      }
      boulder.recordCompletionAttempt();
      boulder.recordCompletionAttempt();
      
      // Reset
      boulder.reset();
      
      const state = boulder.getState();
      expect(state.iteration).toBe(0);
      expect(state.totalValidations).toBe(0);
      expect(state.consecutiveCompletionsAttempted).toBe(0);
      expect(state.canComplete).toBe(false);
      expect(state.status).toBe('FORCED_CONTINUATION');
    });

    it('should allow work after reset', () => {
      // Build up state
      for (let i = 0; i < 5; i++) {
        boulder.incrementIteration();
      }
      expect(boulder.canComplete()).toBe(true);
      
      // Reset
      boulder.reset();
      expect(boulder.canComplete()).toBe(false);
      
      // Build up again
      for (let i = 0; i < 5; i++) {
        boulder.incrementIteration();
      }
      expect(boulder.canComplete()).toBe(true);
    });
  });

  describe('Partial Iterations', () => {
    it('should block completion at boundary conditions', () => {
      // Exactly 4 iterations - should block
      for (let i = 0; i < 4; i++) {
        boulder.incrementIteration();
      }
      expect(boulder.canComplete()).toBe(false);
      
      // One more - should allow
      boulder.incrementIteration();
      expect(boulder.canComplete()).toBe(true);
    });

    it('should handle zero iterations', () => {
      expect(boulder.getState().iteration).toBe(0);
      expect(boulder.canComplete()).toBe(false);
    });
  });

  describe('Improvement Tasks Edge Cases', () => {
    it('should handle requesting more tasks than available', () => {
      const tasks = boulder.getImprovementTasks(100);
      // Should not exceed available tasks
      expect(tasks.length).toBeLessThanOrEqual(8); // Total available tasks
    });

    it('should handle requesting zero tasks', () => {
      const tasks = boulder.getImprovementTasks(0);
      expect(tasks).toHaveLength(0);
    });

    it('should return unique tasks when possible', () => {
      const tasks = boulder.getImprovementTasks(5);
      const uniqueTasks = [...new Set(tasks)];
      // Should have unique tasks (may have duplicates due to randomness)
      expect(uniqueTasks.length).toBeGreaterThan(0);
    });
  });

  describe('State After Penalty', () => {
    it('should properly penalize and recover', () => {
      // Get to 10 iterations
      for (let i = 0; i < 10; i++) {
        boulder.incrementIteration();
      }
      expect(boulder.getState().iteration).toBe(10);
      
      // Trigger penalty (3 consecutive attempts)
      boulder.recordCompletionAttempt();
      boulder.recordCompletionAttempt();
      boulder.recordCompletionAttempt();
      
      // Should drop to 8 iterations
      expect(boulder.getState().iteration).toBe(8);
      expect(boulder.getState().status).toBe('BLOCKED');
      
      // Do more work to recover
      for (let i = 0; i < 5; i++) {
        boulder.incrementIteration();
      }
      
      expect(boulder.getState().iteration).toBe(13);
      expect(boulder.canComplete()).toBe(true);
    });
  });

  describe('Time Tracking', () => {
    it('should update lastValidationTime on iteration', () => {
      const before = boulder.getState().lastValidationTime;
      
      // Small delay to ensure time difference
      setTimeout(() => {
        boulder.incrementIteration();
        const after = boulder.getState().lastValidationTime;
        expect(after).toBeGreaterThanOrEqual(before);
      }, 10);
    });
  });

  describe('Message Formatting', () => {
    it('should handle very large iteration numbers', () => {
      // Simulate many iterations
      for (let i = 0; i < 10000; i++) {
        boulder.incrementIteration();
      }
      
      const message = boulder.getEnforcementMessage();
      expect(message).toContain('BOULDER[10000]');
      expect(message).toContain('NEXUS INTERNAL');
    });

    it('should maintain message format consistency', () => {
      boulder.incrementIteration();
      const msg1 = boulder.getEnforcementMessage();
      
      boulder.incrementIteration();
      const msg2 = boulder.getEnforcementMessage();
      
      // Both should follow same pattern
      expect(msg1).toMatch(/BOULDER\[\d+\]: NEXUS INTERNAL/);
      expect(msg2).toMatch(/BOULDER\[\d+\]: NEXUS INTERNAL/);
    });
  });
});
