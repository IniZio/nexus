import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { ValidationEngine, createValidationEngine } from '../engine/checker.js';
import { PromptGenerator, createPromptGenerator } from '../prompts/generator.js';
import { ExecutionContext, EnforcerConfig } from '../types.js';
import * as fs from 'fs';
import * as path from 'path';
import { fileURLToPath } from 'url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

describe('Boulder Continuous Enforcement', () => {
  let engine: ValidationEngine;
  let generator: PromptGenerator;
  let tempDir: string;

  beforeEach(() => {
    // Create a temporary directory for tests
    tempDir = fs.mkdtempSync(path.join(__dirname, 'test-workspace-'));
    
    // Create .nexus directory structure
    const nexusPath = path.join(tempDir, '.nexus');
    fs.mkdirSync(nexusPath, { recursive: true });
    
    // Create enforcer-rules.json
    const config: EnforcerConfig = {
      version: '1.0.0',
      rules: {
        workspace: { required: true, enforceWorkspace: true, enforceIsolation: true },
        dogfooding: { required: true, logFriction: true, testChanges: true },
        completion: { required: true, verifyTests: true, verifyBuild: true, verifyLint: true, verifyTypecheck: true },
        quality: [],
      },
      adaptive: true,
      agentSpecificPrompts: true,
      strictMode: false,
    };
    fs.writeFileSync(path.join(nexusPath, 'enforcer-rules.json'), JSON.stringify(config, null, 2));
    
    engine = createValidationEngine(path.join(nexusPath, 'enforcer-rules.json'));
    generator = createPromptGenerator();
  });

  afterEach(() => {
    // Clean up temp directory
    fs.rmSync(tempDir, { recursive: true, force: true });
  });

  describe('Iteration Counting', () => {
    it('should track iteration count across validations', () => {
      const context: ExecutionContext = {
        workspacePath: tempDir,
        workingDirectory: tempDir,
        agentType: 'test',
        taskDescription: 'Test task',
        timestamp: new Date(),
        environment: {},
      };

      // First validation
      const result1 = engine.validate(context);
      expect(result1.iteration).toBe(1);

      // Second validation
      const result2 = engine.validate(context);
      expect(result2.iteration).toBe(2);

      // Third validation
      const result3 = engine.validate(context);
      expect(result3.iteration).toBe(3);
    });

    it('should never allow completion on iteration < 5', () => {
      const context: ExecutionContext = {
        workspacePath: tempDir,
        workingDirectory: tempDir,
        agentType: 'test',
        taskDescription: 'Test task',
        timestamp: new Date(),
        environment: {},
      };

      for (let i = 1; i <= 4; i++) {
        const result = engine.validate(context);
        expect(result.canComplete).toBe(false);
        expect(result.iteration).toBe(i);
        expect(result.boulderStatus).toBe('FORCED_CONTINUATION');
      }
    });

    it('should only allow completion after minimum iterations', () => {
      const context: ExecutionContext = {
        workspacePath: tempDir,
        workingDirectory: tempDir,
        agentType: 'test',
        taskDescription: 'Test task',
        timestamp: new Date(),
        environment: {},
      };

      // Run 5 validations
      let result: ReturnType<typeof engine.validate>;
      for (let i = 1; i <= 5; i++) {
        result = engine.validate(context);
      }

      // After 5 iterations, completion should be possible if no errors
      expect(result!.canComplete).toBe(true);
      expect(result!.boulderStatus).toBe('ALLOWED');
    });
  });

  describe('Boulder Enforcement Patterns', () => {
    it('should inject continuation prompts when completion attempted early', () => {
      const context: ExecutionContext = {
        workspacePath: tempDir,
        workingDirectory: tempDir,
        agentType: 'test',
        taskDescription: 'Test task',
        timestamp: new Date(),
        environment: {},
      };

      const result = engine.validate(context);
      const prompt = generator.generatePrompt('after', context, { result });

      expect(prompt).toContain('BOULDER ENFORCEMENT');
      expect(prompt).toContain('NOT ALLOWED');
      expect(prompt).toContain('The boulder NEVER stops');
    });

    it('should suggest continuous improvement tasks', () => {
      const context: ExecutionContext = {
        workspacePath: tempDir,
        workingDirectory: tempDir,
        agentType: 'test',
        taskDescription: 'Test task',
        timestamp: new Date(),
        environment: {},
      };

      const result = engine.validate(context);
      expect(result.improvementTasks).toBeDefined();
      expect(result.improvementTasks!.length).toBeGreaterThan(0);
      expect(result.improvementTasks).toContain('Write additional test cases');
      expect(result.improvementTasks).toContain('Refactor code for better performance');
      expect(result.improvementTasks).toContain('Research best practices for current implementation');
    });
  });

  describe('Edge Cases', () => {
    it('should handle missing workspace gracefully', () => {
      const invalidContext: ExecutionContext = {
        workspacePath: '/nonexistent/path',
        workingDirectory: '/nonexistent/path',
        agentType: 'test',
        taskDescription: 'Test task',
        timestamp: new Date(),
        environment: {},
      };

      const result = engine.validate(invalidContext);
      expect(result.passed).toBe(false);
      expect(result.boulderStatus).toBe('FORCED_CONTINUATION');
    });

    it('should handle rapid successive validations', () => {
      const context: ExecutionContext = {
        workspacePath: tempDir,
        workingDirectory: tempDir,
        agentType: 'test',
        taskDescription: 'Test task',
        timestamp: new Date(),
        environment: {},
      };

      // Rapid validations
      const results: ReturnType<typeof engine.validate>[] = [];
      for (let i = 0; i < 10; i++) {
        results.push(engine.validate(context));
      }

      expect(results.every(r => r.iteration !== undefined)).toBe(true);
      expect(results[results.length - 1].iteration).toBe(10);
    });

    it('should maintain enforcement state across multiple files', () => {
      const context1: ExecutionContext = {
        workspacePath: tempDir,
        workingDirectory: tempDir,
        currentFile: 'file1.ts',
        agentType: 'test',
        taskDescription: 'Test task 1',
        timestamp: new Date(),
        environment: {},
      };

      const context2: ExecutionContext = {
        workspacePath: tempDir,
        workingDirectory: tempDir,
        currentFile: 'file2.ts',
        agentType: 'test',
        taskDescription: 'Test task 2',
        timestamp: new Date(),
        environment: {},
      };

      engine.validate(context1);
      const result2 = engine.validate(context2);

      expect(result2.iteration).toBe(2);
    });
  });

  describe('Performance', () => {
    it('should complete validation within 100ms', () => {
      const context: ExecutionContext = {
        workspacePath: tempDir,
        workingDirectory: tempDir,
        agentType: 'test',
        taskDescription: 'Test task',
        timestamp: new Date(),
        environment: {},
      };

      const startTime = Date.now();
      engine.validate(context);
      const endTime = Date.now();

      expect(endTime - startTime).toBeLessThan(100);
    });

    it('should handle 1000 validations without memory issues', () => {
      const context: ExecutionContext = {
        workspacePath: tempDir,
        workingDirectory: tempDir,
        agentType: 'test',
        taskDescription: 'Test task',
        timestamp: new Date(),
        environment: {},
      };

      const startTime = Date.now();
      for (let i = 0; i < 1000; i++) {
        engine.validate(context);
      }
      const endTime = Date.now();

      // Should complete in under 10 seconds
      expect(endTime - startTime).toBeLessThan(10000);
    });
  });
});
