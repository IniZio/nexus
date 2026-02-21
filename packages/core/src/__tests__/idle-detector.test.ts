import { describe, it, expect, beforeEach } from 'vitest';
import { BoulderIdleDetector } from '../boulder/idle-detector.js';

describe('BoulderIdleDetector - False Positive Prevention', () => {
  let detector: BoulderIdleDetector;

  beforeEach(() => {
    detector = new BoulderIdleDetector({ idleThresholdMs: 1000 });
  });

  describe('Tool Call Detection', () => {
    it('should NOT trigger enforcement on tool calls', () => {
      detector.recordToolCall('read');
      
      const result = detector.checkIdle();
      expect(result.isIdle).toBe(false);
      expect(result.reason).toBe('active_tool_usage');
    });

    it('should NOT trigger enforcement on multiple tool calls', () => {
      detector.recordToolCall('read');
      detector.recordToolCall('bash');
      detector.recordToolCall('write');
      
      const result = detector.checkIdle();
      expect(result.isIdle).toBe(false);
    });

    it('should NOT trigger enforcement on text output with tool keywords', () => {
      const text = 'I will use the read tool to check the file';
      detector.recordTextOutput(text);
      
      const result = detector.checkIdle();
      expect(result.isIdle).toBe(false);
    });
  });

  describe('Completion Detection', () => {
    it('should trigger enforcement on "I am done"', () => {
      const text = 'I am done with this task';
      const isCompletion = detector.recordTextOutput(text);
      
      expect(isCompletion).toBe(true);
    });

    it('should trigger enforcement on "task complete"', () => {
      const text = 'The implementation is now task complete';
      const isCompletion = detector.recordTextOutput(text);
      
      expect(isCompletion).toBe(true);
    });

    it('should trigger enforcement on "all done"', () => {
      const text = 'We are all done here';
      const isCompletion = detector.recordTextOutput(text);
      
      expect(isCompletion).toBe(true);
    });

    it('should NOT trigger enforcement on "let me complete that" (false positive)', () => {
      const text = 'Let me complete that function for you';
      const isCompletion = detector.recordTextOutput(text);
      
      expect(isCompletion).toBe(false);
    });
  });

  describe('Activity Tracking', () => {
    it('should track recent tool calls', () => {
      detector.recordToolCall('read');
      detector.recordToolCall('edit');
      
      const status = detector.getStatus();
      expect(status.recentToolCalls).toBeGreaterThanOrEqual(2);
      expect(status.isWorking).toBe(true);
    });

    it('should detect idle after threshold', async () => {
      detector.recordToolCall('read');
      
      // Wait for idle threshold
      await new Promise(resolve => setTimeout(resolve, 1100));
      
      const result = detector.checkIdle();
      expect(result.isIdle).toBe(true);
      expect(result.reason).toBe('no_activity_timeout');
    });
  });

  describe('Mixed Activity', () => {
    it('should handle mixed tool calls and text', () => {
      detector.recordToolCall('read');
      detector.recordTextOutput('Let me analyze this');
      detector.recordToolCall('grep');
      detector.recordTextOutput('Here is what I found');
      
      const status = detector.getStatus();
      expect(status.isWorking).toBe(true);
      expect(status.recentToolCalls).toBeGreaterThanOrEqual(2);
    });
  });
});
