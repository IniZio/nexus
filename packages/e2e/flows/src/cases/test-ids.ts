export const runtimeSelectionCaseIds = [
  'runtime-selection/pass-firecracker',
  'runtime-selection/unsupported-nested-virt-seatbelt',
  'runtime-selection/hard-fail-create-fails',
  'runtime-selection/installable-missing-setup-fails',
] as const;

export const ptyPersistenceCaseIds = [
  'pty-persistence/daemon-restart-tmux-recovery',
] as const;

export const worktreeSyncCaseIds = [
  'worktree-sync/host-to-workspace-file-propagation',
] as const;

export const lifecycleHooksCaseIds = [
  'lifecycle-hooks/prestart-poststart-prestop-order',
] as const;

export const spotlightComposeCaseIds = [
  'spotlight-compose/apply-compose-ports-list-close',
] as const;

export const spotlightMultiProjectTunnelingCaseIds = {
  multipleProjectsSeparateTunnels: 'spotlight.multi-project.multiple-projects-separate-tunnels',
  workspacePortIsolation: 'spotlight.multi-project.workspace-port-isolation',
  tunnelLifecycleIndependence: 'spotlight.multi-project.tunnel-lifecycle-independence',
} as const;

export const toolsAuthForwardingCaseIds = [
  'tools-auth-forwarding/mint-exec-revoke',
] as const;

export const liveToolsAuthCaseIds = [
  'tools-auth-live/opencode-copilot-minimax-and-codex-exec',
] as const;

export const cliRuntimeCaseIds = [
  'cli-runtime/workspace-lifecycle-and-tunnel-flow',
  'cli-runtime/failure-paths-and-usage-validation',
] as const;

export const multiProjectCaseIds = [
  'multi-project/workspace-create-with-projects',
  'multi-project/project-lifecycle-attach-detach',
  'multi-project/project-port-aggregation',
] as const;

export const implementedCaseIds = [
  ...runtimeSelectionCaseIds,
  ...ptyPersistenceCaseIds,
  ...worktreeSyncCaseIds,
  ...lifecycleHooksCaseIds,
  ...spotlightComposeCaseIds,
  ...Object.values(spotlightMultiProjectTunnelingCaseIds),
  ...toolsAuthForwardingCaseIds,
  ...liveToolsAuthCaseIds,
  ...cliRuntimeCaseIds,
  ...multiProjectCaseIds,
] as const;
