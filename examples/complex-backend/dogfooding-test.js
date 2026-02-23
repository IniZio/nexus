#!/usr/bin/env node

/**
 * DEPRECATED: This dogfooding test is disabled
 * 
 * This test depended on the workspace-sdk package which has been deleted.
 * The workspace functionality is now provided by nexusd with a different architecture.
 * 
 * To test complex backend with the new system:
 * 1. Create a workspace: nexus workspace create backend-test
 * 2. SSH into it: nexus workspace ssh backend-test
 * 3. Run the backend development workflow manually
 * 
 * For automated testing, rewrite this script to use the nexus CLI:
 *   nexus workspace exec backend-test -- npm install
 *   nexus workspace exec backend-test -- npm run dev
 */

console.log('='.repeat(70));
console.log('DEPRECATED: Complex Backend Dogfooding Test');
console.log('='.repeat(70));
console.log();
console.log('This test has been disabled because it depends on the deleted');
console.log('workspace-sdk package.');
console.log();
console.log('The workspace functionality is now provided by nexusd.');
console.log();
console.log('To test complex backend:');
console.log('  1. nexus workspace create backend-test');
console.log('  2. nexus workspace ssh backend-test');
console.log('  3. cd /workspace && npm install && npm run dev');
console.log();
console.log('For automated testing, rewrite this script to use:');
console.log('  nexus workspace exec <name> -- <command>');
console.log();
console.log('='.repeat(70));

process.exit(0);

/*
// ORIGINAL TEST CODE (preserved for reference):
// This used the deleted workspace-sdk package

const { WorkspaceClient } = require('../../packages/workspace-sdk/dist/client');

async function runTests() {
  // ... original test implementation ...
}

runTests().then(result => {
  // ... original test completion ...
});
*/
