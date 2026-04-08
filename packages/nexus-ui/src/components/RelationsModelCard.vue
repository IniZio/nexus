<script setup lang="ts">
import Card from "primevue/card";

const example = {
  repoId: "repo_local_hanlun",
  kind: "local",
  displayName: "hanlun-lms",
  remoteUrl: null,
  nodes: [
    {
      workspaceId: "ws-root",
      worktreeRef: "main",
      parentWorkspaceId: null,
      lineageRootWorkspaceId: "ws-root"
    },
    {
      workspaceId: "ws-child-1",
      worktreeRef: "feature/a",
      parentWorkspaceId: "ws-root",
      lineageRootWorkspaceId: "ws-root"
    },
    {
      workspaceId: "ws-child-2",
      worktreeRef: "feature/a-fork",
      parentWorkspaceId: "ws-child-1",
      lineageRootWorkspaceId: "ws-root"
    }
  ]
};
</script>

<template>
  <Card>
    <template #title>Workspace Relations (Draft)</template>
    <template #content>
      <pre>{{ JSON.stringify(example, null, 2) }}</pre>
    </template>
  </Card>
</template>

<style scoped>
pre {
  margin: 0;
  font-family: "JetBrains Mono", monospace;
  font-size: 12px;
  white-space: pre-wrap;
}
</style>
