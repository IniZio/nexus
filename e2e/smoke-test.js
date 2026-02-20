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

    console.log('Reading package.json...');
    const packageJson = await client.fs.readFile('package.json');
    const pkg = JSON.parse(packageJson);
    console.log(`✓ Read package.json - Express version: ${pkg.dependencies?.express || 'not found'}\n`);

    console.log('Listing src/ directory...');
    const files = await client.fs.readdir('src');
    const fileNames = files.map(f => typeof f === 'string' ? f : f.name);
    console.log(`✓ Listed src/ directory - Files: ${fileNames.slice(0, 5).join(', ')}${fileNames.length > 5 ? '...' : ''}\n`);

    console.log('Disconnecting...');
    await client.disconnect();
    console.log('✓ Disconnected\n');

    console.log('═══════════════════════════════════════');
    console.log('           SUCCESS - All tests passed!');
    console.log('═══════════════════════════════════════');
    process.exit(0);
  } catch (error) {
    console.error('✗ Error:', error.message);
    try {
      await client.disconnect();
    } catch (e) {}
    console.log('\n═══════════════════════════════════════');
    console.log('           FAILED');
    console.log('═══════════════════════════════════════');
    process.exit(1);
  }
}

runSmokeTest();
