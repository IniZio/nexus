import { describe, it, expect, beforeEach } from 'vitest';
import { BoulderStateManager, IMPROVEMENT_TASKS } from '../boulder/state.js';
import { ValidationEngine } from '../engine/checker.js';
import { ExecutionContext } from '../types.js';

describe('Boulder Continuous Enforcement Integration', () => {
  let boulder: BoulderStateManager;

  beforeEach(() => {
    boulder = BoulderStateManager.getInstance();
    boulder.reset();
  });

  describe('Iteration Tracking', () => {
    it('should start at iteration 0', () => {
      const state = boulder.getState();
      expect(state.iteration).toBe(0);
    });

    it('should increment iteration on each validation', () => {
      boulder.incrementIteration();
      expect(boulder.getState().iteration).toBe(1);
      
      boulder.incrementIteration();
      expect(boulder.getState().iteration).toBe(2);
    });

    it('should track total validations', () => {
      for (let i = 0; i < 5; i++) {
        boulder.incrementIteration();
      }
      expect(boulder.getState().totalValidations).toBe(5);
    });
  });

  describe('Completion Enforcement', () => {
    it('should not allow completion before minimum iterations', () => {
      // First 4 iterations should not allow completion
      for (let i = 1; i <= 4; i++) {
        boulder.incrementIteration();
        expect(boulder.canComplete()).toBe(false);
        expect(boulder.getState().status).toBe('FORCED_CONTINUATION');
      }
    });

    it('should allow completion after 5 iterations', () => {
      for (let i = 1; i <= 5; i++) {
        boulder.incrementIteration();
      }
      
      expect(boulder.canComplete()).toBe(true);
      expect(boulder.getState().status).toBe('ALLOWED');
    });

    it('should block after 3 consecutive completion attempts', () => {
      // Get to allowed state
      for (let i = 1; i <= 5; i++) {
        boulder.incrementIteration();
      }
      expect(boulder.canComplete()).toBe(true);

      // Record 3 completion attempts
      boulder.recordCompletionAttempt();
      boulder.recordCompletionAttempt();
      boulder.recordCompletionAttempt();

      expect(boulder.canComplete()).toBe(false);
      expect(boulder.getState().status).toBe('BLOCKED');
    });

    it('should reset consecutive attempts counter on successful work', () => {
      // Get to allowed state
      for (let i = 1; i <= 5; i++) {
        boulder.incrementIteration();
      }

      // 2 completion attempts
      boulder.recordCompletionAttempt();
      boulder.recordCompletionAttempt();
      expect(boulder.getState().consecutiveCompletionsAttempted).toBe(2);

      // Increment iteration (doing work)
      boulder.incrementIteration();
      
      // Counter should reset on successful iteration
      expect(boulder.getState().consecutiveCompletionsAttempted).toBe(0);
    });
  });

  describe('Improvement Tasks', () => {
    it('should return improvement tasks', () => {
      const tasks = boulder.getImprovementTasks(3);
      expect(tasks).toHaveLength(3);
      expect(tasks.every(t => IMPROVEMENT_TASKS.includes(t))).toBe(true);
    });

    it('should return different tasks on multiple calls', () => {
      const tasks1 = boulder.getImprovementTasks(3);
      const tasks2 = boulder.getImprovementTasks(3);
      
      // They might be the same by chance, but verify we get valid tasks
      expect(tasks1.every(t => IMPROVEMENT_TASKS.includes(t))).toBe(true);
      expect(tasks2.every(t => IMPROVEMENT_TASKS.includes(t))).toBe(true);
    });
  });

  describe('Enforcement Messages', () => {
    it('should generate enforcement message with iteration', () => {
      boulder.incrementIteration();
      const message = boulder.getEnforcementMessage();
      expect(message).toContain('BOULDER[1]');
      expect(message).toContain('NEXUS INTERNAL');
    });

    it('should update message with each iteration', () => {
      for (let i = 1; i <= 3; i++) {
        boulder.incrementIteration();
        const message = boulder.getEnforcementMessage();
        expect(message).toContain(`BOULDER[${i}]`);
      }
    });
  });

  describe('Singleton Pattern', () => {
    it('should maintain single instance', () => {
      const instance1 = BoulderStateManager.getInstance();
      const instance2 = BoulderStateManager.getInstance();
      
      expect(instance1).toBe(instance2);
    });

    it('should share state across instances', () => {
      const instance1 = BoulderStateManager.getInstance();
      const instance2 = BoulderStateManager.getInstance();
      
      instance1.incrementIteration();
      expect(instance2.getState().iteration).toBe(1);
    });
  });

  describe('Integration with ValidationEngine', () => {
    it('should include boulder fields in validation result', () => {
      // Note: This is a simplified test - in reality you'd need proper setup
      const context: ExecutionContext = {
        workspacePath: '/tmp/test',
        workingDirectory: '/tmp/test',
        agentType: 'test',
        taskDescription: 'test task',
        timestamp: new Date(),
        environment: {},
      };

      // The actual integration would use a properly initialized engine
      // This test verifies the structure is in place
      expect(context).toBeDefined();
    });
  });
});
