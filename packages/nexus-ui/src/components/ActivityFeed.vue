<script setup lang="ts">
defineProps<{
  logs: string[];
}>();

function levelOf(line: string): "error" | "warn" | "ok" | "info" {
  const l = line.toLowerCase();
  if (l.includes("fail") || l.includes("error")) return "error";
  if (l.includes("warn")) return "warn";
  if (l.includes(" ok") || l.includes("created") || l.includes("refresh")) return "ok";
  return "info";
}
</script>

<template>
  <div class="activity">
    <div v-if="!logs.length" class="activity__empty">No activity yet</div>
    <div
      v-for="(line, i) in logs"
      :key="i"
      class="activity__line"
      :class="`activity__line--${levelOf(line)}`"
    >
      <span class="activity__dot" />
      <span class="activity__text">{{ line }}</span>
    </div>
  </div>
</template>

<style scoped>
.activity {
  font-family: "JetBrains Mono", "Fira Code", monospace;
  font-size: 12px;
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 260px;
  overflow-y: auto;
}
.activity__empty { color: var(--nx-text-muted); padding: 8px 0; }
.activity__line {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  padding: 3px 6px;
  border-radius: 4px;
  line-height: 1.5;
}
.activity__line:hover { background: var(--nx-surface-3); }
.activity__dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  margin-top: 5px;
  flex-shrink: 0;
}
.activity__line--error .activity__dot  { background: var(--nx-state-stopped); }
.activity__line--warn  .activity__dot  { background: var(--nx-state-paused); }
.activity__line--ok    .activity__dot  { background: var(--nx-state-running); }
.activity__line--info  .activity__dot  { background: var(--nx-text-muted); }

.activity__line--error .activity__text { color: #fca5a5; }
.activity__line--warn  .activity__text { color: #fcd34d; }
.activity__line--ok    .activity__text { color: var(--nx-text-secondary); }
.activity__line--info  .activity__text { color: var(--nx-text-muted); }
</style>
