<script setup lang="ts">
import { computed } from "vue";
import Button from "primevue/button";
import type { WorkspaceRecord } from "@nexus/sdk";

const props = defineProps<{
  workspace: WorkspaceRecord;
  spotlight: { remotePort: number; localPort: number; service: string }[];
  actionPending: string | null;
}>();

const emit = defineEmits<{
  (e: "action", kind: string, ws: WorkspaceRecord): void;
}>();

const stateClass = computed(() => {
  const s = props.workspace.state ?? "unknown";
  return `nx-badge--${s}`;
});

const canStart   = computed(() => ["stopped", "created", "restored", "paused"].includes(props.workspace.state));
const canStop    = computed(() => ["running", "paused"].includes(props.workspace.state));

function act(kind: string) {
  emit("action", kind, props.workspace);
}

const shortId = computed(() => props.workspace.id.slice(0, 8));
const repoLabel = computed(() => {
  const r = props.workspace.repo || "";
  if (!r) return "—";
  // strip https:// prefix for display
  return r.replace(/^https?:\/\//, "").replace(/\.git$/, "");
});
const refLabel = computed(() => props.workspace.ref || "—");
const updatedLabel = computed(() => {
  if (!props.workspace.updatedAt) return "";
  return new Date(props.workspace.updatedAt).toLocaleString();
});
</script>

<template>
  <div class="ws-card">
    <div class="ws-card__header">
      <div class="ws-card__title-row">
        <span class="ws-card__name">{{ workspace.workspaceName }}</span>
        <span class="nx-badge" :class="stateClass">{{ workspace.state || "unknown" }}</span>
      </div>
      <div class="ws-card__meta">
        <span class="nx-mono ws-card__id">{{ shortId }}</span>
        <span class="ws-card__sep">·</span>
        <span class="ws-card__repo" :title="workspace.repo">{{ repoLabel }}</span>
        <span class="ws-card__sep">@</span>
        <span class="ws-card__ref">{{ refLabel }}</span>
      </div>
    </div>

    <div v-if="spotlight.length" class="ws-card__spotlight">
      <span v-for="fwd in spotlight" :key="fwd.service" class="ws-card__port">
        <i class="pi pi-link" style="font-size:10px" />
        {{ fwd.service }}:{{ fwd.localPort }}
      </span>
    </div>

    <div class="ws-card__footer">
      <div class="ws-card__actions">
        <Button
          v-if="canStart"
          size="small"
          label="Start"
          icon="pi pi-play"
          :loading="actionPending === 'start'"
          @click="act('start')"
        />
        <Button
          v-if="canStop"
          size="small"
          label="Stop"
          icon="pi pi-stop"
          severity="secondary"
          :loading="actionPending === 'stop'"
          @click="act('stop')"
        />
        <Button
          size="small"
          label="Fork"
          icon="pi pi-copy"
          severity="help"
          :loading="actionPending === 'fork'"
          @click="act('fork')"
        />
        <Button
          size="small"
          icon="pi pi-wifi"
          severity="info"
          v-tooltip.top="'Apply spotlight defaults'"
          :loading="actionPending === 'spotlight-defaults'"
          @click="act('spotlight-defaults')"
        />
        <Button
          size="small"
          icon="pi pi-server"
          severity="info"
          v-tooltip.top="'Apply compose ports'"
          :loading="actionPending === 'spotlight-compose'"
          @click="act('spotlight-compose')"
        />
      </div>
      <span class="ws-card__updated">{{ updatedLabel }}</span>
    </div>
  </div>
</template>

<style scoped>
.ws-card {
  background: var(--nx-surface-2);
  border: 1px solid var(--nx-border);
  border-radius: var(--nx-radius);
  padding: 14px 16px;
  display: flex;
  flex-direction: column;
  gap: 10px;
  transition: border-color 150ms, box-shadow 150ms;
}
.ws-card:hover {
  border-color: var(--nx-border-light);
  box-shadow: 0 2px 16px rgba(0,0,0,.25);
}

.ws-card__title-row {
  display: flex;
  align-items: center;
  gap: 10px;
}
.ws-card__name {
  font-size: 15px;
  font-weight: 600;
  color: var(--nx-text-primary);
}

.ws-card__meta {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  margin-top: 2px;
}
.ws-card__id   { color: var(--nx-text-muted); }
.ws-card__sep  { color: var(--nx-text-muted); }
.ws-card__repo { color: var(--nx-text-secondary); font-size: 12px; max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.ws-card__ref  { color: var(--nx-accent); font-size: 12px; font-family: "JetBrains Mono", monospace; }

.ws-card__spotlight {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
}
.ws-card__port {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 11px;
  padding: 2px 8px;
  background: var(--nx-accent-muted);
  color: var(--nx-accent);
  border-radius: 99px;
  font-family: "JetBrains Mono", monospace;
}

.ws-card__footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  flex-wrap: wrap;
}
.ws-card__actions {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.ws-card__updated {
  font-size: 11px;
  color: var(--nx-text-muted);
  margin-left: auto;
}
</style>
