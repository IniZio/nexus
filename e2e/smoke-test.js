#!/usr/bin/env node

/**
 * DEPRECATED: Smoke test disabled
 * 
 * This test depended on the workspace-sdk package which has been deleted.
 * The workspace functionality is now provided by nexusd with a different architecture.
 * 
 * To test workspace connectivity:
 *   nexus workspace list
 *   nexus workspace status <name>
 */

console.log('═══════════════════════════════════════');
console.log('SMOKE TEST DISABLED');
console.log('═══════════════════════════════════════');
console.log();
console.log('This smoke test has been disabled because it depends on the');
console.log('deleted workspace-sdk package.');
console.log();
console.log('To verify workspace functionality:');
console.log('  nexus workspace list');
console.log('  nexus workspace create test');
console.log('  nexus workspace status test');
console.log();
console.log('═══════════════════════════════════════');

process.exit(0);

/*
// ORIGINAL TEST CODE (preserved for reference):

const { WorkspaceClient } = require('../packages/workspace-sdk/dist/index');

async function runSmokeTest() {
  const client = new WorkspaceClient({
    endpoint: 'ws://localhost:8080',
    workspaceId: 'test-workspace',
    token: 'test-token'
  });

  try {
    console.log('Connecting to workspace daemon...');
    await client.connect();
    console.log('✓ Connected successfully\n');
    // ... rest of test ...
  } catch (error) {
    console.error('✗ Error:', error.message);
    process.exit(1);
  }
}

runSmokeTest();
*/
