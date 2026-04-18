import React from "react";
import { AbsoluteFill, interpolate, spring, useCurrentFrame, useVideoConfig } from "remotion";


type Card = {
  emoji: string;
  title: string;
  description: string;
  color: string;
  details: string[];
  startFrame: number;
};

const CARDS: Card[] = [
  {
    emoji: "⚡",
    title: "Next.js webapp",
    description: "Full-stack React development environment with hot reload",
    color: "#89b4fa",
    details: ["Port: 3000", "Runtime: Node 20", "Template: webapp-nextjs", "Backend: firecracker"],
    startFrame: 30,
  },
  {
    emoji: "🐳",
    title: "Full-stack Compose",
    description: "Multi-service environment with database and cache",
    color: "#a6e3a1",
    details: ["Services: app / db / cache", "Ports: 3000 / 5432 / 6379", "Template: compose-fullstack", "Backend: firecracker"],
    startFrame: 330,
  },
  {
    emoji: "🍎",
    title: "Nexus macOS app",
    description: "Native Swift development with Xcode integration",
    color: "#cba6f7",
    details: ["Build: swift 5.10", "Runtime: macOS 14", "Template: macos-swift", "Backend: process"],
    startFrame: 630,
  },
];

export const WorkspaceTypesScene: React.FC = () => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
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
          color: "#cdd6f4",
          fontFamily: "sans-serif",
          fontSize: 40,
          fontWeight: 700,
          marginBottom: 12,
        }}
      >
        Workspace templates
      </div>
        <div style={{ display: "flex", gap: 48, flex: 1, alignItems: "stretch" }}>
        {CARDS.map((card) => {
          const sf = card.startFrame;
          const slideY = spring({ frame: Math.max(0, f - sf), fps, config: { damping: 18, stiffness: 70 } });
          const opacity = interpolate(f, [sf, sf + 20], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
          });
          const translateY = interpolate(slideY, [0, 1], [60, 0]);

          return (
            <div
              key={card.title}
              style={{
                flex: 1,
                background: "#1e1e2e",
                border: `1px solid ${card.color}33`,
                borderRadius: 16,
                padding: 48,
                display: "flex",
                flexDirection: "column",
                gap: 16,
                opacity,
                transform: `translateY(${translateY}px)`,
                boxShadow: `0 8px 40px ${card.color}1a`,
              }}
            >
              <div style={{ fontSize: 56 }}>{card.emoji}</div>
              <div style={{ color: card.color, fontFamily: "sans-serif", fontSize: 32, fontWeight: 700 }}>
                {card.title}
              </div>
              <div style={{ color: "#a6adc8", fontFamily: "sans-serif", fontSize: 20, lineHeight: 1.5, flex: 1 }}>
                {card.description}
              </div>
              <div style={{ borderTop: "1px solid rgba(255,255,255,0.06)", paddingTop: 16, display: "flex", flexDirection: "column", gap: 8 }}>
                {card.details.map((d) => (
                  <div key={d} style={{ color: "#6c7086", fontFamily: "'JetBrains Mono', monospace", fontSize: 16 }}>
                    {d}
                  </div>
                ))}
              </div>
            </div>
          );
        })}
      </div>
    </AbsoluteFill>
  );
};
