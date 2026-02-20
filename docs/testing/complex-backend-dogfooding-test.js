const { WorkspaceClient } = require('../../packages/workspace-sdk/dist/client');

async function sleep(ms) {
  return new Promise(resolve => setTimeout(resolve, ms));
}

async function runTests() {
  console.log('='.repeat(70));
  console.log('NEXUS WORKSPACE SDK - COMPLEX BACKEND DOGFOODING TEST');
  console.log('Full-stack application with PostgreSQL database');
  console.log('='.repeat(70));
  console.log();

  const client = new WorkspaceClient({
    endpoint: 'ws://localhost:8080',
    workspaceId: 'complex-backend',
    token: 'test-token',
    reconnect: false,
  });

  const results = [];
  let startTime;

  // Helper function to run a test
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

  // Test 2: List Project Structure
  await runTest('List Project Structure', async () => {
    const entries = await client.fs.readdir('.');
    const structure = entries.map(e => typeof e === 'string' ? e : e.name);
    const hasPackageJson = structure.includes('package.json');
    const hasSrcDir = structure.includes('src');
    if (!hasPackageJson) throw new Error('Missing package.json');
    if (!hasSrcDir) throw new Error('Missing src directory');
    return { details: `Found ${structure.length} entries including package.json and src/` };
  });

  // Test 3: Read package.json
  await runTest('Read package.json', async () => {
    const content = await client.fs.readFile('package.json', 'utf8');
    const pkg = JSON.parse(content);
    if (!pkg.dependencies || !pkg.dependencies.express) {
      throw new Error('Missing Express dependency');
    }
    if (!pkg.dependencies.pg) {
      throw new Error('Missing PostgreSQL dependency');
    }
    return { details: `Found Express ${pkg.dependencies.express} and pg ${pkg.dependencies.pg}` };
  });

  // Test 4: Read Source Files
  await runTest('Read Source Files', async () => {
    const indexContent = await client.fs.readFile('src/index.js', 'utf8');
    if (!indexContent.includes('express')) {
      throw new Error('index.js does not import express');
    }
    const routesExist = await client.fs.readdir('src/routes');
    const routeCount = routesExist.length;
    return { details: `index.js valid, ${routeCount} route files found` };
  });

  // Test 5: Check API Routes
  await runTest('Check API Routes', async () => {
    const usersRoute = await client.fs.readFile('src/routes/users.js', 'utf8');
    const productsRoute = await client.fs.readFile('src/routes/products.js', 'utf8');
    const ordersRoute = await client.fs.readFile('src/routes/orders.js', 'utf8');
    
    if (!usersRoute.includes('get') || !usersRoute.includes('post')) {
      throw new Error('Users route missing get or post');
    }
    if (!productsRoute.includes('get')) {
      throw new Error('Products route missing get');
    }
    if (!ordersRoute.includes('get') || !ordersRoute.includes('post')) {
      throw new Error('Orders route missing get or post');
    }
    
    return { details: 'All route files have expected HTTP methods' };
  });

  // Test 6: Check Database Configuration
  await runTest('Check Database Configuration', async () => {
    const dbConfig = await client.fs.readFile('src/config/database.js', 'utf8');
    if (!dbConfig.includes('pg') && !dbConfig.includes('Pool')) {
      throw new Error('Database config missing Pool configuration');
    }
    return { details: 'Database configuration found' };
  });

  // Test 7: Check Migration Setup
  await runTest('Check Migration Setup', async () => {
    const migrateContent = await client.fs.readFile('src/config/migrate.js', 'utf8');
    if (!migrateContent.includes('CREATE TABLE') && !migrateContent.includes('createTable')) {
      throw new Error('Migration script missing table creation logic');
    }
    return { details: 'Migration script found with table creation' };
  });

  // Test 8: Execute pwd Command
  await runTest('Execute pwd Command', async () => {
    const result = await client.exec.exec('pwd', [], { timeout: 5000 });
    if (result.exitCode !== 0) {
      throw new Error(`Command failed with exit code: ${result.exitCode}`);
    }
    return { details: `Working directory: ${result.stdout.trim()}` };
  });

  // Test 9: Execute ls -la Command
  await runTest('Execute ls -la Command', async () => {
    const result = await client.exec.exec('ls', ['-la', '.'], { timeout: 5000 });
    if (result.exitCode !== 0) {
      throw new Error(`Command failed with exit code: ${result.exitCode}`);
    }
    const lines = result.stdout.trim().split('\n').length;
    return { details: `Listed ${lines} lines of output` };
  });

  // Test 10: Check Node.js Version
  await runTest('Check Node.js Version', async () => {
    const result = await client.exec.exec('node', ['--version'], { timeout: 5000 });
    if (result.exitCode !== 0) {
      throw new Error(`Command failed with exit code: ${result.exitCode}`);
    }
    const version = result.stdout.trim();
    return { details: `Node.js version: ${version}` };
  });

  // Test 11: Check npm Version
  await runTest('Check npm Version', async () => {
    const result = await client.exec.exec('npm', ['--version'], { timeout: 5000 });
    if (result.exitCode !== 0) {
      throw new Error(`Command failed with exit code: ${result.exitCode}`);
    }
    const version = result.stdout.trim();
    return { details: `npm version: ${version}` };
  });

  // Test 12: Run npm install (may take longer)
  console.log('\n' + '='.repeat(70));
  console.log('NOTE: npm install may take 1-2 minutes depending on network...');
  console.log('='.repeat(70));
  
  await runTest('Run npm install', async () => {
    const result = await client.exec.exec('npm', ['install'], { timeout: 120000 });
    if (result.exitCode !== 0) {
      throw new Error(`npm install failed with exit code: ${result.exitCode}\nstderr: ${result.stderr}`);
    }
    return { details: 'Dependencies installed successfully' };
  });

  // Test 13: Verify node_modules Created
  await runTest('Verify node_modules Created', async () => {
    const entries = await client.fs.readdir('node_modules');
    const names = entries.map(e => typeof e === 'string' ? e : e.name);
    const hasExpress = names.includes('express');
    const hasPg = names.includes('pg');
    if (!hasExpress) throw new Error('express not in node_modules');
    if (!hasPg) throw new Error('pg not in node_modules');
    return { details: `Found ${entries.length} packages including express and pg` };
  });

  // Test 14: Write Test Configuration File
  await runTest('Write Test Configuration', async () => {
    const testConfig = {
      test: true,
      timestamp: new Date().toISOString(),
      source: 'dogfooding-test'
    };
    await client.fs.writeFile('test-config.json', JSON.stringify(testConfig, null, 2));
    return { details: 'Written test-config.json' };
  });

  // Test 15: Read and Verify Test File
  await runTest('Read and Verify Test Config', async () => {
    const content = await client.fs.readFile('test-config.json', 'utf8');
    const config = JSON.parse(content);
    if (!config.test || config.source !== 'dogfooding-test') {
      throw new Error('Test config verification failed');
    }
    return { details: 'Config file verified correctly' };
  });

  // Test 16: Run npm test (test the application's own tests)
  console.log('\n' + '='.repeat(70));
  console.log('NOTE: Running npm test to verify application tests...');
  console.log('='.repeat(70));
  
  await runTest('Run Application Tests', async () => {
    const result = await client.exec.exec('npm', ['test'], { timeout: 60000 });
    // Note: Tests might fail if database isn't configured, that's OK for SDK test
    // We're testing that the SDK can execute the test command
    return { details: `Test command executed (exit code: ${result.exitCode})` };
  });

  // Test 17: Check Test Files Exist
  await runTest('Check Test Files', async () => {
    const entries = await client.fs.readdir('tests');
    const hasTestFiles = entries.some(e => 
      (typeof e === 'string' ? e : e.name).includes('.test.js') || 
      (typeof e === 'string' ? e : e.name).includes('.spec.js')
    );
    if (!hasTestFiles) throw new Error('No test files found');
    return { details: `Found ${entries.length} test files` };
  });

  // Test 18: File Stat Operation
  await runTest('File Stat Operation', async () => {
    const stat = await client.fs.stat('package.json');
    if (!stat || typeof stat.size !== 'number') {
      throw new Error('Stat operation did not return expected structure');
    }
    return { details: `package.json size: ${stat.size} bytes` };
  });

  // Test 19: Directory Exists
  await runTest('Directory Exists Check', async () => {
    const srcExists = await client.fs.exists('src');
    const testsExists = await client.fs.exists('tests');
    if (!srcExists) throw new Error('src directory does not exist');
    if (!testsExists) throw new Error('tests directory does not exist');
    return { details: 'Both src/ and tests/ directories exist' };
  });

  // Test 20: Cleanup Test File
  await runTest('Cleanup Test File', async () => {
    await client.fs.rm('test-config.json');
    const exists = await client.fs.exists('test-config.json');
    if (exists) throw new Error('File still exists after deletion');
    return { details: 'test-config.json removed successfully' };
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

  // Calculate average latency
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
  console.log('COMPLEX BACKEND DOGFOODING TEST COMPLETE');
  console.log('='.repeat(70));

  return { success: failed === 0, results, passed, failed, avgLatency };
}

runTests().then(result => {
  // Write results to file for report generation
  const fs = require('fs');
  const report = {
    date: new Date().toISOString(),
    ...result
  };
  fs.writeFileSync('/tmp/complex-backend-dogfooding-results.json', JSON.stringify(report, null, 2));
  process.exit(result.success ? 0 : 1);
}).catch(error => {
  console.error('Test execution failed:', error);
  process.exit(1);
});
