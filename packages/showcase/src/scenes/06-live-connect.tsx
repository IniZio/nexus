import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";
import { MacOSWindow } from "../components/MacOSWindow";
import { BrowserWindow } from "../components/BrowserWindow";


const PROFILES = [
  { name: "local", host: "127.0.0.1", port: "7777" },
  { name: "linuxbox", host: "192.168.1.42", port: "7777" },
];

const WORKSPACES = [
  { id: "ws-abc123", name: "webapp-nextjs", status: "running", port: "3000" },
  { id: "ws-def456", name: "api-service", status: "stopped", port: "8080" },
];

export const LiveConnectScene: React.FC = () => {
  const frame = useCurrentFrame();
  const f = frame;

  const workspacesPanelOpacity = interpolate(f, [300, 340], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  const workspacesPanelX = interpolate(f, [300, 340], [60, 0], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });

  const pulseDot = Math.sin(f * 0.15) * 0.3 + 0.7;
  const activeCardGlow = interpolate(f, [600, 640], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });

  const browserOpacity = interpolate(f, [900, 960], [0, 0.7], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  const browserY = interpolate(f, [900, 960], [40, 0], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });

  return (
    <AbsoluteFill style={{ background: "#11111b", padding: 60 }}>
      <MacOSWindow title="Nexus" width="100%" height="100%">
        <div style={{ display: "flex", height: "100%" }}>
          {/* Sidebar */}
          <div style={{ width: 280, background: "#181825", borderRight: "1px solid rgba(255,255,255,0.06)", padding: 24, flexShrink: 0 }}>
            <div style={{ color: "#6c7086", fontSize: 18, fontFamily: "sans-serif", letterSpacing: 2, textTransform: "uppercase", marginBottom: 16 }}>
              Daemons
            </div>
            {PROFILES.map((p, i) => (
              <div
                key={p.name}
                style={{
                  padding: "8px 12px",
                  borderRadius: 8,
                  marginBottom: 4,
                  background: i === 1 ? "rgba(137,180,250,0.15)" : "transparent",
                  border: i === 1 ? "1px solid rgba(137,180,250,0.3)" : "1px solid transparent",
                  cursor: "pointer",
                }}
              >
                <div style={{ color: i === 1 ? "#89b4fa" : "#a6adc8", fontFamily: "sans-serif", fontSize: 20, fontWeight: i === 1 ? 600 : 400 }}>
                  {p.name}
                </div>
                <div style={{ color: "#6c7086", fontFamily: "'JetBrains Mono', monospace", fontSize: 16, marginTop: 2 }}>
                  {p.host}:{p.port}
                </div>
              </div>
            ))}
          </div>

          {/* Main content */}
          <div style={{ flex: 1, padding: 44, display: "flex", flexDirection: "column", gap: 32 }}>
            {/* Connection info */}
            <div>
              <div style={{ color: "#6c7086", fontSize: 11, fontFamily: "sans-serif", letterSpacing: 2, textTransform: "uppercase", marginBottom: 12 }}>
                Connection
              </div>
              <div style={{ background: "#1e1e2e", borderRadius: 10, padding: 20, display: "flex", flexDirection: "column", gap: 10 }}>
                {[
                  ["Host", "192.168.1.42"],
                  ["Port", "7777"],
                  ["Token", "••••••••••••"],
                ].map(([k, v]) => (
                  <div key={k} style={{ display: "flex", gap: 16 }}>
                    <span style={{ color: "#6c7086", fontFamily: "sans-serif", fontSize: 13, width: 60 }}>{k}</span>
                    <span style={{ color: "#cdd6f4", fontFamily: "'JetBrains Mono', monospace", fontSize: 13 }}>{v}</span>
                  </div>
                ))}
                <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 4 }}>
                  <div style={{ width: 8, height: 8, borderRadius: "50%", background: "#a6e3a1" }} />
                  <span style={{ color: "#a6e3a1", fontFamily: "sans-serif", fontSize: 13, fontWeight: 600 }}>Connected</span>
                </div>
              </div>
            </div>

            {/* Workspaces panel */}
            <div style={{ opacity: workspacesPanelOpacity, transform: `translateX(${workspacesPanelX}px)` }}>
              <div style={{ color: "#6c7086", fontSize: 11, fontFamily: "sans-serif", letterSpacing: 2, textTransform: "uppercase", marginBottom: 12 }}>
                Workspaces
              </div>
              <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
                {WORKSPACES.map((ws, i) => {
                  const isActive = i === 0 && f >= 600;
                  return (
                    <div
                      key={ws.id}
                      style={{
                        background: "#1e1e2e",
                        border: isActive ? `1px solid rgba(166,227,161,${activeCardGlow * 0.5})` : "1px solid rgba(255,255,255,0.06)",
                        borderRadius: 10,
                        padding: 16,
                        display: "flex",
                        alignItems: "center",
                        gap: 12,
                        boxShadow: isActive ? `0 0 20px rgba(166,227,161,${activeCardGlow * 0.15})` : "none",
                      }}
                    >
                      <div
                        style={{
                          width: 10,
                          height: 10,
                          borderRadius: "50%",
                          background: ws.status === "running" ? "#a6e3a1" : "#585b70",
                          opacity: isActive ? pulseDot : 1,
                          flexShrink: 0,
                        }}
                      />
                      <div style={{ flex: 1 }}>
                        <div style={{ color: "#cdd6f4", fontFamily: "sans-serif", fontSize: 14, fontWeight: 600 }}>{ws.name}</div>
                        <div style={{ color: "#6c7086", fontFamily: "'JetBrains Mono', monospace", fontSize: 11, marginTop: 2 }}>{ws.id} · :{ws.port}</div>
                      </div>
                    </div>
                  );
                })}
              </div>
            </div>
          </div>
        </div>
      </MacOSWindow>

      {/* Browser overlay */}
      {f >= 900 && (
        <div
          style={{
            position: "absolute",
            top: 80,
            left: 320,
            right: 40,
            bottom: 40,
            opacity: browserOpacity,
            transform: `translateY(${browserY}px)`,
            zIndex: 10,
          }}
        >
          <BrowserWindow url="http://localhost:3000">
            <div style={{ background: "#0f0f23", height: "100%", display: "flex", alignItems: "center", justifyContent: "center", flexDirection: "column", gap: 16 }}>
              <div style={{ fontSize: 48 }}>⚡</div>
              <div style={{ color: "#cdd6f4", fontFamily: "sans-serif", fontSize: 28, fontWeight: 700 }}>webapp-nextjs</div>
              <div style={{ color: "#6c7086", fontFamily: "sans-serif", fontSize: 14 }}>Running inside Firecracker VM · forwarded to localhost:3000</div>
            </div>
          </BrowserWindow>
        </div>
      )}
    </AbsoluteFill>
  );
};
