const { lstatSync, readFileSync, writeFileSync, existsSync } = require('fs')
const { basename, join } = require('path')
const { log } = require('./utils')

let config = {
  enabled: false,
  infiniteBoulder: false,
  enforceCommits: false,
  enforceTests: false,
  enforceTypecheck: false,
  enforceDogfooding: false,
  enforceWorkspace: false
}
let iteration = 0
let client = null

module.exports = (ctx) => {
  const { client: c, events } = ctx
  client = c

  const configPath = join(process.cwd(), '.nexus/enforcer.json')
  if (existsSync(configPath)) {
    try {
      const configData = readFileSync(configPath, 'utf8')
      const loadedConfig = JSON.parse(configData)
      config = { ...config, ...loadedConfig }
      log('CONFIG_LOADED', config)
    } catch (e) {
      log('CONFIG_ERROR', { error: e.message })
    }
  } else {
    log('CONFIG_MISSING', { path: configPath })
  }

  const isGitRepo = existsSync(join(process.cwd(), '.git'))
  const isNexusRepo = basename(process.cwd()) === 'nexus'
  const isAgentWorkspace = process.cwd().includes('/workspace/') || process.env.NEXUS_WORKSPACE

  if (config.enabled) {
    log('ENFORCER_ENABLED', { 
      isGitRepo, 
      isNexusRepo, 
      isAgentWorkspace,
      infiniteBoulder: config.infiniteBoulder,
      enforceCommits: config.enforceCommits,
      enforceTests: config.enforceTests,
      enforceTypecheck: config.enforceTypecheck,
      enforceDogfooding: config.enforceDogfooding,
      enforceWorkspace: config.enforceWorkspace
    })
  }

  const validateWorkspace = async () => {
    if (!config.enforceWorkspace || !config.enabled) return true

    const validPatterns = [
      /\/workspace\//,
      /\/worktrees\//,
      /\/tmp\/nexus-/,
      /\/agent-\w+\//
    ]

    const cwd = process.cwd()
    const isValid = validPatterns.some(p => p.test(cwd))

    if (!isValid) {
      log('WORKSPACE_VIOLATION', { cwd })
      throw new Error(`WORKSPACE: Invalid working directory. Expected agent workspace or git worktree.\nCurrent: ${cwd}\nPatterns: ${validPatterns.map(p => p.source).join(', ')}`)
    }

    return true
  }

  const validateGit = async () => {
    if (!config.enabled) return true

    const requiredFiles = ['AGENTS.md', 'CLAUDE.md'].filter(f => !existsSync(join(process.cwd(), f)))

    if (requiredFiles.length > 0 && isGitRepo && isNexusRepo && !isAgentWorkspace) {
      log('GIT_MISSING_FILES', { files: requiredFiles })
      throw new Error(`GIT: Repository missing required governance files.\nMissing: ${requiredFiles.join(', ')}\n\nExpected files: ${['AGENTS.md', 'CLAUDE.md'].join(', ')}`)
    }

    if (isGitRepo) {
      const status = execSync('git status --porcelain', { encoding: 'utf8' })
      const staged = execSync('git diff --cached --name-only', { encoding: 'utf8' })
      const untracked = execSync('git ls-files --others --exclude-standard', { encoding: 'utf8' })
      const allFiles = execSync('git ls-files', { encoding: 'utf8' })
      const hasAgentConfig = /AGENTS\.md|CLAUDE\.md|\.claude/.test(allFiles)

      const issues = []
      if (status.trim() && !staged.includes('AGENTS.md') && !staged.includes('CLAUDE.md') && !hasAgentConfig) {
        issues.push('Changes exist but no agent config files staged')
      }
      if (untracked.includes('AGENTS.md') || untracked.includes('CLAUDE.md')) {
        issues.push('Agent config files are untracked')
      }
      if (issues.length > 0) {
        log('GIT_VIOLATION', { issues })
        throw new Error(`GIT: Governance violation detected.\n${issues.map(i => `- ${i}`).join('\n')}`)
      }
    }

    return true
  }

  const validateCommit = async () => {
    if (!config.enforceCommits || !config.enabled || !isGitRepo) return true

    const commitMsg = execSync('git log -1 --format=%s', { encoding: 'utf8' }).trim()
    const patterns = [
      /^[A-Z]/,
      /^[A-Z][a-z]+(\s[A-Z][a-z]+)*$/,
      /^(feat|fix|docs|style|refactor|perf|test|chore|build|ci|revert)(\([a-z]+\))?: .+/
    ]

    if (!patterns.some(p => p.test(commitMsg))) {
      log('COMMIT_VIOLATION', { message: commitMsg })
      throw new Error(`COMMIT: Invalid commit message format.\nCurrent: "${commitMsg}"\nExpected: Imperative mood, capitalized, with optional type (feat|fix|docs|refactor|...)`)
    }

    return true
  }

  const validateTests = async () => {
    if (!config.enforceTests || !config.enabled) return true

    const { lstatSync, existsSync, readFileSync } = require('fs')
    const { join } = require('path')

    const packageJson = join(process.cwd(), 'package.json')
    if (!existsSync(packageJson)) return true

    try {
      const pkg = JSON.parse(readFileSync(packageJson, 'utf8'))
      const scripts = Object.keys(pkg.scripts || {})

      if (!scripts.includes('test') && !scripts.includes('test:watch')) {
        log('TESTS_MISSING', { scripts })
        throw new Error('TESTS: No test script found in package.json.\nAdd "test" or "test:watch" to scripts.')
      }
    } catch (e) {
      log('TESTS_ERROR', { error: e.message })
    }

    return true
  }

  const validateTypecheck = async () => {
    if (!config.enforceTypecheck || !config.enabled) return true

    const { lstatSync, existsSync, readFileSync } = require('fs')
    const { join } = require('path')

    const packageJson = join(process.cwd(), 'package.json')
    if (!existsSync(packageJson)) return true

    try {
      const pkg = JSON.parse(readFileSync(packageJson, 'utf8'))
      const scripts = Object.keys(pkg.scripts || {})

      if (!scripts.includes('typecheck') && !scripts.includes('type:check') && 
          !scripts.some(s => s.includes('type') && pkg.scripts[s]?.includes('tsc'))) {
        log('TYPECHECK_MISSING', { scripts })
        throw new Error('TYPECHECK: No typecheck script found in package.json.\nAdd "typecheck" or "type:check" using tsc --noEmit.')
      }
    } catch (e) {
      log('TYPECHECK_ERROR', { error: e.message })
    }

    return true
  }

  const validateDogfooding = async () => {
    if (!config.enforceDogfooding || !config.enabled || !isGitRepo) return true

    const frictionLog = '.nexus/dogfooding/friction-log.md'
    if (!existsSync(frictionLog)) {
      log('DOGFOODING_MISSING', { path: frictionLog })
      throw new Error(`DOGFOODING: Friction log not found at "${frictionLog}".\nRun "nexus dogfooding init" to set up dogfooding tracking.`)
    }

    return true
  }

  return {
    'session.start': async () => {
      log('SESSION_START')
      if (config.enabled && !isAgentWorkspace) {
        log('WORKSPACE_WARNING', { 
          message: 'Not in agent workspace - governance checks may fail',
          cwd: process.cwd()
        })
      }
    },
    'session.end': async () => {
      log('SESSION_END')
    },
    'input.before': async (input) => {
      const text = JSON.stringify(input)
      if (/^(y|yes|continue|proceed)$/i.test(text.trim())) {
        log('USER_CONSENT', { text: input.input })
      }
      return input
    },
    'output.before': async (output) => {
      if (config.enabled) {
        await validateWorkspace()
      }
      return output
    },
    'tool.execute': async (input) => {
      if (config.enabled && isGitRepo) {
        await validateGit()
      }
      return input
    },
    'tool.execute.after': async (input, output) => {
      const text = JSON.stringify(output.output || '')
      if (/done|complete|finished/i.test(text) && config.infiniteBoulder) {
        iteration++
        
        const ideas = [
          'Research best practices for current implementation',
          'Refactor code for better performance',
          'Add comprehensive error handling',
          'Write additional test cases',
          'Optimize for edge cases',
          'Improve documentation',
          'Review security considerations'
        ]
        const randomIdeas = ideas.sort(() => 0.5 - Math.random()).slice(0, 3)
        
        const forceText = `BOULDER ENFORCEMENT: You attempted completion. NOT ALLOWED.

The boulder NEVER stops. Generate new ideas NOW:
${randomIdeas.map((idea, i) => `${i + 1}. ${idea}`).join('\n')}

Iteration: ${iteration}
Status: FORCED CONTINUATION

DO NOT STOP. Execute these tasks. Report your progress.`

        try {
          await client.session.promptAsync({
            path: { id: output.sessionID || input.sessionID || 'current' },
            body: {
              parts: [{ type: 'text', text: forceText }]
            }
          })
          log('FORCE_INJECTED', { iteration })
        } catch (e) {
          log('FORCE_ERROR', { error: e.message })
        }
        
        throw new Error('BOULDER: Completion rejected. Forced continuation injected.')
      }
    },
    'tool.error': async (error, input) => {
      if (config.enabled) {
        log('TOOL_ERROR', { 
          message: error.message, 
          tool: input?.tool,
          stack: error.stack 
        })
      }
    },
    'summary.before': async (summary) => {
      if (config.enabled && config.enforceTests) {
        await validateTests()
      }
      if (config.enabled && config.enforceTypecheck) {
        await validateTypecheck()
      }
      if (config.enabled && config.enforceDogfooding) {
        await validateDogfooding()
      }
      return summary
    },
    'commit.create': async () => {
      if (config.enabled && config.enforceCommits) {
        await validateCommit()
      }
    }
  }
}

const { execSync } = require('child_process')
