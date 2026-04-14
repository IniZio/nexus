<script setup lang="ts">
import { ref, computed } from "vue";
import { useI18n } from "vue-i18n";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import type { SpotlightForward } from "@nexus/sdk";
import type { WorkspaceRelationsGroup, WorkspaceRelationNode } from "@nexus/sdk";

const { t } = useI18n();

const props = defineProps<{
  relations: WorkspaceRelationsGroup[];
  spotlightByWs: Record<string, SpotlightForward[]>;
  pendingAction: Record<string, string | null>;
  loading: boolean;
}>();

const emit = defineEmits<{
  (e: "action", kind: string, wsId: string, wsName: string, extra?: Record<string, string>): void;
  (e: "create"): void;
}>();

// Track which workspace nodes are expanded
const expanded = ref<Set<string>>(new Set());
function toggleExpand(id: string) {
  if (expanded.value.has(id)) {
    expanded.value.delete(id);
  } else {
    expanded.value.add(id);
  }
}

// Track which repo groups are collapsed (all open by default)
const collapsedGroups = ref<Set<string>>(new Set());
function toggleGroup(repoId: string) {
  if (collapsedGroups.value.has(repoId)) {
    collapsedGroups.value.delete(repoId);
  } else {
    collapsedGroups.value.add(repoId);
  }
}

const hasRelations = computed(() => props.relations.length > 0);

// ── Remove confirmation ─────────────────────────────────────────────────
const confirmRemoveId = ref<string | null>(null);
function askRemove(node: WorkspaceRelationNode) {
  confirmRemoveId.value = node.workspaceId;
}
function cancelRemove() {
  confirmRemoveId.value = null;
}
function confirmRemove(node: WorkspaceRelationNode) {
  confirmRemoveId.value = null;
  emit("action", "remove", node.workspaceId, node.workspaceName);
}

const portDrafts = ref<Record<string, { remotePort: string; localPort: string; service: string; host: string }>>({});

function ensurePortDraft(wsId: string) {
  if (!portDrafts.value[wsId]) {
    portDrafts.value[wsId] = { remotePort: "", localPort: "", service: "", host: "127.0.0.1" };
  }
  return portDrafts.value[wsId];
}

function addForward(node: WorkspaceRelationNode) {
  const draft = ensurePortDraft(node.workspaceId);
  emit("action", "spotlight-add", node.workspaceId, node.workspaceName, {
    remotePort: draft.remotePort.trim(),
    localPort: draft.localPort.trim(),
    service: draft.service.trim(),
    host: draft.host.trim() || "127.0.0.1",
  });
}

function removeForward(node: WorkspaceRelationNode, id: string) {
  emit("action", "spotlight-remove", node.workspaceId, node.workspaceName, { id });
}

function worktreePath(node: WorkspaceRelationNode): string {
  if (!node.localWorktreePath) return "—";
  return node.localWorktreePath;
}

function canStart(node: WorkspaceRelationNode)   { return ["stopped", "created", "restored", "paused"].includes(node.state); }
function canStop(node: WorkspaceRelationNode)    { return node.state === "running" || node.state === "paused"; }
function canRestore(node: WorkspaceRelationNode) { return ["stopped", "removed"].includes(node.state); }

function act(kind: string, node: WorkspaceRelationNode) {
  emit("action", kind, node.workspaceId, node.workspaceName);
}

function treeNodes(group: WorkspaceRelationsGroup): { node: WorkspaceRelationNode; depth: number }[] {
  const childrenOf: Record<string, WorkspaceRelationNode[]> = {};
  for (const n of group.nodes) {
    const parent = n.parentWorkspaceId || "__root__";
    if (!childrenOf[parent]) childrenOf[parent] = [];
    childrenOf[parent].push(n);
  }
  for (const k of Object.keys(childrenOf)) {
    childrenOf[k].sort((a, b) => a.createdAt.localeCompare(b.createdAt));
  }
  const roots = group.nodes.filter((n) => !n.parentWorkspaceId || !group.nodes.find((x) => x.workspaceId === n.parentWorkspaceId));
  const result: { node: WorkspaceRelationNode; depth: number }[] = [];
  function walk(n: WorkspaceRelationNode, depth: number) {
    result.push({ node: n, depth });
    for (const child of childrenOf[n.workspaceId] ?? []) {
      walk(child, depth + 1);
    }
  }
  for (const r of roots) walk(r, 0);
  return result;
}
</script>

<template>
  <div class="rel">

    <!-- Empty state -->
    <div v-if="!loading && !hasRelations" class="rel__empty">
      <i class="pi pi-sitemap" />
      <p>No workspaces yet.</p>
      <Button label="New Workspace" icon="pi pi-plus" @click="$emit('create')" />
    </div>

    <!-- Loading shimmer -->
    <div v-else-if="loading && !hasRelations" class="rel__loading">
      <div v-for="i in 3" :key="i" class="shimmer-row" />
    </div>

    <!-- Repo groups -->
    <div v-else class="rel__groups">
      <div
        v-for="group in relations"
        :key="group.repoId"
        class="rel-group"
        :class="{ 'rel-group--collapsed': collapsedGroups.has(group.repoId) }"
      >
        <!-- Group header -->
        <button class="rel-group__header" @click="toggleGroup(group.repoId)">
          <span class="rel-group__icon">
            <i :class="group.repoKind === 'hosted' ? 'pi pi-github' : 'pi pi-folder-open'" />
          </span>
          <span class="rel-group__info">
            <span class="rel-group__name">{{ group.displayName }}</span>
            <span class="rel-group__url">{{ group.remoteUrl || group.repo }}</span>
          </span>
          <span class="rel-group__count">{{ group.nodes.length }}</span>
          <i
            class="pi rel-group__chevron"
            :class="collapsedGroups.has(group.repoId) ? 'pi-chevron-right' : 'pi-chevron-down'"
          />
        </button>

        <!-- Node list -->
        <Transition name="group-collapse">
          <div v-if="!collapsedGroups.has(group.repoId)" class="rel-group__nodes">
            <div
              v-for="{ node, depth } in treeNodes(group)"
              :key="node.workspaceId"
              class="rel-node-wrap"
              :style="{ '--depth': depth }"
            >
              <!-- Row (always visible) -->
              <button
                class="rel-node__row"
                :class="{ 'rel-node__row--expanded': expanded.has(node.workspaceId) }"
                @click="toggleExpand(node.workspaceId)"
              >
                <!-- Tree connector -->
                <span class="rel-node__indent">
                  <span v-for="d in depth" :key="d" class="rel-node__pipe" />
                  <i
                    class="pi rel-node__expander"
                    :class="expanded.has(node.workspaceId) ? 'pi-chevron-down' : 'pi-chevron-right'"
                  />
                </span>

                <!-- State badge -->
                <span class="nx-badge" :class="`nx-badge--${node.state ?? 'unknown'}`">
                  {{ node.state || "unknown" }}
                </span>

                <!-- Name -->
                <span class="rel-node__name">{{ node.workspaceName }}</span>

                <!-- Ref pill -->
                <span v-if="node.worktreeRef" class="rel-node__ref">{{ node.worktreeRef }}</span>

                <!-- Fork label -->
                <span v-if="node.derivedFromRef" class="rel-node__fork-label">
                  forked
                </span>

                <!-- Spotlight ports summary -->
                <span
                  v-if="(spotlightByWs[node.workspaceId] || []).length"
                  class="rel-node__ports-summary"
                >
                  <i class="pi pi-link" />
                  {{ (spotlightByWs[node.workspaceId] || []).length }}
                </span>

                <!-- Pending spinner -->
                <span v-if="pendingAction[node.workspaceId]" class="rel-node__spinner">
                  <i class="pi pi-spin pi-spinner" />
                </span>
              </button>

              <!-- Expanded detail panel -->
              <Transition name="node-expand">
                <div v-if="expanded.has(node.workspaceId)" class="rel-node__detail">

                  <!-- Worktree path — most important for safety -->
                  <div class="detail-row detail-row--path">
                    <span class="detail-label">
                      <i class="pi pi-folder" />
                      Worktree
                    </span>
                    <span class="detail-value detail-value--mono" :title="worktreePath(node)">
                      {{ worktreePath(node) }}
                    </span>
                  </div>

                  <div class="detail-grid">
                    <div class="detail-row">
                      <span class="detail-label">ID</span>
                      <span class="detail-value detail-value--mono">{{ node.workspaceId }}</span>
                    </div>
                    <div class="detail-row">
                      <span class="detail-label">Ref</span>
                      <span class="detail-value">{{ node.worktreeRef || "—" }}</span>
                    </div>
                    <div class="detail-row" v-if="node.derivedFromRef">
                      <span class="detail-label">Forked from</span>
                      <span class="detail-value">{{ node.derivedFromRef }}</span>
                    </div>
                    <div class="detail-row">
                      <span class="detail-label">Backend</span>
                      <span class="detail-value">{{ node.backend || "local" }}</span>
                    </div>
                    <div class="detail-row">
                      <span class="detail-label">Created</span>
                      <span class="detail-value">{{ new Date(node.createdAt).toLocaleString() }}</span>
                    </div>
                    <div class="detail-row">
                      <span class="detail-label">Updated</span>
                      <span class="detail-value">{{ new Date(node.updatedAt).toLocaleString() }}</span>
                    </div>
                    <div v-if="node.localWorktreePath" class="detail-row">
                      <span class="detail-label">Local sync</span>
                      <span class="detail-value detail-value--mono">{{ node.localWorktreePath }}</span>
                    </div>
                  </div>

                  <!-- Port forwarding -->
                  <div class="detail-section">
                    <span class="detail-section-title">Port Forwarding</span>
                    <table class="ports-table">
                      <thead>
                        <tr>
                          <th>Port</th>
                          <th>Local Address</th>
                          <th>Service</th>
                          <th>Action</th>
                        </tr>
                      </thead>
                      <tbody>
                        <tr v-if="!(spotlightByWs[node.workspaceId] || []).length">
                          <td colspan="4" class="ports-table__empty">No ports forwarded</td>
                        </tr>
                        <tr v-for="fwd in spotlightByWs[node.workspaceId] || []" :key="fwd.id">
                          <td class="nx-mono">{{ fwd.remotePort }}</td>
                          <td class="nx-mono">{{ fwd.host || "127.0.0.1" }}:{{ fwd.localPort }}</td>
                          <td>{{ fwd.service || "—" }}</td>
                          <td>
                            <Button
                              size="small"
                              label="Remove"
                              severity="danger"
                              text
                              :disabled="!!pendingAction[node.workspaceId]"
                              @click.stop="removeForward(node, fwd.id)"
                            />
                          </td>
                        </tr>
                      </tbody>
                    </table>
                    <div v-if="node.state === 'running'" class="ports-add">
                      <InputText
                        v-model="ensurePortDraft(node.workspaceId).remotePort"
                        placeholder="Port"
                        class="ports-add__input ports-add__input--sm"
                        @keydown.enter="addForward(node)"
                      />
                      <InputText
                        v-model="ensurePortDraft(node.workspaceId).localPort"
                        placeholder="Local"
                        class="ports-add__input ports-add__input--sm"
                        @keydown.enter="addForward(node)"
                      />
                      <InputText
                        v-model="ensurePortDraft(node.workspaceId).service"
                        placeholder="Service"
                        class="ports-add__input"
                        @keydown.enter="addForward(node)"
                      />
                      <InputText
                        v-model="ensurePortDraft(node.workspaceId).host"
                        placeholder="Host"
                        class="ports-add__input"
                        @keydown.enter="addForward(node)"
                      />
                      <Button
                        size="small"
                        label="Add port"
                        icon="pi pi-plus"
                        :loading="pendingAction[node.workspaceId] === 'spotlight-add'"
                        :disabled="!!pendingAction[node.workspaceId]"
                        @click.stop="addForward(node)"
                      />
                    </div>
                  </div>

                  <!-- Actions -->
                  <div class="detail-actions">
                    <Button
                      size="small"
                      label="Terminal"
                      icon="pi pi-terminal"
                      severity="contrast"
                      :disabled="!!pendingAction[node.workspaceId]"
                      @click.stop="act('terminal-open', node)"
                    />
                    <Button
                      v-if="canStart(node)"
                      size="small"
                      label="Start"
                      icon="pi pi-play"
                      :loading="pendingAction[node.workspaceId] === 'start'"
                      :disabled="!!pendingAction[node.workspaceId]"
                      @click.stop="act('start', node)"
                    />
                    <Button
                      v-if="canStop(node)"
                      size="small"
                      label="Stop"
                      icon="pi pi-stop-circle"
                      severity="danger"
                      :loading="pendingAction[node.workspaceId] === 'stop'"
                      :disabled="!!pendingAction[node.workspaceId]"
                      @click.stop="act('stop', node)"
                    />
                    <Button
                      v-if="canRestore(node)"
                      size="small"
                      label="Restore"
                      icon="pi pi-history"
                      severity="help"
                      :loading="pendingAction[node.workspaceId] === 'restore'"
                      :disabled="!!pendingAction[node.workspaceId]"
                      @click.stop="act('restore', node)"
                    />
                    <Button
                      size="small"
                      label="Fork"
                      icon="pi pi-copy"
                      severity="help"
                      :loading="pendingAction[node.workspaceId] === 'fork'"
                      :disabled="!!pendingAction[node.workspaceId]"
                      @click.stop="act('fork', node)"
                    />
                    <!-- Delete — always available, requires confirmation -->
                    <Button
                      v-if="confirmRemoveId !== node.workspaceId"
                      size="small"
                      label="Delete"
                      icon="pi pi-trash"
                      severity="danger"
                      outlined
                      :loading="pendingAction[node.workspaceId] === 'remove'"
                      :disabled="!!pendingAction[node.workspaceId]"
                      @click.stop="askRemove(node)"
                    />
                    <!-- Inline confirmation for delete -->
                    <template v-if="confirmRemoveId === node.workspaceId">
                      <span class="confirm-label">Delete this workspace?</span>
                      <Button
                        size="small"
                        label="Yes, delete"
                        icon="pi pi-trash"
                        severity="danger"
                        @click.stop="confirmRemove(node)"
                      />
                      <Button
                        size="small"
                        label="Cancel"
                        severity="secondary"
                        @click.stop="cancelRemove()"
                      />
                    </template>
                  </div>

                </div>
              </Transition>
            </div>
          </div>
        </Transition>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* ── Shell ─────────────────────────────────────────────────────────────── */
.rel { display: flex; flex-direction: column; gap: 14px; }

/* Empty / loading */
.rel__empty {
  text-align: center;
  padding: 64px 24px;
  color: var(--nx-text-muted);
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 12px;
}
.rel__empty .pi { font-size: 36px; }
.rel__empty p { margin: 0; font-size: 14px; }

.rel__loading { display: flex; flex-direction: column; gap: 10px; }
.shimmer-row {
  height: 52px;
  border-radius: var(--nx-radius);
  background: linear-gradient(90deg, var(--nx-surface-2) 25%, var(--nx-surface-3) 50%, var(--nx-surface-2) 75%);
  background-size: 200% 100%;
  animation: shimmer 1.4s infinite;
}
@keyframes shimmer { 0% { background-position: 200% 0 } 100% { background-position: -200% 0 } }

/* ── Group ─────────────────────────────────────────────────────────────── */
.rel-group {
  border: 1px solid var(--nx-border);
  border-radius: var(--nx-radius);
  overflow: hidden;
  background: var(--nx-surface-1);
}

.rel-group__header {
  display: flex;
  align-items: center;
  gap: 12px;
  width: 100%;
  padding: 12px 16px;
  background: var(--nx-surface-2);
  border: none;
  cursor: pointer;
  text-align: left;
  color: inherit;
  border-bottom: 1px solid var(--nx-border);
  transition: background 120ms;
}
.rel-group__header:hover { background: var(--nx-surface-3); }
.rel-group--collapsed .rel-group__header { border-bottom: none; }

.rel-group__icon {
  width: 34px;
  height: 34px;
  border-radius: 7px;
  background: var(--nx-accent-muted);
  color: var(--nx-accent);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 15px;
  flex-shrink: 0;
}
.rel-group__info {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.rel-group__name {
  font-size: 14px;
  font-weight: 600;
  color: var(--nx-text-primary);
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.rel-group__url {
  font-size: 11px;
  color: var(--nx-text-muted);
  font-family: "JetBrains Mono", monospace;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.rel-group__count {
  font-size: 12px;
  font-weight: 700;
  color: var(--nx-text-muted);
  background: var(--nx-surface-3);
  border-radius: 99px;
  padding: 2px 9px;
  white-space: nowrap;
}
.rel-group__chevron {
  color: var(--nx-text-muted);
  font-size: 12px;
  flex-shrink: 0;
  transition: transform 200ms;
}

/* ── Nodes ─────────────────────────────────────────────────────────────── */
.rel-group__nodes { display: flex; flex-direction: column; }

.rel-node-wrap {
  border-bottom: 1px solid var(--nx-border);
}
.rel-node-wrap:last-child { border-bottom: none; }

/* Clickable row */
.rel-node__row {
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  padding: 10px 16px;
  background: transparent;
  border: none;
  cursor: pointer;
  text-align: left;
  color: inherit;
  transition: background 120ms;
  min-height: 44px; /* touch target */
  flex-wrap: wrap;
}
.rel-node__row:hover { background: var(--nx-surface-2); }
.rel-node__row--expanded { background: var(--nx-surface-2); }

/* Tree indent */
.rel-node__indent {
  display: flex;
  align-items: center;
  gap: 0;
  flex-shrink: 0;
}
.rel-node__pipe {
  display: inline-block;
  width: 20px;
  height: 20px;
  border-left: 1px solid var(--nx-border);
  margin-left: 8px;
  flex-shrink: 0;
}
.rel-node__expander {
  font-size: 11px;
  color: var(--nx-text-muted);
  width: 16px;
  flex-shrink: 0;
  transition: transform 200ms;
}

.rel-node__name {
  font-size: 14px;
  font-weight: 500;
  color: var(--nx-text-primary);
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rel-node__ref {
  font-size: 11px;
  color: var(--nx-accent);
  font-family: "JetBrains Mono", monospace;
  background: var(--nx-accent-muted);
  padding: 1px 7px;
  border-radius: 99px;
  white-space: nowrap;
}
.rel-node__fork-label {
  font-size: 10px;
  color: var(--nx-state-restored);
  background: rgba(167,139,250,.12);
  padding: 1px 6px;
  border-radius: 99px;
  font-weight: 600;
  white-space: nowrap;
}
.rel-node__ports-summary {
  display: inline-flex;
  align-items: center;
  gap: 3px;
  font-size: 11px;
  color: var(--nx-accent);
  background: var(--nx-accent-muted);
  padding: 1px 7px;
  border-radius: 99px;
  white-space: nowrap;
}
.rel-node__spinner {
  color: var(--nx-text-muted);
  font-size: 13px;
  animation: spin 1s linear infinite;
}
@keyframes spin { to { transform: rotate(360deg); } }

/* ── Detail panel ──────────────────────────────────────────────────────── */
.rel-node__detail {
  padding: 14px 16px 16px;
  border-top: 1px solid var(--nx-border);
  background: var(--nx-bg);
  display: flex;
  flex-direction: column;
  gap: 14px;
}

/* Worktree path — prominent */
.detail-row--path {
  background: var(--nx-surface-2);
  border: 1px solid var(--nx-border);
  border-radius: var(--nx-radius-sm);
  padding: 8px 12px;
  display: flex;
  align-items: flex-start;
  gap: 10px;
  flex-wrap: wrap;
}
.detail-row--path .detail-label {
  display: flex;
  align-items: center;
  gap: 5px;
  white-space: nowrap;
  font-size: 12px;
  font-weight: 700;
  color: var(--nx-text-secondary);
  text-transform: uppercase;
  letter-spacing: .05em;
}
.detail-row--path .detail-value {
  font-size: 13px;
  color: var(--nx-text-primary);
  word-break: break-all;
}

/* Grid of meta rows */
.detail-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(220px, 1fr));
  gap: 8px 16px;
}
.detail-row {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.detail-label {
  font-size: 10px;
  font-weight: 700;
  color: var(--nx-text-muted);
  text-transform: uppercase;
  letter-spacing: .07em;
}
.detail-value {
  font-size: 13px;
  color: var(--nx-text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.detail-value--mono {
  font-family: "JetBrains Mono", "Fira Code", monospace;
  font-size: 12px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

/* Ports section */
.detail-section { display: flex; flex-direction: column; gap: 8px; }
.detail-section-title {
  font-size: 10px;
  font-weight: 700;
  color: var(--nx-text-muted);
  text-transform: uppercase;
  letter-spacing: .07em;
}
.ports-table {
  width: 100%;
  border-collapse: collapse;
  border: 1px solid var(--nx-border);
  border-radius: var(--nx-radius-sm);
  overflow: hidden;
}
.ports-table th,
.ports-table td {
  text-align: left;
  padding: 7px 9px;
  font-size: 12px;
  border-bottom: 1px solid var(--nx-border);
}
.ports-table th {
  font-size: 11px;
  color: var(--nx-text-muted);
  text-transform: uppercase;
  letter-spacing: .04em;
  background: var(--nx-surface-2);
}
.ports-table tr:last-child td {
  border-bottom: none;
}
.ports-table__empty {
  color: var(--nx-text-muted);
  text-align: center;
}

.ports-add {
  margin-top: 8px;
  display: grid;
  grid-template-columns: 92px 92px 1fr 1fr auto;
  gap: 8px;
  align-items: center;
}

.ports-add__input {
  width: 100%;
}

.ports-add__input--sm {
  max-width: 92px;
}

/* Actions row */
.detail-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  padding-top: 2px;
}

/* ── Transitions ───────────────────────────────────────────────────────── */
.group-collapse-enter-active,
.group-collapse-leave-active { transition: max-height 220ms ease, opacity 200ms; overflow: hidden; }
.group-collapse-enter-from,
.group-collapse-leave-to { max-height: 0; opacity: 0; }
.group-collapse-enter-to,
.group-collapse-leave-from { max-height: 2000px; opacity: 1; }

.node-expand-enter-active,
.node-expand-leave-active { transition: max-height 200ms ease, opacity 180ms; overflow: hidden; }
.node-expand-enter-from,
.node-expand-leave-to { max-height: 0; opacity: 0; }
.node-expand-enter-to,
.node-expand-leave-from { max-height: 800px; opacity: 1; }

/* ── Mobile ────────────────────────────────────────────────────────────── */
@media (max-width: 600px) {
  .rel-group__header { padding: 10px 12px; gap: 10px; }
  .rel-node__row { padding: 10px 12px; gap: 8px; }
  .rel-node__detail { padding: 12px; }
  .detail-grid { grid-template-columns: 1fr 1fr; }
  .detail-actions { gap: 6px; }
  .rel-group__url { display: none; }
}

/* ── Confirm remove ────────────────────────────────────────────────────── */
.confirm-label {
  font-size: 12px;
  font-weight: 600;
  color: var(--nx-state-stopped, #f87171);
  display: flex;
  align-items: center;
  white-space: nowrap;
}

@media (max-width: 900px) {
  .ports-add {
    grid-template-columns: 1fr 1fr;
  }
}
</style>
