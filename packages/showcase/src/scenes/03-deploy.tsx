import React from "react";
import { AbsoluteFill, useCurrentFrame } from "remotion";
import { TerminalWindow, TerminalLine } from "../components/TerminalWindow";


const LINES: TerminalLine[] = [
  { type: "command", text: "ssh linuxbox", startFrame: 0 },
  { type: "output", text: "Connected to linuxbox", startFrame: 40 },
  { type: "command", text: "cd ~/magic/nexus && git pull", startFrame: 70 },
  { type: "output", text: "Already up to date.", startFrame: 130 },
  { type: "command", text: "go build -o ~/magic/bin/nexusd ./cmd/daemon", startFrame: 160 },
  { type: "output", text: "Build successful", startFrame: 260 },
  { type: "command", text: "systemctl --user start nexusd", startFrame: 290 },
  { type: "output", text: "● nexusd.service - Nexus Workspace Daemon", startFrame: 340 },
  { type: "output", text: "   Active: active (running)", startFrame: 360, color: "#a6e3a1" },
  { type: "command", text: "curl http://127.0.0.1:7777/healthz", startFrame: 400 },
  { type: "output", text: '{"ok":true}', startFrame: 460, color: "#a6e3a1" },
];

export const DeployScene: React.FC = () => {
  const frame = useCurrentFrame();
  const f = frame;

  return (
    <AbsoluteFill
      style={{
        background: "#11111b",
        display: "flex",
        flexDirection: "column",
        padding: 90,
        gap: 24,
      }}
    >
      <div
        style={{
          color: "#a6adc8",
          fontSize: 20,
          fontFamily: "sans-serif",
          letterSpacing: 2,
          textTransform: "uppercase",
          opacity: 0.6,
        }}
      >
        Step 1 — Deploy the daemon
      </div>
      <TerminalWindow title="deploy nexusd" lines={LINES} frame={f} />
    </AbsoluteFill>
  );
};
