const { WorkspaceClient } = require('../../packages/workspace-sdk/dist/client');

async function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

async function runTests() {
  console.log('='.repeat(70));
  console.log('NEXUS WORKSPACE SDK - REACT HOT RELOAD DOGFOODING TEST');
  console.log('React development workflow with Create React App');
  console.log('='.repeat(70));
  console.log();

  const client = new WorkspaceClient({
    endpoint: 'ws://localhost:8080',
    workspaceId: 'react-hot-reload',
    token: 'test-token',
    reconnect: false,
  });

  const results = [];
  let startTime;

  async function runTest(name, testFn) {
    console.log(`\nTEST: ${name}`);
    console.log('-'.repeat(70));
    startTime = Date.now();
    try {
      const result = await testFn();
      const latency = Date.now() - startTime;
      console.log(`✓ PASSED (${latency}ms)`);
      if (result && result.details) {
        console.log(`  Details: ${result.details}`);
      }
      results.push({ test: name, status: 'PASS', latency: `${latency}ms`, details: result?.details || '' });
      return result;
    } catch (error) {
      const latency = Date.now() - startTime;
      console.log(`✗ FAILED (${latency}ms): ${error.message}`);
      results.push({ test: name, status: 'FAIL', latency: `${latency}ms`, error: error.message });
      return null;
    }
  }

  // Test 1: WebSocket Connection
  await runTest('WebSocket Connection', async () => {
    await client.connect();
    return { details: 'Connected to ws://localhost:8080' };
  });

  // Test 2: Read package.json (verify React dependencies)
  await runTest('Read package.json', async () => {
    const content = await client.fs.readFile('package.json', 'utf8');
    const pkg = JSON.parse(content);
    if (!pkg.dependencies || !pkg.dependencies.react) {
      throw new Error('Missing React dependency');
    }
    if (!pkg.dependencies['react-dom']) {
      throw new Error('Missing react-dom dependency');
    }
    if (!pkg.dependencies['react-scripts']) {
      throw new Error('Missing react-scripts dependency');
    }
    return { details: `Found React ${pkg.dependencies.react}, react-dom ${pkg.dependencies['react-dom']}, react-scripts ${pkg.dependencies['react-scripts']}` };
  });

  // Test 3: List src/ directory (check for React components)
  await runTest('List src/ Directory', async () => {
    const entries = await client.fs.readdir('src');
    const names = entries.map(e => typeof e === 'string' ? e : e.name);
    const hasAppJs = names.includes('App.js');
    const hasIndexJs = names.includes('index.js');
    if (!hasAppJs) throw new Error('Missing App.js');
    if (!hasIndexJs) throw new Error('Missing index.js');
    return { details: `Found ${names.length} files: ${names.join(', ')}` };
  });

  // Test 4: Read a React component file
  await runTest('Read React Component', async () => {
    const content = await client.fs.readFile('src/App.js', 'utf8');
    if (!content.includes('React')) {
      throw new Error('App.js does not contain React');
    }
    if (!content.includes('import')) {
      throw new Error('App.js does not contain imports');
    }
    return { details: 'App.js is a valid React component' };
  });

  // Test 5: Check build scripts exist
  await runTest('Check Build Scripts', async () => {
    const content = await client.fs.readFile('package.json', 'utf8');
    const pkg = JSON.parse(content);
    if (!pkg.scripts || !pkg.scripts.start) {
      throw new Error('Missing start script');
    }
    if (!pkg.scripts.build) {
      throw new Error('Missing build script');
    }
    if (!pkg.scripts.test) {
      throw new Error('Missing test script');
    }
    return { details: `Scripts: start="${pkg.scripts.start}", build="${pkg.scripts.build}", test="${pkg.scripts.test}"` };
  });

  // Test 6: Execute pwd Command
  await runTest('Execute pwd Command', async () => {
    const result = await client.exec.exec('pwd', [], { timeout: 5000 });
    if (result.exitCode !== 0) {
      throw new Error(`Command failed with exit code: ${result.exitCode}`);
    }
    return { details: `Working directory: ${result.stdout.trim()}` };
  });

  // Test 7: Execute ls -la Command
  await runTest('Execute ls -la Command', async () => {
    const result = await client.exec.exec('ls', ['-la', '.'], { timeout: 5000 });
    if (result.exitCode !== 0) {
      throw new Error(`Command failed with exit code: ${result.exitCode}`);
    }
    const lines = result.stdout.trim().split('\n').length;
    return { details: `Listed ${lines} lines of output` };
  });

  // Test 8: Check Node.js Version
  await runTest('Check Node.js Version', async () => {
    const result = await client.exec.exec('node', ['--version'], { timeout: 5000 });
    if (result.exitCode !== 0) {
      throw new Error(`Command failed with exit code: ${result.exitCode}`);
    }
    const version = result.stdout.trim();
    return { details: `Node.js version: ${version}` };
  });

  // Test 9: Check npm Version
  await runTest('Check npm Version', async () => {
    const result = await client.exec.exec('npm', ['--version'], { timeout: 5000 });
    if (result.exitCode !== 0) {
      throw new Error(`Command failed with exit code: ${result.exitCode}`);
    }
    const version = result.stdout.trim();
    return { details: `npm version: ${version}` };
  });

  // Test 10: Run npm install (may take longer)
  console.log('\n' + '='.repeat(70));
  console.log('NOTE: npm install may take 1-2 minutes depending on network...');
  console.log('='.repeat(70));
  
  await runTest('Run npm install', async () => {
    const result = await client.exec.exec('npm', ['install'], { timeout: 180000 });
    if (result.exitCode !== 0) {
      throw new Error(`npm install failed with exit code: ${result.exitCode}\nstderr: ${result.stderr}`);
    }
    return { details: 'Dependencies installed successfully' };
  });

  // Test 11: Verify node_modules has react
  await runTest('Verify node_modules has React', async () => {
    const entries = await client.fs.readdir('node_modules');
    const names = entries.map(e => typeof e === 'string' ? e : e.name);
    const hasReact = names.includes('react');
    const hasReactDom = names.includes('react-dom');
    if (!hasReact) throw new Error('react not in node_modules');
    if (!hasReactDom) throw new Error('react-dom not in node_modules');
    return { details: `Found ${entries.length} packages including react and react-dom` };
  });

  // Test 12: Write a test file
  await runTest('Write Test File', async () => {
    const testContent = `// Test file created by dogfooding test
// Date: ${new Date().toISOString()}

export const testConfig = {
  test: true,
  framework: 'react',
  timestamp: Date.now()
};
`;
    await client.fs.writeFile('src/test-config.js', testContent);
    return { details: 'Written test-config.js' };
  });

  // Test 13: Read it back
  await runTest('Read Test File Back', async () => {
    const content = await client.fs.readFile('src/test-config.js', 'utf8');
    if (!content.includes('test: true')) {
      throw new Error('Test file content verification failed');
    }
    if (!content.includes('framework')) {
      throw new Error('Missing framework in test file');
    }
    return { details: 'Test file verified correctly' };
  });

  // Test 14: File Stat Operation
  await runTest('File Stat Operation', async () => {
    const stat = await client.fs.stat('package.json');
    if (!stat || typeof stat.size !== 'number') {
      throw new Error('Stat operation did not return expected structure');
    }
    return { details: `package.json size: ${stat.size} bytes` };
  });

  // Test 15: Directory Exists Check
  await runTest('Directory Exists Check', async () => {
    const srcExists = await client.fs.exists('src');
    const publicExists = await client.fs.exists('public');
    if (!srcExists) throw new Error('src directory does not exist');
    if (!publicExists) throw new Error('public directory does not exist');
    return { details: 'Both src/ and public/ directories exist' };
  });

  // Test 16: Cleanup Test File
  await runTest('Cleanup Test File', async () => {
    await client.fs.rm('src/test-config.js');
    const exists = await client.fs.exists('src/test-config.js');
    if (exists) throw new Error('File still exists after deletion');
    return { details: 'test-config.js removed successfully' };
  });

  // Cleanup
  console.log('\n' + '='.repeat(70));
  console.log('CLEANUP');
  console.log('-'.repeat(70));
  try {
    await client.disconnect();
    console.log('✓ Disconnected from workspace');
  } catch (error) {
    console.log(`✗ Disconnect error: ${error.message}`);
  }

  // Summary
  console.log('\n' + '='.repeat(70));
  console.log('TEST SUMMARY');
  console.log('='.repeat(70));

  const passed = results.filter(r => r.status === 'PASS').length;
  const failed = results.filter(r => r.status === 'FAIL').length;

  console.log(`\nTotal Tests: ${results.length}`);
  console.log(`✓ Passed: ${passed}`);
  console.log(`✗ Failed: ${failed}`);
  console.log();

  const latencies = results
    .filter(r => r.latency)
    .map(r => parseInt(r.latency.replace('ms', '')));
  const avgLatency = latencies.length > 0 
    ? Math.round(latencies.reduce((a, b) => a + b, 0) / latencies.length)
    : 0;

  console.log(`Average Latency: ${avgLatency}ms`);
  console.log();

  if (failed === 0) {
    console.log('🎉 ALL TESTS PASSED!');
  } else {
    console.log(`⚠️  ${failed} TEST(S) FAILED - see details above`);
  }

  console.log('\nDetailed Results:');
  console.log('-'.repeat(70));
  results.forEach((r, i) => {
    const icon = r.status === 'PASS' ? '✓' : '✗';
    console.log(`${icon} ${String(i + 1).padStart(2)}. ${r.test} - ${r.status} (${r.latency})`);
    if (r.error) {
      console.log(`    Error: ${r.error}`);
    }
  });

  console.log('\n' + '='.repeat(70));
  console.log('REACT HOT RELOAD DOGFOODING TEST COMPLETE');
  console.log('='.repeat(70));

  return { success: failed === 0, results, passed, failed, avgLatency };
}

runTests().then(result => {
  const fs = require('fs');
  const report = {
    date: new Date().toISOString(),
    ...result
  };
  fs.writeFileSync('/tmp/react-hot-reload-dogfooding-results.json', JSON.stringify(report, null, 2));
  process.exit(result.success ? 0 : 1);
}).catch(error => {
  console.error('Test execution failed:', error);
  process.exit(1);
});
