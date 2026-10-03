export type NexusBinding = { ref: string; root: string }

declare module 'claude-code' {
  interface PluginState {
    'nexus-subagent': { bindings: Record<string, NexusBinding> }
  }
}
