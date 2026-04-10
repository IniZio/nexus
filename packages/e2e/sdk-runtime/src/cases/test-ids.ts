export const runtimeSelectionCaseIds = [
  'runtime-selection/pass-firecracker',
  'runtime-selection/unsupported-nested-virt-seatbelt',
  'runtime-selection/hard-fail-create-fails',
  'runtime-selection/installable-missing-setup-fails',
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

export const toolsAuthForwardingCaseIds = [
  'tools-auth-forwarding/mint-exec-revoke',
] as const;

export const implementedCaseIds = [
  ...runtimeSelectionCaseIds,
  ...worktreeSyncCaseIds,
  ...lifecycleHooksCaseIds,
  ...spotlightComposeCaseIds,
  ...toolsAuthForwardingCaseIds,
] as const;
