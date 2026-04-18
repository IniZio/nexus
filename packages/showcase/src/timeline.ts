/**
 * Single source of truth for scene frame timestamps.
 * All scenes reference this file for their start frame and duration.
 * Total: ~6150 frames @ 30fps = ~205 seconds
 */

export const FPS = 30;

export const SCENES = {
  intro: { start: 0, duration: 150 },           // 0:00 – 0:05  (5s)  Title card
  problem: { start: 150, duration: 300 },        // 0:05 – 0:15  (10s) The problem
  architecture: { start: 450, duration: 450 },   // 0:15 – 0:30  (15s) Architecture diagram
  deploy: { start: 900, duration: 600 },         // 0:30 – 0:50  (20s) Deploy daemon to linuxbox
  createWorkspace: { start: 1500, duration: 900 }, // 0:50 – 1:20  (30s) nexus create + connect
  workspaceTypes: { start: 2400, duration: 1200 }, // 1:20 – 2:00  (40s) 3 workspace types
  liveConnect: { start: 3600, duration: 1800 },  // 2:00 – 3:00  (60s) macOS app live connect
  outro: { start: 5400, duration: 750 },         // 3:00 – 3:25  (25s) Outro / CTA
} as const;

export type SceneName = keyof typeof SCENES;

/** Total composition duration in frames */
export const TOTAL_FRAMES =
  SCENES.outro.start + SCENES.outro.duration; // 6150

/** Helper: get frame number relative to a scene's start */
export function relFrame(scene: SceneName, absFrame: number): number {
  return absFrame - SCENES[scene].start;
}

/** Easing helpers */
export function easeInOut(t: number): number {
  return t < 0.5 ? 2 * t * t : -1 + (4 - 2 * t) * t;
}

export function lerp(a: number, b: number, t: number): number {
  return a + (b - a) * t;
}
