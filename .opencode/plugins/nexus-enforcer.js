// Nexus Enforcer Plugin for OpenCode
// Native ES module format as per OpenCode docs

export const NexusEnforcer = async ({ project, client, $, directory, worktree }) => {
  // Load config
  const configPath = `${directory}/.nexus/enforcer-config.json`;
  const overridesPath = `${directory}/.nexus/enforcer-config.local.json`;
  
  let config = {
    enabled: true,
    enforceWorkspace: true,
    enforceDogfooding: true,
    blockStopWithTodos: true
  };
  
  try {
    const base = await $`cat ${configPath}`.json();
    config = { ...config, ...base };
  } catch {}
  
  try {
    const overrides = await $`cat ${overridesPath}`.json();
    config = { ...config, ...overrides };
  } catch {}
  
  console.log('[nexus-enforcer] Loaded:', { enabled: config.enabled });
  
  return {
    'tool.execute.before': async (input, output) => {
      if (!config.enabled) return;
      
      const isWrite = ['write', 'edit', 'bash'].includes(input.tool);
      if (isWrite && config.enforceWorkspace) {
        const workspaceFile = `${directory}/.nexus/workspace.json`;
        const exists = await $`test -f ${workspaceFile} 2>/dev/null && echo yes || echo no`.text();
        
        if (exists.trim() !== 'yes') {
          throw new Error('[NEXUS ENFORCEMENT] Not in workspace. Run: nexus workspace create <name>');
        }
      }
    },
    
    'tool.execute.after': async (input, output) => {
      if (!config.enabled) return;
      
      const result = JSON.stringify(output.result || '');
      if (result.match(/done|complete|finished/i) && config.enforceDogfooding) {
        const frictionLog = `${directory}/.nexus/dogfooding/friction-log.md`;
        const exists = await $`test -f ${frictionLog} 2>/dev/null && echo yes || echo no`.text();
        
        if (exists.trim() !== 'yes') {
          console.warn('[NEXUS ENFORCEMENT] Missing friction log');
        }
      }
    },
    
    'todo.updated': async ({ todo }) => {
      if (!config.enabled || !config.blockStopWithTodos) return;
      
      const incomplete = todo.items?.filter(t => t.status !== 'completed').length || 0;
      if (incomplete > 0) {
        console.log(`[NEXUS BOULDER] ${incomplete} tasks remaining. Keep rolling!`);
      }
    }
  };
};

export default NexusEnforcer;
