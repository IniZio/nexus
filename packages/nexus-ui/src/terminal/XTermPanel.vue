<script setup lang="ts">
import { onMounted, onUnmounted, ref } from "vue";
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";
import { onPTYData, onPTYExit, ptyClose, ptyOpen, ptyResize, ptyWrite } from "../nexus-api";

const props = defineProps<{ workspaceId: string }>();

const container = ref<HTMLElement | null>(null);
let term: Terminal | null = null;
let fit: FitAddon | null = null;
let sessionId = "";
let unSubData: (() => void) | null = null;
let unSubExit: (() => void) | null = null;

async function openTerminal() {
  if (!term || !fit || !props.workspaceId) return;

  fit.fit();
  const cols = term.cols || 80;
  const rows = term.rows || 24;

  sessionId = await ptyOpen(props.workspaceId, cols, rows);

  unSubData = onPTYData((sid, data) => {
    if (sid !== sessionId || !term) return;
    term.write(data);
  });

  unSubExit = onPTYExit((sid, exitCode) => {
    if (sid !== sessionId || !term) return;
    term.writeln(`\r\n[process exited ${exitCode}]`);
  });

  term.onData((data) => {
    if (!sessionId) return;
    void ptyWrite(sessionId, data);
  });

  term.onResize((size) => {
    if (!sessionId) return;
    void ptyResize(sessionId, size.cols, size.rows);
  });
}

onMounted(async () => {
  if (!container.value) return;
  term = new Terminal({
    convertEol: true,
    fontFamily: "JetBrains Mono, Menlo, monospace",
    fontSize: 12,
    theme: {
      background: "#0f172a",
      foreground: "#e2e8f0",
    },
  });
  fit = new FitAddon();
  term.loadAddon(fit);
  term.open(container.value);

  if (!props.workspaceId) {
    term.writeln("No workspace selected.");
    return;
  }

  try {
    await openTerminal();
  } catch (error) {
    term.writeln(`Failed to open PTY: ${(error as Error).message}`);
  }
});

onUnmounted(() => {
  if (sessionId) {
    void ptyClose(sessionId);
    sessionId = "";
  }
  unSubData?.();
  unSubData = null;
  unSubExit?.();
  unSubExit = null;
  term?.dispose();
  term = null;
  fit = null;
});
</script>

<template>
  <div class="xterm-wrap" ref="container" />
</template>

<style scoped>
.xterm-wrap {
  width: 100%;
  height: 420px;
  border: 1px solid #334155;
  border-radius: 10px;
  overflow: hidden;
}
</style>
