const path = require('path');

console.log('=== Testing Nexus Enforcer Plugin ===\n');

let testResults = {
  passed: 0,
  failed: 0,
  errors: []
};

function test(name, fn) {
  try {
    fn();
    console.log(`✓ ${name}`);
    testResults.passed++;
  } catch (e) {
    console.log(`✗ ${name}: ${e.message}`);
    testResults.failed++;
    testResults.errors.push({ name, error: e.message });
  }
}

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}

console.log('1. Testing require() of plugin...');
let plugin;
try {
  const pluginPath = path.resolve(__dirname, '.opencode/plugins/nexus-enforcer.js');
  plugin = require(pluginPath);
  console.log('   Plugin loaded successfully');
  testResults.passed++;
} catch (e) {
  console.log(`   ✗ Failed to load plugin: ${e.message}`);
  testResults.failed++;
  testResults.errors.push({ name: 'require plugin', error: e.message });
  console.log('\n=== TEST RESULTS ===');
  console.log(`Passed: ${testResults.passed}`);
  console.log(`Failed: ${testResults.failed}`);
  process.exit(1);
}

console.log('\n2. Testing exports...');
test('exports name property', () => {
  assert(typeof plugin.name === 'string', 'name should be a string');
  assert(plugin.name === 'nexus-enforcer', 'name should be "nexus-enforcer"');
});

test('exports version property', () => {
  assert(typeof plugin.version === 'string', 'version should be a string');
  assert(plugin.version === '1.0.0', 'version should be "1.0.0"');
});

test('exports initialize function', () => {
  assert(typeof plugin.initialize === 'function', 'initialize should be a function');
});

test('exports getPlugin function', () => {
  assert(typeof plugin.getPlugin === 'function', 'getPlugin should be a function');
});

test('exports getEnforcer function', () => {
  assert(typeof plugin.getEnforcer === 'function', 'getEnforcer should be a function');
});

test('exports createEnforcer function', () => {
  assert(typeof plugin.createEnforcer === 'function', 'createEnforcer should be a function');
});

test('exports getHooks function', () => {
  assert(typeof plugin.getHooks === 'function', 'getHooks should be a function');
});

test('exports NexusEnforcerPlugin object', () => {
  assert(typeof plugin.NexusEnforcerPlugin === 'object', 'NexusEnforcerPlugin should be an object');
  assert(plugin.NexusEnforcerPlugin !== null, 'NexusEnforcerPlugin should not be null');
});

test('exports DualLayerBoulderEnforcer class', () => {
  assert(typeof plugin.DualLayerBoulderEnforcer === 'function', 'DualLayerBoulderEnforcer should be a function');
});

console.log('\n3. Testing initialization with mock context...');
let mockContext = {
  sessionId: 'test-session-123',
  userId: 'test-user',
  workspace: '/home/newman/magic/nexus'
};

test('initialize() returns success object', () => {
  let result = plugin.initialize({ enabled: true });
  assert(typeof result === 'object', 'initialize should return an object');
  assert(result.success === true, 'result.success should be true');
  assert(typeof result.message === 'string', 'result.message should be a string');
});

test('getPlugin() returns plugin object', () => {
  let p = plugin.getPlugin();
  assert(typeof p === 'object', 'getPlugin should return an object');
  assert(p.name === 'nexus-enforcer', 'plugin name should match');
});

test('getEnforcer() returns enforcer instance', () => {
  let enforcer = plugin.getEnforcer();
  assert(enforcer !== null, 'getEnforcer should return non-null after init');
  assert(typeof enforcer.recordToolCall === 'function', 'enforcer should have recordToolCall');
  assert(typeof enforcer.checkText === 'function', 'enforcer should have checkText');
  assert(typeof enforcer.getStatus === 'function', 'enforcer should have getStatus');
});

test('createEnforcer() creates new instance', () => {
  let newEnforcer = plugin.createEnforcer({ enabled: true });
  assert(newEnforcer !== null, 'createEnforcer should return an enforcer');
  assert(typeof newEnforcer.recordToolCall === 'function', 'new enforcer should have recordToolCall');
});

test('getHooks() returns hooks object', () => {
  let hooks = plugin.getHooks();
  assert(typeof hooks === 'object', 'getHooks should return an object');
  assert(typeof hooks['tool.execute.before'] === 'function', 'should have tool.execute.before hook');
  assert(typeof hooks['response.before'] === 'function', 'should have response.before hook');
});

test('checkCompletionAttempt() works', () => {
  let result = plugin.checkCompletionAttempt('I am done with the task');
  assert(typeof result === 'boolean', 'checkCompletionAttempt should return boolean');
});

test('getStatus() returns status object', () => {
  let status = plugin.getStatus();
  assert(typeof status === 'object', 'getStatus should return object');
  assert(typeof status.active === 'boolean', 'status should have active boolean');
  assert(typeof status.enabled === 'boolean', 'status should have enabled boolean');
});

test('forceContinuation() works without error', () => {
  plugin.forceContinuation();
  let status = plugin.getStatus();
  assert(status.iteration >= 0, 'iteration should be non-negative');
});

test('recordToolCall() works', () => {
  plugin.recordToolCall('bash');
  let status = plugin.getStatus();
  assert(status.timeSinceActivityMs >= 0, 'timeSinceActivity should be non-negative');
});

test('destroy() works without error', () => {
  plugin.destroy();
  let status = plugin.getStatus();
  assert(status.active === false, 'status.active should be false after destroy');
});

console.log('\n4. Testing error handling...');
test('initialize with disabled config returns success: false', () => {
  let result = plugin.initialize({ enabled: false });
  assert(result.success === false, 'should return success: false when disabled');
  assert(result.reason === 'disabled', 'should return reason: disabled');
});

test('checkCompletionAttempt returns false when not initialized', () => {
  let result = plugin.checkCompletionAttempt('test text');
  assert(result === false, 'should return false when not initialized');
});

test('getStatus returns inactive state when not initialized', () => {
  let status = plugin.getStatus();
  assert(status.active === false, 'should be inactive');
  assert(status.enabled === false, 'should be disabled');
});

console.log('\n=== TEST RESULTS ===');
console.log(`Passed: ${testResults.passed}`);
console.log(`Failed: ${testResults.failed}`);

if (testResults.failed > 0) {
  console.log('\nFailed tests:');
  testResults.errors.forEach((e, i) => {
    console.log(`  ${i + 1}. ${e.name}: ${e.error}`);
  });
  process.exit(1);
} else {
  console.log('\nAll tests passed!');
  process.exit(0);
}
