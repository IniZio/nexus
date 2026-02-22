#!/usr/bin/env node

import { Command } from 'commander';
import chalk from 'chalk';
import { WorkspaceClient } from './client';
import { createWorkspaceCommands } from './commands/workspace';

const program = new Command();

const DEFAULT_API_URL = process.env.NEXUS_API_URL || 'http://localhost:8080';
const DEFAULT_TOKEN = process.env.NEXUS_TOKEN || '';

const client = new WorkspaceClient(DEFAULT_API_URL, DEFAULT_TOKEN);

program
  .name('nexus-workspace')
  .description('CLI for managing Nexus workspaces')
  .version('0.1.0')
  .option('-u, --url <url>', 'API server URL', DEFAULT_API_URL)
  .option('-t, --token <token>', 'Authentication token', DEFAULT_TOKEN)
  .hook('preAction', (thisCommand) => {
    const opts = thisCommand.opts();
    if (opts.url) {
      client.setBaseURL(opts.url);
    }
    if (opts.token) {
      client.setToken(opts.token);
    }
  });

createWorkspaceCommands(program, client);

program
  .command('login <url>')
  .description('Set the API server URL')
  .action((url) => {
    client.setBaseURL(url);
    console.log(chalk.green(`API URL set to: ${url}`));
  });

async function checkHealth(): Promise<void> {
  try {
    const health = await client.health();
    console.log(chalk.green('✓'), chalk.gray('Connected to'), chalk.cyan(health.status));
  } catch {
    console.log(chalk.red('✗'), chalk.gray('Not connected to daemon'));
  }
}

program
  .hook('preAction', async (thisCommand) => {
    if (thisCommand.name() === 'login') return;
    await checkHealth();
  });

program.parse(process.argv);

if (!process.argv.slice(2).length) {
  program.outputHelp();
}
