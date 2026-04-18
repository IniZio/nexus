import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { TerminalWindow, TerminalLine } from "../components/TerminalWindow";


const TERMINAL_LINES: TerminalLine[] = [
  { type: "command", text: "ssh linuxbox", startFrame: 10 },
  { type: "output", text: "Welcome to linuxbox (Ubuntu 24.04)", startFrame: 40 },
  { type: "output", text: "Last login: Fri Apr 18 09:14:22 2026", startFrame: 50 },
  { type: "blank", text: "", startFrame: 60 },
  { type: "command", text: "cd ~/project && code .", startFrame: 70 },
  { type: "output", text: "Starting VS Code tunnel...", startFrame: 120 },
  { type: "output", text: "⚠  Port 3000 not forwarded — configure manually", startFrame: 140, color: "#f9e2af" },
  { type: "output", text: "⚠  AWS credentials not found in remote env", startFrame: 160, color: "#f9e2af" },
  { type: "output", text: "✗ Environment differs from local machine", startFrame: 180, color: "#f38ba8" },
];

const PAIN_POINTS = [
  { label: "Port forwarding", detail: "manually set up every session", frame: 60 },
  { label: "Credential syncing", detail: "secrets don't travel with you", frame: 110 },
  { label: "Environment drift", detail: "remote ≠ local, always", frame: 160 },
  { label: "Context switching", detail: "SSH ↔ local kills flow", frame: 210 },
];

export const ProblemScene: React.FC = () => {
  const frame = useCurrentFrame();
  const f = frame;

  return (
    <AbsoluteFill
      style={{
        background: "#11111b",
        display: "flex",
        flexDirection: "row",
        padding: 90,
        gap: 50,
        alignItems: "stretch",
      }}
    >
      {/* Left: terminal */}
      <div style={{ flex: 6 }}>
        <div style={{ color: "#a6adc8", fontSize: 20, fontFamily: "sans-serif", marginBottom: 16, letterSpacing: 2, textTransform: "uppercase", opacity: 0.6 }}>
          The old way
        </div>
        <TerminalWindow title="ssh session" lines={TERMINAL_LINES} frame={f} height={720} />
      </div>

      {/* Right: pain points */}
      <div style={{ flex: 4, display: "flex", flexDirection: "column", justifyContent: "center", gap: 24 }}>
        <div style={{ color: "#f38ba8", fontFamily: "sans-serif", fontSize: 35, fontWeight: 700, marginBottom: 12 }}>
          The friction
        </div>
        {PAIN_POINTS.map((p) => {
          const opacity = interpolate(f, [p.frame, p.frame + 20], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
          const translateY = interpolate(f, [p.frame, p.frame + 20], [16, 0], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
          return (
            <div key={p.label} style={{ opacity, transform: `translateY(${translateY}px)` }}>
              <div style={{ color: "#cdd6f4", fontFamily: "sans-serif", fontSize: 28, fontWeight: 600 }}>
                {p.label}
              </div>
              <div style={{ color: "#6c7086", fontFamily: "sans-serif", fontSize: 22, marginTop: 4 }}>
                {p.detail}
              </div>
            </div>
          );
        })}
      </div>
    </AbsoluteFill>
  );
};
