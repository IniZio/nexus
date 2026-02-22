import { Command } from 'commander';
import chalk from 'chalk';
import ora from 'ora';
import { WorkspaceClient, Workspace, CreateWorkspaceRequest, PortMapping } from '../client';

export function createWorkspaceCommands(program: Command, client: WorkspaceClient): void {
  const workspace = program
    .command('workspace')
    .alias('ws')
    .description('Manage workspaces');

  workspace
    .command('create')
    .description('Create a new workspace')
    .option('-n, --name <name>', 'Workspace name')
    .option('-d, --display-name <name>', 'Display name')
    .option('-r, --repo <url>', 'Repository URL')
    .option('-b, --branch <branch>', 'Branch name')
    .option('--image <image>', 'Docker image')
    .action(async (options) => {
      const spinner = ora('Creating workspace...').start();

      try {
        const req: CreateWorkspaceRequest = {
          name: options.name || `workspace-${Date.now()}`,
          display_name: options.displayName,
          repository_url: options.repo,
          branch: options.branch,
          config: options.image ? { image: options.image } : undefined,
        };

        const ws = await client.createWorkspace(req);
        spinner.succeed(chalk.green('Workspace created'));
        console.log(chalk.bold('\nWorkspace Details:'));
        console.log(`  ID: ${ws.id}`);
        console.log(`  Name: ${ws.name}`);
        console.log(`  Status: ${ws.status}`);
      } catch (error: any) {
        spinner.fail(chalk.red('Failed to create workspace'));
        console.error(chalk.red(error.message));
        process.exit(1);
      }
    });

  workspace
    .command('list')
    .alias('ls')
    .description('List all workspaces')
    .action(async () => {
      const spinner = ora('Fetching workspaces...').start();

      try {
        const result = await client.listWorkspaces();
        spinner.stop();

        if (result.workspaces.length === 0) {
          console.log(chalk.yellow('No workspaces found'));
          return;
        }

        console.log(chalk.bold(`\nWorkspaces (${result.total}):\n`));
        console.log(
          result.workspaces
            .map((ws: Workspace) =>
              `  ${chalk.cyan(ws.id.padEnd(20))} ${ws.name.padEnd(20)} ${getStatusColor(ws.status)(ws.status)}`
            )
            .join('\n')
        );
      } catch (error: any) {
        spinner.fail(chalk.red('Failed to list workspaces'));
        console.error(chalk.red(error.message));
        process.exit(1);
      }
    });

  workspace
    .command('status <id>')
    .description('Get workspace status')
    .action(async (id) => {
      const spinner = ora('Fetching workspace status...').start();

      try {
        const ws = await client.getWorkspace(id);
        spinner.succeed('Workspace status:');

        console.log(chalk.bold('\nWorkspace Details:'));
        console.log(`  ID: ${ws.id}`);
        console.log(`  Name: ${ws.name}`);
        console.log(`  Display Name: ${ws.display_name || '-'}`);
        console.log(`  Status: ${getStatusColor(ws.status)(ws.status)}`);
        console.log(`  Backend: ${ws.backend}`);
        if (ws.repository?.url) {
          console.log(`  Repository: ${ws.repository.url}`);
        }
        if (ws.branch) {
          console.log(`  Branch: ${ws.branch}`);
        }
        if (ws.ports && ws.ports.length > 0) {
          console.log(chalk.bold('\n  Ports:'));
          ws.ports.forEach((p: PortMapping) => {
            console.log(`    ${p.name}: ${p.host_port}:${p.container_port} (${p.protocol})`);
          });
        }
        console.log(`\n  Created: ${new Date(ws.created_at).toLocaleString()}`);
        console.log(`  Updated: ${new Date(ws.updated_at).toLocaleString()}`);
      } catch (error: any) {
        spinner.fail(chalk.red('Failed to get workspace status'));
        console.error(chalk.red(error.message));
        process.exit(1);
      }
    });

  workspace
    .command('start <id>')
    .description('Start a workspace')
    .action(async (id) => {
      const spinner = ora('Starting workspace...').start();

      try {
        const ws = await client.startWorkspace(id);
        spinner.succeed(chalk.green('Workspace started'));
        console.log(`  Status: ${ws.status}`);
      } catch (error: any) {
        spinner.fail(chalk.red('Failed to start workspace'));
        console.error(chalk.red(error.message));
        process.exit(1);
      }
    });

  workspace
    .command('stop <id>')
    .description('Stop a workspace')
    .option('-t, --timeout <seconds>', 'Timeout in seconds', '30')
    .action(async (id, options) => {
      const spinner = ora('Stopping workspace...').start();

      try {
        const ws = await client.stopWorkspace(id, parseInt(options.timeout));
        spinner.succeed(chalk.green('Workspace stopped'));
        console.log(`  Status: ${ws.status}`);
      } catch (error: any) {
        spinner.fail(chalk.red('Failed to stop workspace'));
        console.error(chalk.red(error.message));
        process.exit(1);
      }
    });

  workspace
    .command('delete <id>')
    .description('Delete a workspace')
    .option('-f, --force', 'Force delete without confirmation')
    .action(async (id, options) => {
      if (!options.force) {
        const readline = require('readline').createInterface({
          input: process.stdin,
          output: process.stdout,
        });

        const answer = await new Promise<string>((resolve) => {
          readline.question(
            chalk.yellow(`Are you sure you want to delete workspace ${id}? (y/N): `),
            resolve
          );
        });
        readline.close();

        if (answer.toLowerCase() !== 'y') {
          console.log(chalk.yellow('Cancelled'));
          return;
        }
      }

      const spinner = ora('Deleting workspace...').start();

      try {
        await client.deleteWorkspace(id);
        spinner.succeed(chalk.green('Workspace deleted'));
      } catch (error: any) {
        spinner.fail(chalk.red('Failed to delete workspace'));
        console.error(chalk.red(error.message));
        process.exit(1);
      }
    });

  workspace
    .command('exec <id> <command...>')
    .description('Execute a command in a workspace')
    .action(async (id, command) => {
      const spinner = ora('Executing command...').start();

      try {
        const output = await client.exec(id, command);
        spinner.stop();
        console.log(output || chalk.gray('(no output)'));
      } catch (error: any) {
        spinner.fail(chalk.red('Failed to execute command'));
        console.error(chalk.red(error.message));
        process.exit(1);
      }
    });

  workspace
    .command('logs <id>')
    .description('Get workspace logs')
    .option('-t, --tail <lines>', 'Number of lines to show', '100')
    .action(async (id, options) => {
      const spinner = ora('Fetching logs...').start();

      try {
        const logs = await client.getLogs(id, parseInt(options.tail));
        spinner.stop();
        console.log(logs || chalk.gray('(no logs)'));
      } catch (error: any) {
        spinner.fail(chalk.red('Failed to get logs'));
        console.error(chalk.red(error.message));
        process.exit(1);
      }
    });

  workspace
    .command('use <id>')
    .description('Set active workspace')
    .action(async (id) => {
      try {
        const ws = await client.getWorkspace(id);
        console.log(chalk.green(`Active workspace set to: ${ws.name} (${ws.id})`));
      } catch (error: any) {
        console.error(chalk.red(error.message));
        process.exit(1);
      }
    });
}

function getStatusColor(status: string): (text: string) => string {
  switch (status) {
    case 'running':
      return chalk.green;
    case 'stopped':
      return chalk.red;
    case 'creating':
      return chalk.yellow;
    case 'sleeping':
      return chalk.blue;
    case 'error':
      return chalk.red.bold;
    default:
      return chalk.gray;
  }
}
