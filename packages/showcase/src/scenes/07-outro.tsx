import React from "react";
import { AbsoluteFill, interpolate, spring, useCurrentFrame, useVideoConfig } from "remotion";


const FULL_URL = "github.com/inizio/nexus";

const BADGES = [
  { label: "Open Source", color: "#89b4fa", frame: 160 },
  { label: "Go + Swift", color: "#a6e3a1", frame: 200 },
  { label: "Firecracker", color: "#cba6f7", frame: 240 },
];

export const OutroScene: React.FC = () => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const f = frame;

  const titleOpacity = spring({ frame: f, fps, config: { damping: 20, stiffness: 60 } });
  const titleTranslate = interpolate(titleOpacity, [0, 1], [30, 0]);

  const charsVisible = Math.floor(Math.max(0, f - 60) * 1.5);
  const urlText = FULL_URL.slice(0, charsVisible);

  const urlOpacity = interpolate(f, [55, 70], [0, 1], {
    extrapolateLeft: "clamp",
    extrapolateRight: "clamp",
  });

  const orbOpacity = interpolate(f, [0, 40, 700, 750], [0, 0.25, 0.25, 0], {
    extrapolateRight: "clamp",
  });

  return (
    <AbsoluteFill style={{ background: "#11111b", display: "flex", alignItems: "center", justifyContent: "center" }}>
      {/* Background orb */}
      <div
        style={{
          position: "absolute",
          width: 700,
          height: 700,
          borderRadius: "50%",
          background: "radial-gradient(circle, #89b4fa 0%, #cba6f7 40%, transparent 70%)",
          opacity: orbOpacity,
          filter: "blur(100px)",
        }}
      />

      <div style={{ textAlign: "center", position: "relative", zIndex: 1, display: "flex", flexDirection: "column", alignItems: "center", gap: 28 }}>
        {/* Main tagline */}
        <div
          style={{
            fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
            fontSize: 58,
            fontWeight: 800,
            color: "#cdd6f4",
            letterSpacing: -2,
            lineHeight: 1.2,
            opacity: titleOpacity,
            transform: `translateY(${titleTranslate}px)`,
          }}
        >
          Build anywhere.
          <br />
          Run anywhere.
        </div>

        {/* GitHub URL with typing effect */}
        <div style={{ opacity: urlOpacity, display: "flex", alignItems: "center", gap: 6 }}>
          <span style={{ color: "#6c7086", fontFamily: "'JetBrains Mono', monospace", fontSize: 20 }}>
            github.com/
          </span>
          <span style={{ color: "#89b4fa", fontFamily: "'JetBrains Mono', monospace", fontSize: 20, fontWeight: 600 }}>
            {urlText.replace("github.com/", "")}
          </span>
          {charsVisible < FULL_URL.length && (
            <span
              style={{
                display: "inline-block",
                width: 10,
                height: "1.1em",
                background: "#cdd6f4",
                verticalAlign: "middle",
                opacity: Math.floor(f / 8) % 2 === 0 ? 1 : 0,
              }}
            />
          )}
        </div>

        {/* Badges */}
        <div style={{ display: "flex", gap: 16, marginTop: 8 }}>
          {BADGES.map((b) => {
            const op = interpolate(f, [b.frame, b.frame + 20], [0, 1], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
            });
            const ty = interpolate(f, [b.frame, b.frame + 20], [10, 0], {
              extrapolateLeft: "clamp",
              extrapolateRight: "clamp",
            });
            return (
              <div
                key={b.label}
                style={{
                  background: `${b.color}1a`,
                  border: `1px solid ${b.color}44`,
                  borderRadius: 20,
                  padding: "6px 18px",
                  color: b.color,
                  fontFamily: "sans-serif",
                  fontSize: 14,
                  fontWeight: 600,
                  opacity: op,
                  transform: `translateY(${ty}px)`,
                }}
              >
                {b.label}
              </div>
            );
          })}
        </div>
      </div>
    </AbsoluteFill>
  );
};
