#!/usr/bin/env node
/**
 * Boulder CLI Tool
 * 
 * CLI for managing the boulder enforcer.
 * Commands:
 *   boulder status   - Show current iteration, tasks, idle time
 *   boulder reset    - Reset boulder state (for testing)
 *   boulder enforce  - Manually trigger enforcement
 *   boulder config  - Show/configure boulder settings
 */

import * as fs from 'fs';
import * as path from 'path';

const BOULDER_DIR = '.nexus/boulder';
const STATE_FILE = path.join(BOULDER_DIR, 'state.json');
const TASKS_FILE = path.join(BOULDER_DIR, 'tasks.json');
const CONFIG_FILE = path.join(BOULDER_DIR, 'config.json');

interface BoulderState {
  iteration: number;
  sessionStartTime: number;
  totalWorkTimeMs: number;
  tasksCompleted: number;
  tasksCreated: number;
  lastActivity: number;
  status: string;
}

interface Task {
  id: string;
  description: string;
  iteration: number;
  status: 'pending' | 'active' | 'paused' | 'done';
  dependencies: string[];
  createdAt: number;
  lastActiveAt: number;
  category: string;
  priority: number;
}

interface BoulderConfig {
  minTasksInQueue: number;
  idleThresholdMs: number;
  nextTasksCount: number;
}

const DEFAULT_CONFIG: BoulderConfig = {
  minTasksInQueue: 5,
  idleThresholdMs: 60000,
  nextTasksCount: 3,
};

function ensureBoulderDir(): void {
  if (!fs.existsSync(BOULDER_DIR)) {
    fs.mkdirSync(BOULDER_DIR, { recursive: true });
  }
}

function readState(): BoulderState | null {
  if (!fs.existsSync(STATE_FILE)) {
    return null;
  }
  try {
    const data = fs.readFileSync(STATE_FILE, 'utf-8');
    return JSON.parse(data);
  } catch {
    return null;
  }
}

function writeState(state: BoulderState): void {
  ensureBoulderDir();
  fs.writeFileSync(STATE_FILE, JSON.stringify(state, null, 2));
}

function readTasks(): Task[] {
  if (!fs.existsSync(TASKS_FILE)) {
    return [];
  }
  try {
    const data = fs.readFileSync(TASKS_FILE, 'utf-8');
    const parsed = JSON.parse(data);
    return parsed.tasks || [];
  } catch {
    return [];
  }
}

function writeTasks(tasks: Task[], globalIteration: number, taskIdCounter: number): void {
  ensureBoulderDir();
  fs.writeFileSync(TASKS_FILE, JSON.stringify({
    globalIteration,
    taskIdCounter,
    tasks
  }, null, 2));
}

function readConfig(): BoulderConfig {
  if (!fs.existsSync(CONFIG_FILE)) {
    return DEFAULT_CONFIG;
  }
  try {
    const data = fs.readFileSync(CONFIG_FILE, 'utf-8');
    return { ...DEFAULT_CONFIG, ...JSON.parse(data) };
  } catch {
    return DEFAULT_CONFIG;
  }
}

function writeConfig(config: BoulderConfig): void {
  ensureBoulderDir();
  fs.writeFileSync(CONFIG_FILE, JSON.stringify(config, null, 2));
}

function formatDuration(ms: number): string {
  const secs = Math.floor(ms / 1000);
  const mins = Math.floor(secs / 60);
  const hours = Math.floor(mins / 60);
  
  if (hours > 0) {
    return `${hours}h ${mins % 60}m ${secs % 60}s`;
  }
  if (mins > 0) {
    return `${mins}m ${secs % 60}s`;
  }
  return `${secs}s`;
}

function formatTimeSince(timestamp: number): string {
  return formatDuration(Date.now() - timestamp);
}

function cmdStatus(): void {
  const state = readState();
  const tasks = readTasks();
  const config = readConfig();
  
  if (!state) {
    console.log('┌─────────────────────────────────────────┐');
    console.log('│           BOULDER ENFORCER               │');
    console.log('├─────────────────────────────────────────┤');
    console.log('│  Status: Not initialized                │');
    console.log('│  Run "boulder enforce" to start          │');
    console.log('└─────────────────────────────────────────┘');
    return;
  }
  
  const taskStats = {
    total: tasks.length,
    pending: tasks.filter(t => t.status === 'pending').length,
    active: tasks.filter(t => t.status === 'active').length,
    done: tasks.filter(t => t.status === 'done').length,
    paused: tasks.filter(t => t.status === 'paused').length,
  };
  
  const sessionDuration = Date.now() - state.sessionStartTime;
  const _idleTime = Date.now() - state.lastActivity;
  
  console.log('');
  console.log('┌─────────────────────────────────────────┐');
  console.log('│           BOULDER ENFORCER               │');
  console.log('├─────────────────────────────────────────┤');
  console.log(`│  Iteration:       ${String(state.iteration).padEnd(26)}│`);
  console.log(`│  Status:          ${state.status.padEnd(26)}│`);
  console.log(`│  Session Time:    ${formatDuration(sessionDuration).padEnd(26)}│`);
  console.log(`│  Idle Time:       ${formatTimeSince(state.lastActivity).padEnd(26)}│`);
  console.log('├─────────────────────────────────────────┤');
  console.log('│           TASK QUEUE STATISTICS         │');
  console.log('├─────────────────────────────────────────┤');
  console.log(`│  Total Tasks:     ${String(taskStats.total).padEnd(26)}│`);
  console.log(`│  Pending:          ${String(taskStats.pending).padEnd(26)}│`);
  console.log(`│  Active:           ${String(taskStats.active).padEnd(26)}│`);
  console.log(`│  Done:             ${String(taskStats.done).padEnd(26)}│`);
  console.log(`│  Paused:           ${String(taskStats.paused).padEnd(26)}│`);
  console.log('├─────────────────────────────────────────┤');
  console.log('│           COMPLETION STATS              │');
  console.log('├─────────────────────────────────────────┤');
  console.log(`│  Tasks Created:   ${String(state.tasksCreated).padEnd(26)}│`);
  console.log(`│  Tasks Done:      ${String(state.tasksCompleted).padEnd(26)}│`);
  console.log(`│  Work Time:       ${formatDuration(state.totalWorkTimeMs).padEnd(26)}│`);
  console.log('├─────────────────────────────────────────┤');
  console.log('│           CONFIGURATION                 │');
  console.log('├─────────────────────────────────────────┤');
  console.log(`│  Min Queue Size:   ${String(config.minTasksInQueue).padEnd(26)}│`);
  console.log(`│  Idle Threshold:   ${formatDuration(config.idleThresholdMs).padEnd(26)}│`);
  console.log(`│  Next Tasks:       ${String(config.nextTasksCount).padEnd(26)}│`);
  console.log('└─────────────────────────────────────────┘');
  console.log('');
  
  if (taskStats.active > 0) {
    console.log('Active Tasks:');
    tasks.filter(t => t.status === 'active').forEach((task, i) => {
      console.log(`  ${i + 1}. [${task.category}] ${task.description}`);
    });
    console.log('');
  }
}

function cmdReset(): void {
  ensureBoulderDir();
  
  const newState: BoulderState = {
    iteration: 0,
    sessionStartTime: Date.now(),
    totalWorkTimeMs: 0,
    tasksCompleted: 0,
    tasksCreated: 0,
    lastActivity: Date.now(),
    status: 'CONTINUOUS',
  };
  
  writeState(newState);
  writeTasks([], 1, 0);
  
  console.log('┌─────────────────────────────────────────┐');
  console.log('│           BOULDER RESET                 │');
  console.log('├─────────────────────────────────────────┤');
  console.log('│  ✓ Boulder state has been reset         │');
  console.log('│  ✓ Task queue has been cleared          │');
  console.log('│  ✓ Ready for new enforcement cycle      │');
  console.log('└─────────────────────────────────────────┘');
}

function cmdEnforce(): void {
  const state = readState();
  const _config = readConfig();
  
  if (!state) {
    cmdReset();
  }
  
  const currentState = readState()!;
  currentState.iteration += 1;
  currentState.lastActivity = Date.now();
  
  const newTasks = [
    `Improvement iteration ${currentState.iteration + 1}`,
    `Refine architecture patterns`,
    `Enhance error handling`,
    `Optimize build processes`,
    `Update documentation`,
    `Review code quality metrics`,
    `Performance tuning exercise`,
    `Dependency audit`,
    `Technical debt assessment`,
    `Best practices alignment`,
  ];
  
  const tasks = readTasks();
  newTasks.forEach((desc, i) => {
    tasks.push({
      id: `task-${Date.now()}-${i}`,
      description: desc,
      iteration: currentState.iteration,
      status: 'pending',
      dependencies: [],
      createdAt: Date.now(),
      lastActiveAt: Date.now(),
      category: 'improvement',
      priority: Math.floor(Math.random() * 100),
    });
  });
  
  currentState.tasksCreated += newTasks.length;
  writeState(currentState);
  writeTasks(tasks, currentState.iteration + 1, tasks.length);
  
  console.log('┌─────────────────────────────────────────┐');
  console.log('│        BOULDER ENFORCEMENT              │');
  console.log('├─────────────────────────────────────────┤');
  console.log(`│  Iteration: ${currentState.iteration}                              │`);
  console.log('│                                         │');
  console.log('│  Enforcement triggered manually.        │');
  console.log('│  New tasks have been queued:            │');
  console.log('│                                         │');
  newTasks.slice(0, 5).forEach((task, i) => {
    console.log(`│  ${i + 1}. ${task.substring(0, 35).padEnd(35)}│`);
  });
  console.log(`│  ... and ${newTasks.length - 5} more tasks               │`);
  console.log('│                                         │');
  console.log('│  The boulder NEVER stops.               │');
  console.log('└─────────────────────────────────────────┘');
}

function cmdConfig(args: string[]): void {
  const config = readConfig();
  
  if (args.length === 0) {
    console.log('');
    console.log('┌─────────────────────────────────────────┐');
    console.log('│        BOULDER CONFIGURATION            │');
    console.log('├─────────────────────────────────────────┤');
    console.log(`│  minTasksInQueue  = ${String(config.minTasksInQueue).padEnd(20)}│`);
    console.log(`│  idleThresholdMs  = ${String(config.idleThresholdMs).padEnd(20)}│`);
    console.log(`│  nextTasksCount   = ${String(config.nextTasksCount).padEnd(20)}│`);
    console.log('├─────────────────────────────────────────┤');
    console.log('│  Usage: boulder config <key> <value>    │');
    console.log('│  Example: boulder config minTasksInQueue 10│');
    console.log('└─────────────────────────────────────────┘');
    return;
  }
  
  const key = args[0];
  const value = args[1];
  
  if (!key || !value) {
    console.log('Error: Please provide both key and value');
    console.log('Usage: boulder config <key> <value>');
    console.log('Available keys: minTasksInQueue, idleThresholdMs, nextTasksCount');
    return;
  }
  
  const numValue = parseInt(value, 10);
  if (isNaN(numValue)) {
    console.log('Error: Value must be a number');
    return;
  }
  
  switch (key) {
    case 'minTasksInQueue':
      config.minTasksInQueue = numValue;
      break;
    case 'idleThresholdMs':
      config.idleThresholdMs = numValue;
      break;
    case 'nextTasksCount':
      config.nextTasksCount = numValue;
      break;
    default:
      console.log(`Error: Unknown config key "${key}"`);
      console.log('Available keys: minTasksInQueue, idleThresholdMs, nextTasksCount');
      return;
  }
  
  writeConfig(config);
  
  console.log('┌─────────────────────────────────────────┐');
  console.log('│        CONFIGURATION UPDATED             │');
  console.log('├─────────────────────────────────────────┤');
  console.log(`│  ${key} = ${String(numValue).padEnd(28)}│`);
  console.log('└─────────────────────────────────────────┘');
}

function showHelp(): void {
  console.log('');
  console.log('Boulder CLI - Boulder Enforcer Management');
  console.log('');
  console.log('Usage: boulder <command> [options]');
  console.log('');
  console.log('Commands:');
  console.log('  status                Show current iteration, tasks, idle time');
  console.log('  reset                 Reset boulder state (for testing)');
  console.log('  enforce               Manually trigger enforcement');
  console.log('  config [key] [value]  Show or configure boulder settings');
  console.log('  help                  Show this help message');
  console.log('');
  console.log('Examples:');
  console.log('  boulder status');
  console.log('  boulder reset');
  console.log('  boulder enforce');
  console.log('  boulder config');
  console.log('  boulder config minTasksInQueue 10');
  console.log('');
}

function main(): void {
  const args = process.argv.slice(2);
  
  if (args.length === 0 || args[0] === 'help' || args[0] === '--help' || args[0] === '-h') {
    showHelp();
    process.exit(0);
  }
  
  const command = args[0];
  
  switch (command) {
    case 'status':
      cmdStatus();
      break;
    case 'reset':
      cmdReset();
      break;
    case 'enforce':
      cmdEnforce();
      break;
    case 'config':
      cmdConfig(args.slice(1));
      break;
    default:
      console.log(`Unknown command: ${command}`);
      console.log('Run "boulder help" for usage information');
      process.exit(1);
  }
}

main();
