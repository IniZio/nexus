const { WorkspaceClient } = require('./dist/client');

async function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

async function runTests() {
  console.log('='.repeat(60));
  console.log('NEXUS WORKSPACE SDK DOGFOODING TEST');
  console.log('='.repeat(60));
  console.log();

  const client = new WorkspaceClient({
    endpoint: 'ws://localhost:8080',
    workspaceId: 'test-workspace',
    token: 'test-token',
    reconnect: false,
  });

  const results = [];
  let startTime;

  // Test 1: Connection
  console.log('TEST 1: WebSocket Connection');
  console.log('-'.repeat(40));
  startTime = Date.now();
  try {
    await client.connect();
    const latency = Date.now() - startTime;
    console.log(`✓ Connected successfully (latency: ${latency}ms)`);
    results.push({ test: 'Connection', status: 'PASS', latency: `${latency}ms` });
  } catch (error) {
    console.log(`✗ Connection failed: ${error.message}`);
    results.push({ test: 'Connection', status: 'FAIL', error: error.message });
    process.exit(1);
  }

  // Test 2: Write File
  console.log('\nTEST 2: Write File');
  console.log('-'.repeat(40));
  const testContent = `// Test file created by SDK dogfooding test
// Timestamp: ${new Date().toISOString()}
const greeting = "Hello from Nexus Workspace SDK!";
console.log(greeting);
module.exports = { greeting };
`;
  startTime = Date.now();
  try {
    await client.fs.writeFile('test-file.js', testContent);
    const latency = Date.now() - startTime;
    console.log(`✓ Wrote test-file.js (${testContent.length} bytes, latency: ${latency}ms)`);
    results.push({ test: 'Write File', status: 'PASS', latency: `${latency}ms`, details: `${testContent.length} bytes` });
  } catch (error) {
    console.log(`✗ Write failed: ${error.message}`);
    results.push({ test: 'Write File', status: 'FAIL', error: error.message });
  }

  // Test 3: Read File Back
  console.log('\nTEST 3: Read File');
  console.log('-'.repeat(40));
  startTime = Date.now();
  try {
    const content = await client.fs.readFile('test-file.js', 'utf8');
    const latency = Date.now() - startTime;
    const matches = content === testContent;
    if (matches) {
      console.log(`✓ Read file successfully (${content.length} bytes, latency: ${latency}ms)`);
      console.log(`✓ Content verification: MATCH`);
      results.push({ test: 'Read File', status: 'PASS', latency: `${latency}ms`, details: `${content.length} bytes, content verified` });
    } else {
      console.log(`✗ Content mismatch!`);
      results.push({ test: 'Read File', status: 'FAIL', error: 'Content mismatch' });
    }
  } catch (error) {
    console.log(`✗ Read failed: ${error.message}`);
    results.push({ test: 'Read File', status: 'FAIL', error: error.message });
  }

  // Test 4: List Directory
  console.log('\nTEST 4: List Directory');
  console.log('-'.repeat(40));
  startTime = Date.now();
  try {
    const entries = await client.fs.readdir('.');
    const latency = Date.now() - startTime;
    console.log(`✓ Directory listing successful (${entries.length} entries, latency: ${latency}ms)`);
    console.log('  Contents:', entries.map(e => typeof e === 'string' ? e : e.name).join(', '));
    results.push({ test: 'List Directory', status: 'PASS', latency: `${latency}ms`, details: `${entries.length} entries` });
  } catch (error) {
    console.log(`✗ Readdir failed: ${error.message}`);
    results.push({ test: 'List Directory', status: 'FAIL', error: error.message });
  }

  // Test 5: Execute pwd command
  console.log('\nTEST 5: Execute pwd Command');
  console.log('-'.repeat(40));
  startTime = Date.now();
  try {
    const result = await client.exec.exec('pwd', [], { timeout: 5000 });
    const latency = Date.now() - startTime;
    if (result.exitCode === 0) {
      console.log(`✓ Command executed successfully (exit code: ${result.exitCode}, latency: ${latency}ms)`);
      console.log(`  stdout: ${result.stdout.trim()}`);
      if (result.stderr) {
        console.log(`  stderr: ${result.stderr}`);
      }
      results.push({ test: 'Execute pwd', status: 'PASS', latency: `${latency}ms`, details: `exitCode: ${result.exitCode}` });
    } else {
      console.log(`✗ Command failed with exit code: ${result.exitCode}`);
      console.log(`  stderr: ${result.stderr}`);
      results.push({ test: 'Execute pwd', status: 'FAIL', error: `exit code: ${result.exitCode}` });
    }
  } catch (error) {
    console.log(`✗ Exec failed: ${error.message}`);
    results.push({ test: 'Execute pwd', status: 'FAIL', error: error.message });
  }

  // Test 6: Execute ls command
  console.log('\nTEST 6: Execute ls -la Command');
  console.log('-'.repeat(40));
  startTime = Date.now();
  try {
    const result = await client.exec.exec('ls', ['-la', '.'], { timeout: 5000 });
    const latency = Date.now() - startTime;
    if (result.exitCode === 0) {
      console.log(`✓ Command executed successfully (exit code: ${result.exitCode}, latency: ${latency}ms)`);
      console.log('  Output:');
      result.stdout.trim().split('\n').forEach(line => console.log(`    ${line}`));
      results.push({ test: 'Execute ls', status: 'PASS', latency: `${latency}ms`, details: `exitCode: ${result.exitCode}` });
    } else {
      console.log(`✗ Command failed with exit code: ${result.exitCode}`);
      results.push({ test: 'Execute ls', status: 'FAIL', error: `exit code: ${result.exitCode}` });
    }
  } catch (error) {
    console.log(`✗ Exec failed: ${error.message}`);
    results.push({ test: 'Execute ls', status: 'FAIL', error: error.message });
  }

  // Test 7: File Exists Check
  console.log('\nTEST 7: File Exists Check');
  console.log('-'.repeat(40));
  startTime = Date.now();
  try {
    const exists = await client.fs.exists('test-file.js');
    const latency = Date.now() - startTime;
    if (exists) {
      console.log(`✓ File exists check: TRUE (latency: ${latency}ms)`);
      results.push({ test: 'File Exists', status: 'PASS', latency: `${latency}ms`, details: 'test-file.js exists' });
    } else {
      console.log(`✗ File exists check returned false`);
      results.push({ test: 'File Exists', status: 'FAIL', error: 'File not found' });
    }
  } catch (error) {
    console.log(`✗ Exists check failed: ${error.message}`);
    results.push({ test: 'File Exists', status: 'FAIL', error: error.message });
  }

  // Cleanup
  console.log('\n' + '='.repeat(60));
  console.log('CLEANUP');
  console.log('-'.repeat(40));
  try {
    await client.disconnect();
    console.log('✓ Disconnected from workspace');
  } catch (error) {
    console.log(`✗ Disconnect error: ${error.message}`);
  }

  // Summary
  console.log('\n' + '='.repeat(60));
  console.log('TEST SUMMARY');
  console.log('='.repeat(60));

  const passed = results.filter(r => r.status === 'PASS').length;
  const failed = results.filter(r => r.status === 'FAIL').length;

  console.log(`\nTotal Tests: ${results.length}`);
  console.log(`✓ Passed: ${passed}`);
  console.log(`✗ Failed: ${failed}`);
  console.log();

  if (failed === 0) {
    console.log('🎉 ALL TESTS PASSED!');
  } else {
    console.log('⚠️  SOME TESTS FAILED - see details above');
  }

  console.log('\nDetailed Results:');
  console.log('-'.repeat(60));
  results.forEach(r => {
    const icon = r.status === 'PASS' ? '✓' : '✗';
    console.log(`${icon} ${r.test}: ${r.status}${r.latency ? ` (${r.latency})` : ''}${r.error ? ` - ${r.error}` : ''}`);
  });

  console.log('\n' + '='.repeat(60));
  console.log('DOGFOODING TEST COMPLETE');
  console.log('='.repeat(60));

  return failed === 0;
}

runTests().then(success => {
  process.exit(success ? 0 : 1);
}).catch(error => {
  console.error('Test execution failed:', error);
  process.exit(1);
});
