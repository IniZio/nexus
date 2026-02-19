/**
 * Boulder System Performance Benchmarks
 * 
 * Benchmarks for measuring performance of the boulder enforcement system.
 * Targets:
 * - Tool call recording: <1ms (in-memory only)
 * - Text analysis: <5ms (in-memory only)
 * - Task queue operations: <5ms (includes disk I/O for persistence)
 * - Interval check overhead: <0.5ms (in-memory only)
 */

import { BoulderIdleDetector } from '../boulder/idle-detector.js';
import { TaskQueue } from '../boulder/task-queue.js';
import { BoulderContinuousEnforcement } from '../boulder/index.js';

interface BenchmarkResult {
  name: string;
  iterations: number;
  totalMs: number;
  avgMs: number;
  minMs: number;
  maxMs: number;
  p95Ms: number;
  p99Ms: number;
}

interface MemorySnapshot {
  heapUsed: number;
  heapTotal: number;
  external: number;
}

function formatNumber(n: number): string {
  return n.toLocaleString('en-US', { maximumFractionDigits: 2 });
}

function runBenchmark(
  name: string,
  fn: () => void,
  iterations: number = 1000
): BenchmarkResult {
  const times: number[] = [];
  
  for (let i = 0; i < iterations; i++) {
    const start = performance.now();
    fn();
    const end = performance.now();
    times.push(end - start);
  }
  
  times.sort((a, b) => a - b);
  
  const totalMs = times.reduce((sum, t) => sum + t, 0);
  const avgMs = totalMs / iterations;
  
  return {
    name,
    iterations,
    totalMs,
    avgMs,
    minMs: times[0],
    maxMs: times[times.length - 1],
    p95Ms: times[Math.floor(iterations * 0.95)],
    p99Ms: times[Math.floor(iterations * 0.99)],
  };
}

function getMemorySnapshot(): MemorySnapshot {
  const usage = process.memoryUsage();
  return {
    heapUsed: usage.heapUsed,
    heapTotal: usage.heapTotal,
    external: usage.external,
  };
}

function formatMemory(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(2)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
}

function printResult(result: BenchmarkResult, threshold?: number): void {
  const passed = threshold === undefined || result.avgMs < threshold;
  const status = passed ? '✓ PASS' : '✗ FAIL';
  
  console.log(`\n${result.name}`);
  console.log(`  Status: ${status}${threshold ? ` (threshold: ${threshold}ms)` : ''}`);
  console.log(`  Iterations: ${formatNumber(result.iterations)}`);
  console.log(`  Total: ${result.totalMs.toFixed(3)}ms`);
  console.log(`  Avg: ${result.avgMs.toFixed(4)}ms`);
  console.log(`  Min: ${result.minMs.toFixed(4)}ms`);
  console.log(`  Max: ${result.maxMs.toFixed(4)}ms`);
  console.log(`  P95: ${result.p95Ms.toFixed(4)}ms`);
  console.log(`  P99: ${result.p99Ms.toFixed(4)}ms`);
}

function printMemorySnapshot(label: string, snapshot: MemorySnapshot): void {
  console.log(`\n${label}`);
  console.log(`  Heap Used: ${formatMemory(snapshot.heapUsed)}`);
  console.log(`  Heap Total: ${formatMemory(snapshot.heapTotal)}`);
  console.log(`  External: ${formatMemory(snapshot.external)}`);
}

console.log('='.repeat(70));
console.log('BOULDER SYSTEM PERFORMANCE BENCHMARKS');
console.log('='.repeat(70));

console.log('\n--- Tool Call Recording Speed ---');
const toolCallDetector = new BoulderIdleDetector();

const toolCallResult = runBenchmark(
  'BoulderIdleDetector.recordToolCall',
  () => {
    toolCallDetector.recordToolCall('test-tool');
  },
  1000
);
printResult(toolCallResult, 1);

console.log('\n--- Text Analysis Speed ---');
const textAnalysisDetector = new BoulderIdleDetector();

const textAnalysisResult = runBenchmark(
  'BoulderIdleDetector.recordTextOutput (with analysis)',
  () => {
    textAnalysisDetector.recordTextOutput('I am done with the task and ready to complete');
  },
  1000
);
printResult(textAnalysisResult, 5);

const shortTextResult = runBenchmark(
  'BoulderIdleDetector.recordTextOutput (short text)',
  () => {
    textAnalysisDetector.recordTextOutput('test');
  },
  1000
);
printResult(shortTextResult, 5);

console.log('\n--- Idle Check Overhead ---');
const idleCheckResult = runBenchmark(
  'BoulderIdleDetector.checkIdle',
  () => {
    textAnalysisDetector.checkIdle();
  },
  1000
);
printResult(idleCheckResult, 0.5);

console.log('\n--- Task Queue Operations ---');
const taskQueue = new TaskQueue();

const addTaskResult = runBenchmark(
  'TaskQueue.addTask',
  () => {
    const task = {
      id: `bench-${Date.now()}-${Math.random()}`,
      description: 'Benchmark task',
      iteration: 1,
      status: 'pending' as const,
      dependencies: [],
      createdAt: Date.now(),
      lastActiveAt: Date.now(),
      category: 'testing' as const,
      priority: 50,
    };
    taskQueue.addTask(task);
  },
  1000
);
printResult(addTaskResult, 5);  // Higher threshold due to disk I/O

const getStatsResult = runBenchmark(
  'TaskQueue.getStats',
  () => {
    taskQueue.getStats();
  },
  1000
);
printResult(getStatsResult, 0.5);

const getNextTaskResult = runBenchmark(
  'TaskQueue.getNextTask',
  () => {
    taskQueue.ensureMinimumTasks(20);
    taskQueue.getNextTask();
  },
  1000
);
printResult(getNextTaskResult, 5);  // Higher threshold due to disk I/O

console.log('\n--- Boulder Enforcement Operations ---');
const enforcement = new BoulderContinuousEnforcement();

const recordToolCallEnforcement = runBenchmark(
  'BoulderContinuousEnforcement.recordToolCall',
  () => {
    enforcement.recordToolCall('test-tool');
  },
  1000
);
printResult(recordToolCallEnforcement, 1);

const recordTextEnforcement = runBenchmark(
  'BoulderContinuousEnforcement.recordTextOutput',
  () => {
    enforcement.recordTextOutput('Working on the implementation');
  },
  1000
);
printResult(recordTextEnforcement, 5);

console.log('\n--- Memory Usage Over 1000 Iterations ---');
const memoryIterations = 1000;
const memorySnapshots: MemorySnapshot[] = [];

console.log(`Running ${memoryIterations} iterations with memory tracking...`);

const initialMemory = getMemorySnapshot();
printMemorySnapshot('Initial Memory', initialMemory);

for (let i = 0; i < memoryIterations; i++) {
  enforcement.recordToolCall(`tool-${i}`);
  enforcement.recordTextOutput(`Text output ${i % 100}`);
  
  if (i % 100 === 0) {
    memorySnapshots.push(getMemorySnapshot());
  }
}

const finalMemory = getMemorySnapshot();
printMemorySnapshot(`After ${memoryIterations} Iterations`, finalMemory);

const memoryGrowth = {
  heapUsed: finalMemory.heapUsed - initialMemory.heapUsed,
  heapTotal: finalMemory.heapTotal - initialMemory.heapTotal,
  external: finalMemory.external - initialMemory.external,
};

console.log('\nMemory Growth:');
console.log(`  Heap Used: ${formatMemory(memoryGrowth.heapUsed)} (${memoryGrowth.heapUsed > 0 ? '+' : ''}${formatMemory(memoryGrowth.heapUsed)})`);
console.log(`  Heap Total: ${formatMemory(memoryGrowth.heapTotal)} (${memoryGrowth.heapTotal > 0 ? '+' : ''}${formatMemory(memoryGrowth.heapTotal)})`);
console.log(`  External: ${formatMemory(memoryGrowth.external)} (${memoryGrowth.external > 0 ? '+' : ''}${formatMemory(memoryGrowth.external)})`);
console.log(`  Per Iteration: ${formatMemory(Math.max(0, memoryGrowth.heapUsed) / memoryIterations)}`);

console.log('\n--- Stress Test: 10,000 Rapid Operations ---');
const stressIterations = 10000;

console.log(`Running ${stressIterations} rapid tool calls...`);
const stressStart = performance.now();
for (let i = 0; i < stressIterations; i++) {
  enforcement.recordToolCall(`stress-tool-${i}`);
}
const stressEnd = performance.now();
const stressTotalMs = stressEnd - stressStart;
const stressAvgMs = stressTotalMs / stressIterations;

console.log(`  Total Time: ${stressTotalMs.toFixed(2)}ms`);
console.log(`  Avg Per Call: ${stressAvgMs.toFixed(4)}ms`);
console.log(`  Throughput: ${(stressIterations / (stressTotalMs / 1000)).toFixed(0)} ops/sec`);

console.log('\n--- Completion Attempt Detection ---');
const completionDetector = new BoulderIdleDetector();

const completionTests = [
  { text: 'I am done', expected: true },
  { text: 'All work is complete', expected: true },
  { text: 'That is all for now', expected: true },
  { text: 'Implementation complete', expected: true },
  { text: 'Working on the next task', expected: false },
  { text: 'Let me read the file', expected: false },
  { text: 'I will complete the edit', expected: false },
  { text: 'Testing in progress', expected: false },
];

let correctDetections = 0;
for (const test of completionTests) {
  const result = completionDetector.recordTextOutput(test.text);
  if (result === test.expected) {
    correctDetections++;
  }
}

console.log(`\nCompletion Detection Accuracy: ${correctDetections}/${completionTests.length} (${((correctDetections / completionTests.length) * 100).toFixed(0)}%)`);
completionTests.forEach(test => {
  const result = completionDetector.recordTextOutput(test.text);
  const status = result === test.expected ? '✓' : '✗';
  console.log(`  ${status} "${test.text}" -> ${result ? 'DETECTED' : 'NOT DETECTED'}`);
});

console.log('\n' + '='.repeat(70));
console.log('BENCHMARK SUMMARY');
console.log('='.repeat(70));

const allResults: { name: string; avg: number; threshold: number }[] = [
  { name: 'Tool Call Recording', avg: toolCallResult.avgMs, threshold: 1 },
  { name: 'Text Analysis', avg: textAnalysisResult.avgMs, threshold: 5 },
  { name: 'Idle Check', avg: idleCheckResult.avgMs, threshold: 0.5 },
  { name: 'TaskQueue.addTask', avg: addTaskResult.avgMs, threshold: 5 },
  { name: 'TaskQueue.getStats', avg: getStatsResult.avgMs, threshold: 0.5 },
  { name: 'TaskQueue.getNextTask', avg: getNextTaskResult.avgMs, threshold: 5 },
  { name: 'Enforcement.recordToolCall', avg: recordToolCallEnforcement.avgMs, threshold: 1 },
  { name: 'Enforcement.recordTextOutput', avg: recordTextEnforcement.avgMs, threshold: 5 },
];

let allPassed = true;
console.log('\nPerformance Targets:');
for (const r of allResults) {
  const passed = r.avg < r.threshold;
  if (!passed) allPassed = false;
  const status = passed ? '✓' : '✗';
  console.log(`  ${status} ${r.name}: ${r.avg.toFixed(4)}ms < ${r.threshold}ms`);
}

console.log('\n' + '='.repeat(70));
console.log(allPassed ? 'ALL BENCHMARKS PASSED ✓' : 'SOME BENCHMARKS FAILED ✗');
console.log('='.repeat(70));

console.log('\nAnalysis:');
if (toolCallResult.avgMs < 0.1) {
  console.log('  - Tool call recording is highly optimized (<0.1ms)');
}
if (textAnalysisResult.avgMs < 2) {
  console.log('  - Text analysis is fast and efficient (<2ms)');
}
if (idleCheckResult.avgMs < 0.2) {
  console.log('  - Idle check overhead is minimal (<0.2ms)');
}
if (memoryGrowth.heapUsed / memoryIterations < 1000) {
  console.log('  - Memory usage per iteration is low (<1KB)');
}
if (stressAvgMs < 0.1) {
  console.log('  - Stress test shows excellent throughput');
}
