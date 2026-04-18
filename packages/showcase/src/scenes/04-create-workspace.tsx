import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { TerminalWindow, TerminalLine } from "../components/TerminalWindow";


const LINES: TerminalLine[] = [
  { type: "command", text: "nexus create --template webapp-nextjs", startFrame: 0 },
  { type: "output", text: "Creating workspace...", startFrame: 60 },
  { type: "output", text: "Provisioning Firecracker VM...", startFrame: 90 },
  { type: "output", text: "Installing dependencies...", startFrame: 130 },
  { type: "output", text: "✓ Workspace ready: ws-abc123", startFrame: 200, color: "#a6e3a1" },
  { type: "command", text: "nexus connect ws-abc123", startFrame: 250 },
  { type: "output", text: "Forwarding ports: 3000 → localhost:3000", startFrame: 310 },
  { type: "output", text: "✓ Connected", startFrame: 360, color: "#a6e3a1" },
];

export const CreateWorkspaceScene: React.FC = () => {
  const frame = useCurrentFrame();
  const f = frame;

  const cardOpacity = interpolate(f, [200, 230], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });
  const cardTranslate = interpolate(f, [200, 230], [30, 0], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });

  const statusBadgeOpacity = interpolate(f, [360, 390], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });

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
        Step 2 — Create a workspace
      </div>
      <div style={{ display: "flex", flex: 1, gap: 40 }}>
        {/* Terminal 60% */}
        <div style={{ flex: 6 }}>
          <TerminalWindow title="nexus create" lines={LINES} frame={f} />
        </div>

        {/* Status card 40% */}
        <div
          style={{
            flex: 4,
            display: "flex",
            flexDirection: "column",
            justifyContent: "center",
            gap: 16,
            opacity: cardOpacity,
            transform: `translateY(${cardTranslate}px)`,
          }}
        >
          <div
            style={{
              background: "#1e1e2e",
              border: "1px solid rgba(166,227,161,0.3)",
              borderRadius: 12,
              padding: 28,
              boxShadow: "0 8px 32px rgba(0,0,0,0.4)",
            }}
          >
            <div style={{ color: "#a6adc8", fontSize: 18, fontFamily: "sans-serif", letterSpacing: 2, textTransform: "uppercase", marginBottom: 16, opacity: 0.6 }}>
              Workspace
            </div>
            <div style={{ color: "#cdd6f4", fontFamily: "sans-serif", fontSize: 32, fontWeight: 700, marginBottom: 20 }}>
              webapp-nextjs
            </div>
            {[
              ["Status", "Running", "#a6e3a1"],
              ["ID", "ws-abc123", "#cdd6f4"],
              ["Port", "3000", "#89b4fa"],
              ["Backend", "firecracker", "#cba6f7"],
            ].map(([key, val, color]) => (
              <div key={key} style={{ display: "flex", justifyContent: "space-between", marginBottom: 10 }}>
                <span style={{ color: "#6c7086", fontFamily: "sans-serif", fontSize: 20 }}>{key}</span>
                <span style={{ color, fontFamily: "'JetBrains Mono', monospace", fontSize: 20 }}>{val}</span>
              </div>
            ))}
            <div
              style={{
                marginTop: 20,
                background: "rgba(166,227,161,0.1)",
                border: "1px solid rgba(166,227,161,0.3)",
                borderRadius: 8,
                padding: "8px 16px",
                display: "flex",
                alignItems: "center",
                gap: 8,
                opacity: statusBadgeOpacity,
              }}
            >
              <div style={{ width: 12, height: 12, borderRadius: "50%", background: "#a6e3a1" }} />
              <span style={{ color: "#a6e3a1", fontFamily: "sans-serif", fontSize: 20, fontWeight: 600 }}>
                Connected — localhost:3000
              </span>
            </div>
          </div>
        </div>
      </div>
    </AbsoluteFill>
  );
};
