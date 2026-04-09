<script setup lang="ts">
import { onMounted, ref } from "vue";
import { useI18n } from "vue-i18n";
import Button from "primevue/button";
import InputText from "primevue/inputtext";
import RelationsView from "./components/RelationsView.vue";
import ActivityFeed from "./components/ActivityFeed.vue";
import XTermPanel from "./terminal/XTermPanel.vue";
import {
  listRelations,
  listWorkspaces,
  createWorkspace,
  forkWorkspace,
  pauseWorkspace,
  resumeWorkspace,
  startWorkspace,
  stopWorkspace,
  exposeSpotlight,
  closeSpotlight,
  listSpotlight,
  removeWorkspace,
  restoreWorkspace,
  pickDirectory,
} from "./nexus-api";
import type { SpotlightForward, WorkspaceRelationsGroup } from "@nexus/sdk";

const { t } = useI18n();

// ── Navigation ──────────────────────────────────────────────────────────
type Tab = "workspaces" | "activity";
const activeTab = ref<Tab>("workspaces");

const navItems: { id: Tab; icon: string; label: string }[] = [
  { id: "workspaces", icon: "pi-sitemap",  label: "Workspaces" },
  { id: "activity",   icon: "pi-list",     label: "Activity"   },
];

// ── State ────────────────────────────────────────────────────────────────
const loading       = ref(false);
const relations     = ref<WorkspaceRelationsGroup[]>([]);
const spotlightByWs = ref<Record<string, SpotlightForward[]>>({});
const logs          = ref<string[]>([]);
const pendingAction = ref<Record<string, string | null>>({});
const selectedWorkspaceId = ref("");
type TerminalTab = { workspaceId: string; workspaceName: string; backend: string };
const terminalTabs = ref<TerminalTab[]>([]);
const activeTerminalWorkspaceId = ref("");
const workspaceBackendById = ref<Record<string, string>>({});

function backendLabel(backend: string) {
  const normalized = (backend || "local").trim().toLowerCase();
  if (normalized === "firecracker") return "Firecracker VM";
  if (normalized === "seatbelt") return "Seatbelt runtime";
  if (normalized === "lxc") return "LXC runtime";
  return "Host local";
}

// ── Create form ──────────────────────────────────────────────────────────
const showCreate = ref(false);
const createForm = ref({ repo: "", ref: "main", workspaceName: "", agentProfile: "default" });
const createBusy = ref(false);
const createError = ref("");
const repoPickerBusy = ref(false);

function deriveWorkspaceName(repoPath: string): string {
  const normalized = repoPath.trim().replace(/\\/g, "/").replace(/\/+$/, "");
  if (!normalized) return "";
  const segments = normalized.split("/").filter(Boolean);
  const base = (segments[segments.length - 1] || normalized)
    .replace(/\.git$/, "")
    .replace(/[^a-zA-Z0-9-_]/g, "-")
    .toLowerCase();
  return base || "workspace";
}

function onRepoChanged() {
  createError.value = "";
  createForm.value.workspaceName = deriveWorkspaceName(createForm.value.repo);
}

async function chooseRepoPath() {
  repoPickerBusy.value = true;
  try {
    const picked = await pickDirectory("Select repository folder");
    if (!picked.cancelled && picked.path) {
      createForm.value.repo = picked.path;
      createForm.value.workspaceName = deriveWorkspaceName(picked.path);
    }
  } catch (error) {
    createError.value = (error as Error).message || "failed to open folder picker";
  } finally {
    repoPickerBusy.value = false;
  }
}

// ── Log helper ───────────────────────────────────────────────────────────
function logLine(line: string) {
  logs.value = [`[${new Date().toISOString()}] ${line}`, ...logs.value].slice(0, 300);
}

// ── Data ─────────────────────────────────────────────────────────────────
async function refreshAll() {
  loading.value = true;
  try {
    const workspaces = await listWorkspaces();
    workspaceBackendById.value = Object.fromEntries(
      workspaces.map((ws) => [ws.id, ws.backend || "local"]),
    );
    relations.value  = await listRelations();
    if (!selectedWorkspaceId.value && workspaces.length > 0) {
      selectedWorkspaceId.value = workspaces[0].id;
    }
    if (selectedWorkspaceId.value && !workspaces.find((ws) => ws.id === selectedWorkspaceId.value)) {
      selectedWorkspaceId.value = workspaces[0]?.id || "";
    }
    const nextSpotlight: typeof spotlightByWs.value = {};
    for (const ws of workspaces) {
      nextSpotlight[ws.id] = await listSpotlight(ws.id);
    }
    spotlightByWs.value = nextSpotlight;
    logLine(t("log.refreshed"));
  } catch (error) {
    logLine(`${t("log.refreshFailed")}: ${(error as Error).message}`);
  } finally {
    loading.value = false;
  }
}

async function onCreate() {
  if (!createForm.value.repo || !createForm.value.workspaceName) {
    createError.value = "Repo path and workspace name are required.";
    return;
  }
  createBusy.value = true;
  createError.value = "";
  try {
    await createWorkspace(createForm.value);
    logLine(`registered ${createForm.value.workspaceName}`);
    createForm.value = { repo: "", ref: "main", workspaceName: "", agentProfile: "default" };
    showCreate.value = false;
    await refreshAll();
  } catch (error) {
    createError.value = (error as Error).message;
    logLine(`register failed: ${(error as Error).message}`);
  } finally {
    createBusy.value = false;
  }
}

async function runAction(kind: string, wsId: string, wsName: string, extra?: Record<string, string>) {
  if (kind === "terminal-open") {
    const existing = terminalTabs.value.find((tab) => tab.workspaceId === wsId);
    const backend = workspaceBackendById.value[wsId] || "local";
    if (!existing) {
      terminalTabs.value.push({ workspaceId: wsId, workspaceName: wsName, backend });
    } else {
      existing.workspaceName = wsName;
      existing.backend = backend;
    }
    activeTerminalWorkspaceId.value = wsId;
    selectedWorkspaceId.value = wsId;
    return;
  }

  pendingAction.value[wsId] = kind;
  try {
    switch (kind) {
      case "start":              await startWorkspace(wsId);                  break;
      case "stop":               await stopWorkspace(wsId);                   break;
      case "pause":              await pauseWorkspace(wsId);                  break;
      case "resume":             await resumeWorkspace(wsId);                 break;
      case "fork": {
        const childWorkspaceName = extra?.childWorkspaceName || window.prompt("Fork workspace name", `${wsName}-fork`) || "";
        const childRef = extra?.childRef || window.prompt("Fork branch name", `${wsName}-fork`) || "";
        if (!childWorkspaceName.trim() || !childRef.trim()) {
          throw new Error("Fork requires workspace name and new branch name.");
        }
        await forkWorkspace(wsId, childWorkspaceName.trim(), childRef.trim());
        break;
      }
      case "remove":             await removeWorkspace(wsId);                 break;
      case "restore":            await restoreWorkspace(wsId);                break;
      case "spotlight-add": {
        const remotePort = Number(extra?.remotePort || "0");
        const localPort = Number(extra?.localPort || "0");
        if (!remotePort || !localPort) {
          throw new Error("Both remote and local ports are required.");
        }
        await exposeSpotlight({
          workspaceId: wsId,
          service: extra?.service || "",
          remotePort,
          localPort,
          host: extra?.host || "127.0.0.1",
        });
        break;
      }
      case "spotlight-remove": {
        const id = extra?.id || "";
        if (!id) {
          throw new Error("Forward id is required.");
        }
        await closeSpotlight(id);
        break;
      }
    }
    logLine(`${kind} ok · ${wsName}`);
    await refreshAll();
  } catch (error) {
    logLine(`${kind} failed · ${wsName}: ${(error as Error).message}`);
  } finally {
    pendingAction.value[wsId] = null;
  }
}

function toggleMobileSidebar() {
  (window.document.querySelector(".sidebar") as HTMLElement | null)?.classList.toggle("sidebar--open");
}

function closeTerminalTab(workspaceId: string) {
  terminalTabs.value = terminalTabs.value.filter((tab) => tab.workspaceId !== workspaceId);
  if (activeTerminalWorkspaceId.value === workspaceId) {
    activeTerminalWorkspaceId.value = terminalTabs.value[terminalTabs.value.length - 1]?.workspaceId || "";
  }
}

onMounted(refreshAll);
</script>

<template>
  <div class="app-shell">

    <!-- ── Sidebar ──────────────────────────────────────────────────── -->
    <aside class="sidebar">
      <div class="sidebar__brand">
        <span class="sidebar__logo">
          <i class="pi pi-server" />
        </span>
        <span class="sidebar__wordmark">Nexus</span>
      </div>

      <nav class="sidebar__nav">
        <button
          v-for="item in navItems"
          :key="item.id"
          class="nav-item"
          :class="{ 'nav-item--active': activeTab === item.id }"
          @click="activeTab = item.id"
        >
          <i :class="['pi', item.icon]" />
          <span class="nav-item__label">{{ item.label }}</span>
        </button>
      </nav>
    </aside>

    <!-- ── Main ─────────────────────────────────────────────────────── -->
    <div class="main">

      <!-- Topbar -->
      <header class="topbar">
        <div class="topbar__left">
          <!-- Mobile sidebar toggle -->
          <button class="mobile-menu-btn" @click="toggleMobileSidebar">
            <i class="pi pi-bars" />
          </button>
          <h1 class="topbar__title">
            {{ navItems.find(n => n.id === activeTab)?.label }}
          </h1>
        </div>
        <div class="topbar__right">
          <Button
            icon="pi pi-refresh"
            severity="secondary"
            size="small"
            :loading="loading"
            aria-label="Refresh"
            @click="refreshAll"
          />
          <Button
            v-if="activeTab === 'workspaces'"
            icon="pi pi-plus"
            label="Register"
            size="small"
            @click="showCreate = !showCreate"
          />
        </div>
      </header>

      <Transition name="quake-terminal">
        <section v-if="terminalTabs.length" class="quake-terminal">
          <div class="quake-terminal__header">
            <div class="quake-tabs">
              <button
                v-for="tab in terminalTabs"
                :key="tab.workspaceId"
                class="quake-tab"
                :class="{ 'quake-tab--active': activeTerminalWorkspaceId === tab.workspaceId }"
                @click="activeTerminalWorkspaceId = tab.workspaceId"
              >
                <span class="quake-tab__label">{{ tab.workspaceName }}</span>
                <span class="quake-tab__backend" :class="`quake-tab__backend--${tab.backend || 'local'}`">{{ backendLabel(tab.backend) }}</span>
                <span
                  class="quake-tab__close"
                  @click.stop="closeTerminalTab(tab.workspaceId)"
                >
                  <i class="pi pi-times" />
                </span>
              </button>
            </div>
          </div>
          <XTermPanel
            v-if="activeTerminalWorkspaceId"
            :key="activeTerminalWorkspaceId"
            :workspace-id="activeTerminalWorkspaceId"
          />
        </section>
      </Transition>

      <!-- Register workspace slide-in -->
      <Transition name="slide-down">
        <section v-if="showCreate && activeTab === 'workspaces'" class="create-panel" aria-label="Register workspace">
          <div class="create-panel__header">
            <span class="create-panel__title">Register Workspace</span>
            <span class="create-panel__subtitle">
              Enter a repo path (absolute or relative to daemon working directory).
            </span>
          </div>
          <div class="create-panel__body">
            <!-- Primary: explicit path input -->
            <div class="form-field form-field--primary">
              <label for="cf-repo">
                <i class="pi pi-folder-open" />
                Repo path
              </label>
              <div class="repo-path-row">
                <InputText
                  id="cf-repo"
                  v-model="createForm.repo"
                  placeholder="e.g. .case-studies/hanlun-lms"
                  @input="onRepoChanged"
                  @keydown.enter="onCreate"
                />
                <Button
                  label="Browse"
                  icon="pi pi-folder-open"
                  severity="secondary"
                  :loading="repoPickerBusy"
                  @click="chooseRepoPath"
                />
              </div>
            </div>
            <!-- Secondary fields row -->
            <div class="create-panel__secondary">
              <div class="form-field">
                <label for="cf-name">Workspace name</label>
                <InputText
                  id="cf-name"
                  v-model="createForm.workspaceName"
                  placeholder="auto-derived"
                  @keydown.enter="onCreate"
                />
              </div>
              <div class="form-field">
                <label for="cf-ref">Ref / branch</label>
                <InputText id="cf-ref" v-model="createForm.ref" placeholder="main" @keydown.enter="onCreate" />
              </div>
              <div class="form-field">
                <label for="cf-profile">Agent profile</label>
                <InputText id="cf-profile" v-model="createForm.agentProfile" placeholder="default" @keydown.enter="onCreate" />
              </div>
            </div>
            <!-- Error -->
            <div v-if="createError" class="create-panel__error">
              <i class="pi pi-exclamation-circle" />
              {{ createError }}
            </div>
          </div>
          <div class="create-panel__actions">
            <Button label="Register" icon="pi pi-check" :loading="createBusy" @click="onCreate" />
            <Button label="Cancel" severity="secondary" @click="showCreate = false; createError = ''" />
          </div>
        </section>
      </Transition>

      <!-- Page content -->
      <div class="content" :class="{ 'content--with-quake': terminalTabs.length > 0 }">

        <!-- Workspaces (Relations view) -->
        <div v-if="activeTab === 'workspaces'" class="content-pane">
          <RelationsView
            :relations="relations"
            :spotlight-by-ws="spotlightByWs"
            :pending-action="pendingAction"
            :loading="loading"
            @action="(kind, wsId, wsName, extra) => runAction(kind, wsId, wsName, extra)"
            @create="showCreate = true"
          />
        </div>

        <!-- Activity -->
        <div v-else-if="activeTab === 'activity'" class="content-pane">
          <div class="activity-header">
            <span class="section-label">Activity log</span>
            <Button
              v-if="logs.length"
              icon="pi pi-trash"
              severity="secondary"
              size="small"
              aria-label="Clear logs"
              @click="logs = []"
            />
          </div>
          <ActivityFeed :logs="logs" />
        </div>

      </div>
    </div>
  </div>
</template>

<style scoped>
/* ── Shell ─────────────────────────────────────────────────────────────── */
.app-shell {
  display: flex;
  min-height: 100vh;
}

/* ── Sidebar ───────────────────────────────────────────────────────────── */
.sidebar {
  width: 220px;
  flex-shrink: 0;
  background: var(--nx-surface-1);
  border-right: 1px solid var(--nx-border);
  display: flex;
  flex-direction: column;
  position: sticky;
  top: 0;
  height: 100vh;
  overflow-y: auto;
  z-index: 100;
}

.sidebar__brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 18px 16px 16px;
  border-bottom: 1px solid var(--nx-border);
  flex-shrink: 0;
}
.sidebar__logo {
  width: 30px;
  height: 30px;
  border-radius: 7px;
  background: linear-gradient(135deg, #1d4ed8, #3b82f6);
  display: flex;
  align-items: center;
  justify-content: center;
  color: #fff;
  font-size: 14px;
  flex-shrink: 0;
}
.sidebar__wordmark {
  font-size: 15px;
  font-weight: 700;
  color: var(--nx-text-primary);
  letter-spacing: -.015em;
}

.sidebar__nav {
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 10px 8px;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 10px;
  border-radius: 6px;
  border: none;
  background: transparent;
  color: var(--nx-text-secondary);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  width: 100%;
  text-align: left;
  transition: background 120ms, color 120ms;
  min-height: 40px; /* touch target */
}
.nav-item:hover { background: var(--nx-surface-3); color: var(--nx-text-primary); }
.nav-item--active { background: var(--nx-accent-muted); color: var(--nx-accent); }
.nav-item .pi { font-size: 14px; flex-shrink: 0; }

/* ── Main ──────────────────────────────────────────────────────────────── */
.main {
  flex: 1;
  display: flex;
  flex-direction: column;
  min-width: 0;
  overflow: hidden;
}

/* ── Topbar ────────────────────────────────────────────────────────────── */
.topbar {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding: 12px 20px;
  background: var(--nx-surface-1);
  border-bottom: 1px solid var(--nx-border);
  position: sticky;
  top: 0;
  z-index: 10;
  flex-shrink: 0;
}
.topbar__left { display: flex; align-items: center; gap: 10px; }
.topbar__title {
  margin: 0;
  font-size: 16px;
  font-weight: 700;
  color: var(--nx-text-primary);
  letter-spacing: -.015em;
}
.topbar__right { display: flex; gap: 8px; align-items: center; }

.mobile-menu-btn {
  display: none;
  background: transparent;
  border: none;
  color: var(--nx-text-secondary);
  cursor: pointer;
  padding: 6px;
  border-radius: 5px;
  font-size: 16px;
}
.mobile-menu-btn:hover { background: var(--nx-surface-3); color: var(--nx-text-primary); }

/* ── Create panel ──────────────────────────────────────────────────────── */
.create-panel {
  background: var(--nx-surface-1);
  border-bottom: 1px solid var(--nx-border);
  padding: 16px 20px;
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.create-panel__header {
  display: flex;
  flex-direction: column;
  gap: 3px;
}
.create-panel__title {
  font-size: 13px;
  font-weight: 700;
  color: var(--nx-text-primary);
}
.create-panel__subtitle {
  font-size: 12px;
  color: var(--nx-text-muted);
  line-height: 1.5;
}
.create-panel__body {
  display: flex;
  flex-direction: column;
  gap: 10px;
}
.create-panel__secondary {
  display: grid;
  grid-template-columns: 1.5fr 1fr 1fr;
  gap: 10px;
}

.repo-path-row {
  display: grid;
  grid-template-columns: 1fr auto;
  gap: 8px;
  align-items: center;
}

.create-panel__error {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--nx-state-stopped);
  background: #fee2e2;
  border: 1px solid #fca5a5;
  border-radius: var(--nx-radius-sm);
  padding: 6px 10px;
}
.form-field { display: flex; flex-direction: column; gap: 4px; }
.form-field--primary { }
.form-field label {
  font-size: 11px;
  font-weight: 600;
  color: var(--nx-text-muted);
  display: flex;
  align-items: center;
  gap: 4px;
}
.form-field label .pi { font-size: 11px; }
.create-panel__actions { display: flex; gap: 8px; }

.slide-down-enter-active,
.slide-down-leave-active { transition: max-height 240ms ease, opacity 200ms; overflow: hidden; }
.slide-down-enter-from, .slide-down-leave-to { max-height: 0; opacity: 0; }
.slide-down-enter-to, .slide-down-leave-from { max-height: 420px; opacity: 1; }

/* ── Content ───────────────────────────────────────────────────────────── */
.content {
  flex: 1;
  overflow-y: auto;
  padding: 20px;
}

.content--with-quake {
  padding-top: 470px;
}
.content-pane { /* default full height */ }

.quake-terminal {
  position: fixed;
  top: 58px;
  left: 220px;
  right: 0;
  z-index: 120;
  background: #0f172a;
  border-bottom: 1px solid #1e293b;
  box-shadow: 0 14px 40px rgba(2, 6, 23, .45);
  padding: 10px 20px 14px;
}

.quake-terminal__header {
  display: block;
  margin-bottom: 8px;
}

.quake-tabs {
  display: flex;
  align-items: center;
  gap: 6px;
  overflow-x: auto;
  padding-bottom: 4px;
}

.quake-tab {
  border: 1px solid #334155;
  background: #0b1220;
  color: #94a3b8;
  border-radius: 8px;
  padding: 4px 8px;
  display: inline-flex;
  align-items: center;
  gap: 8px;
  font-size: 12px;
  cursor: pointer;
}

.quake-tab--active {
  border-color: #2563eb;
  color: #dbeafe;
  background: rgba(37, 99, 235, .18);
}

.quake-tab__label {
  max-width: 220px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}

.quake-tab__backend {
  font-size: 10px;
  line-height: 1;
  border-radius: 999px;
  border: 1px solid;
  padding: 2px 6px;
}

.quake-tab__backend--firecracker {
  color: #bfdbfe;
  border-color: #3b82f6;
  background: rgba(59, 130, 246, .18);
}

.quake-tab__backend--lxc {
  color: #a7f3d0;
  border-color: #10b981;
  background: rgba(16, 185, 129, .18);
}

.quake-tab__backend--local {
  color: #fca5a5;
  border-color: #f97316;
  background: rgba(249, 115, 22, .18);
}

.quake-tab__close {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border-radius: 4px;
}

.quake-tab__close:hover {
  background: rgba(148, 163, 184, .18);
}

.quake-terminal-enter-active,
.quake-terminal-leave-active {
  transition: transform 180ms ease, opacity 180ms ease;
}

.quake-terminal-enter-from,
.quake-terminal-leave-to {
  transform: translateY(-12px);
  opacity: 0;
}

/* Activity header */
.activity-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 12px;
}
.section-label {
  font-size: 11px;
  font-weight: 700;
  color: var(--nx-text-muted);
  text-transform: uppercase;
  letter-spacing: .08em;
}

/* ── Mobile ────────────────────────────────────────────────────────────── */
@media (max-width: 768px) {
  .sidebar {
    position: fixed;
    left: -220px;
    top: 0;
    height: 100vh;
    transition: left 220ms ease;
    box-shadow: 4px 0 24px rgba(0,0,0,.15);
  }
  .sidebar--open { left: 0; }
  .mobile-menu-btn { display: flex; }
  .content { padding: 14px; }
  .content--with-quake { padding-top: 420px; }
  .create-panel { padding: 14px; }
  .create-panel__secondary { grid-template-columns: 1fr 1fr; }
  .topbar { padding: 10px 14px; }
  .quake-terminal {
    left: 0;
    top: 52px;
    padding: 8px 12px 12px;
  }
}
@media (max-width: 480px) {
  .create-panel__secondary { grid-template-columns: 1fr; }
}
</style>
