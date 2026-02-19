#!/usr/bin/env node
/**
 * Boulder E2E Simulation Test
 * Simulates OpenCode agent behavior to test boulder enforcement
 */

const path = require('path');
const fs = require('fs');

const pluginPath = path.join(__dirname, '..', '.opencode/plugins/nexus-enforcer.js');
console.log('Loading plugin from:', pluginPath);

try {
  const plugin = require(pluginPath);
  console.log('✓ Plugin loaded');
  console.log('  Exports:', Object.keys(plugin).join(', '));

  const mockContext = {
    config: { boulder: { enabled: true, idleThresholdMs: 3000, checkIntervalMs: 1000 }},
    on: (event) => console.log(`  Handler: ${event}`)
  };

  console.log('\nInitializing...');
  const result = plugin.initialize(mockContext);
  console.log('✓ Initialized:', result.success);

  console.log('\n--- E2E TEST ---\n');
  
  // Test 1: Tool calls
  console.log('1. Tool calls (should NOT trigger)...');
  plugin.recordToolCall('read');
  plugin.recordToolCall('write');
  console.log('   Status:', plugin.getStatus());

  // Test 2: Completion attempt
  console.log('\n2. Completion attempt (SHOULD trigger)...');
  const detected = plugin.checkCompletionAttempt('I am done');
  console.log('   Detected:', detected);
  
  if (detected) {
    const enforcement = plugin.forceContinuation();
    console.log('   ✓ ENFORCEMENT! Iteration:', enforcement.iteration);
  }

  // Test 3: False positive
  console.log('\n3. False positive test...');
  const fp = plugin.checkCompletionAttempt('Let me complete the function');
  console.log('   Detected:', fp, '(should be false)');

  // Test 4: Idle
  console.log('\n4. Waiting for idle timeout (4s)...');
  setTimeout(() => {
    const status = plugin.getStatus();
    console.log('   Status:', status);
    console.log('\n✓ E2E TEST COMPLETE - Iteration:', status.iteration);
    plugin.destroy();
    process.exit(0);
  }, 4000);

} catch (err) {
  console.error('✗ Error:', err.message);
  process.exit(1);
}
