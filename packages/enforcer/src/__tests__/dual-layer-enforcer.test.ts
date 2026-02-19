import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { DualLayerBoulderEnforcer, createDualLayerEnforcer } from '../boulder/dual-layer-enforcer.js';

describe('DualLayerBoulderEnforcer', () => {
  let enforcer: DualLayerBoulderEnforcer;
  let enforcementMessages: string[] = [];

  beforeEach(() => {
    enforcementMessages = [];
    enforcer = createDualLayerEnforcer(
      {
        idleThresholdMs: 500,
        checkIntervalMs: 100,
      },
      (msg) => enforcementMessages.push(msg)
    );
  });

  afterEach(() => {
    enforcer.stop();
  });

  describe('Layer 1: Explicit Completion Detection', () => {
    it('should detect explicit "done"', () => {
      const result = enforcer.checkText('I am done');
      expect(result).toBe(true);
    });

    it('should detect "task complete"', () => {
      const result = enforcer.checkText('Task complete');
      expect(result).toBe(true);
    });

    it('should NOT detect completion with work indicators', () => {
      const result = enforcer.checkText('Let me complete the function');
      expect(result).toBe(false);
    });

    it('should NOT detect completion with tool keywords', () => {
      const result = enforcer.checkText('I will use the read tool when done');
      expect(result).toBe(false);
    });

    it('should NOT detect completion during implementation', () => {
      const result = enforcer.checkText('Implementing the feature, almost done');
      expect(result).toBe(false);
    });
  });

  describe('Layer 2: Interval Idle Checking', () => {
    it('should trigger enforcement after idle threshold', async () => {
      // Wait for idle threshold + check interval
      await new Promise(resolve => setTimeout(resolve, 700));

      expect(enforcementMessages.length).toBeGreaterThan(0);
      expect(enforcementMessages[0]).toContain('BOULDER ENFORCEMENT');
    });

    it('should NOT trigger while tool calls are active', async () => {
      // Simulate active work
      const interval = setInterval(() => {
        enforcer.recordToolCall('read');
      }, 50);

      // Wait longer than idle threshold
      await new Promise(resolve => setTimeout(resolve, 1000));

      clearInterval(interval);

      // Should have no enforcement messages
      expect(enforcementMessages.length).toBe(0);
    });

    it('should track activity timestamps', () => {
      const before = enforcer.getStatus().lastActivity;
      
      enforcer.recordToolCall('write');
      
      const after = enforcer.getStatus().lastActivity;
      expect(after).toBeGreaterThanOrEqual(before);
    });
  });

  describe('Iteration Tracking', () => {
    it('should increment iteration on enforcement', () => {
      expect(enforcer.getStatus().iteration).toBe(0);
      
      enforcer.triggerEnforcement();
      
      expect(enforcer.getStatus().iteration).toBe(1);
    });

    it('should track multiple enforcements', () => {
      enforcer.triggerEnforcement();
      enforcer.triggerEnforcement();
      enforcer.triggerEnforcement();
      
      expect(enforcer.getStatus().iteration).toBe(3);
    });
  });

  describe('Enforcement Message', () => {
    it('should include iteration count', () => {
      enforcer.triggerEnforcement();
      
      expect(enforcementMessages[0]).toContain('Iteration: 1');
    });

    it('should include improvement tasks', () => {
      enforcer.triggerEnforcement();
      
      expect(enforcementMessages[0]).toContain('1.');
      expect(enforcementMessages[0]).toContain('2.');
      expect(enforcementMessages[0]).toContain('3.');
    });

    it('should include boulder branding', () => {
      enforcer.triggerEnforcement();
      
      expect(enforcementMessages[0]).toContain('BOULDER ENFORCEMENT');
      expect(enforcementMessages[0]).toContain('The boulder NEVER stops');
    });
  });

  describe('Status Tracking', () => {
    it('should report interval status', () => {
      const status = enforcer.getStatus();
      
      expect(status.intervalActive).toBe(true);
      expect(status.isEnforcing).toBe(false);
    });

    it('should report time since activity', () => {
      const status = enforcer.getStatus();
      
      expect(status.timeSinceActivity).toBeGreaterThanOrEqual(0);
    });
  });

  describe('Lifecycle', () => {
    it('should stop interval when stop() called', () => {
      enforcer.stop();
      
      expect(enforcer.getStatus().intervalActive).toBe(false);
    });

    it('should not enforce after stopped', async () => {
      enforcer.stop();
      
      await new Promise(resolve => setTimeout(resolve, 1000));
      
      expect(enforcementMessages.length).toBe(0);
    });
  });

  describe('False Positive Prevention', () => {
    it('should NOT trigger on tool call recording', () => {
      enforcer.recordToolCall('read');
      enforcer.recordToolCall('write');
      
      expect(enforcementMessages.length).toBe(0);
    });

    it('should NOT trigger on work-in-progress text', () => {
      const texts = [
        'Let me read the file',
        'I will implement this',
        'Working on the solution',
        'Using the bash tool to check',
      ];

      texts.forEach(text => {
        expect(enforcer.checkText(text)).toBe(false);
      });
    });
  });
});
