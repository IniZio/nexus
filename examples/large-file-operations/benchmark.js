const fs = require('fs');
const path = require('path');
const { spawn } = require('child_process');

const GENERATED_DIR = path.join(__dirname, 'generated');

function formatBytes(bytes) {
  if (bytes < 1024) return bytes + ' B';
  if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(2) + ' KB';
  return (bytes / 1024 / 1024).toFixed(2) + ' MB';
}

function getDirectoryStats(dir) {
  const stats = {
    totalFiles: 0,
    totalSize: 0,
    largestFile: { name: '', size: 0 },
    byType: {}
  };

  function walk(currentDir) {
    const files = fs.readdirSync(currentDir);
    for (const file of files) {
      const filepath = path.join(currentDir, file);
      const stat = fs.statSync(filepath);
      if (stat.isDirectory()) {
        walk(filepath);
      } else {
        stats.totalFiles++;
        stats.totalSize += stat.size;

        const ext = path.extname(file).toLowerCase();
        if (!stats.byType[ext]) {
          stats.byType[ext] = { count: 0, size: 0 };
        }
        stats.byType[ext].count++;
        stats.byType[ext].size += stat.size;

        if (stat.size > stats.largestFile.size) {
          stats.largestFile = { name: file, size: stat.size };
        }
      }
    }
  }

  if (fs.existsSync(dir)) {
    walk(dir);
  }

  return stats;
}

function runNexusCommand(args) {
  return new Promise((resolve, reject) => {
    const startTime = Date.now();
    const nexusPath = path.join(__dirname, '..', '..', 'packages', 'cli', 'bin', 'nexus.js');
    
    if (!fs.existsSync(nexusPath)) {
      console.log('Nexus CLI not found, simulating benchmark...');
      resolve({ 
        exitCode: 0, 
        stdout: 'SIMULATION MODE - No actual Nexus run',
        stderr: '',
        duration: Date.now() - startTime
      });
      return;
    }

    const proc = spawn('node', [nexusPath, ...args], {
      cwd: __dirname,
      env: { ...process.env }
    });

    let stdout = '';
    let stderr = '';

    proc.stdout.on('data', (data) => {
      stdout += data.toString();
    });

    proc.stderr.on('data', (data) => {
      stderr += data.toString();
    });

    proc.on('close', (code) => {
      resolve({
        exitCode: code,
        stdout,
        stderr,
        duration: Date.now() - startTime
      });
    });

    proc.on('error', reject);
  });
}

async function runBenchmarks() {
  console.log('=== Nexus Large File Operations Benchmark ===\n');

  const stats = getDirectoryStats(GENERATED_DIR);
  console.log('Test Data Statistics:');
  console.log(`  Total Files: ${stats.totalFiles}`);
  console.log(`  Total Size: ${formatBytes(stats.totalSize)}`);
  console.log(`  Largest File: ${stats.largestFile.name} (${formatBytes(stats.largestFile.size)})`);
  console.log('  By Type:');
  for (const [type, typeStats] of Object.entries(stats.byType)) {
    console.log(`    ${type || '(no ext)'}: ${typeStats.count} files, ${formatBytes(typeStats.size)}`);
  }

  console.log('\n--- Benchmark 1: Initial Analysis ---');
  const result1 = await runNexusCommand(['analyze', '--dir', GENERATED_DIR]);
  console.log(`Duration: ${result1.duration}ms`);
  console.log(`Exit Code: ${result1.exitCode}`);
  if (result1.stdout) console.log(result1.stdout.substring(0, 500));

  console.log('\n--- Benchmark 2: File Change Detection ---');
  const testFile = path.join(GENERATED_DIR, 'test_change.txt');
  fs.writeFileSync(testFile, 'Initial content');
  
  const startChange = Date.now();
  fs.writeFileSync(testFile, 'Changed content');
  
  const result2 = await runNexusCommand(['watch', '--dir', GENERATED_DIR, '--timeout', '2000']);
  console.log(`Duration: ${result2.duration}ms`);
  console.log(`Exit Code: ${result2.exitCode}`);
  
  fs.unlinkSync(testFile);

  console.log('\n--- Benchmark 3: Pattern Search ---');
  const result3 = await runNexusCommand(['search', '--dir', GENERATED_DIR, '--pattern', 'function|class|const']);
  console.log(`Duration: ${result3.duration}ms`);
  console.log(`Exit Code: ${result3.exitCode}`);
  if (result3.stdout) console.log(result3.stdout.substring(0, 300));

  console.log('\n--- Benchmark 4: Memory Usage ---');
  const result4 = await runNexusCommand(['analyze', '--dir', GENERATED_DIR, '--memory-check']);
  console.log(`Duration: ${result4.duration}ms`);
  console.log(`Exit Code: ${result4.exitCode}`);

  console.log('\n=== Benchmark Complete ===');
  console.log('\nPerformance Targets:');
  console.log('  Initial Analysis: < 30 seconds for 200MB');
  console.log('  Change Detection: < 2 seconds latency');
  console.log('  Pattern Search: < 10 seconds for 200MB');
  console.log('  Memory Usage: < 1GB RSS');
}

runBenchmarks().catch(console.error);
