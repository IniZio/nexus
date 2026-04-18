import React from "react";
import { AbsoluteFill, interpolate, useCurrentFrame } from "remotion";


type Box = { label: string; color: string; x: number; y: number; w: number; h: number };

const BOXES: Box[] = [
  { label: "Mac\n(you)", color: "#89b4fa", x: 40, y: 220, w: 160, h: 80 },
  { label: "SSH Tunnel", color: "#f9e2af", x: 280, y: 220, w: 160, h: 80 },
  { label: "Linux Daemon\n(nexusd)", color: "#a6e3a1", x: 520, y: 220, w: 180, h: 80 },
  { label: "Firecracker VM\n(workspace)", color: "#cba6f7", x: 780, y: 120, w: 180, h: 80 },
  { label: "Firecracker VM\n(workspace)", color: "#cba6f7", x: 780, y: 320, w: 180, h: 80 },
];

const ARROWS = [
  { x1: 200, y1: 260, x2: 280, y2: 260, startFrame: 30, endFrame: 80 },
  { x1: 440, y1: 260, x2: 520, y2: 260, startFrame: 80, endFrame: 130 },
  { x1: 700, y1: 230, x2: 780, y2: 185, startFrame: 130, endFrame: 180 },
  { x1: 700, y1: 290, x2: 780, y2: 365, startFrame: 130, endFrame: 180 },
];

export const ArchitectureScene: React.FC = () => {
  const frame = useCurrentFrame();
  const f = frame;

  return (
    <AbsoluteFill style={{ background: "#11111b", display: "flex", flexDirection: "column", alignItems: "center", justifyContent: "center" }}>
      <div style={{ color: "#cdd6f4", fontFamily: "sans-serif", fontSize: 42, fontWeight: 700, marginBottom: 40, letterSpacing: -0.5 }}>
        How Nexus works
      </div>

      <svg width={1000} height={520} style={{ overflow: "visible" }}>
        {/* Arrows */}
        {ARROWS.map((a, i) => {
          const progress = interpolate(f, [a.startFrame, a.endFrame], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
          });
          const x2 = a.x1 + (a.x2 - a.x1) * progress;
          const y2 = a.y1 + (a.y2 - a.y1) * progress;
          return (
            <g key={i}>
              <line x1={a.x1} y1={a.y1} x2={x2} y2={y2} stroke="#585b70" strokeWidth={2} strokeDasharray="6 4" />
              {progress >= 1 && (
                <polygon
                  points={`${a.x2},${a.y2} ${a.x2 - 10},${a.y2 - 5} ${a.x2 - 10},${a.y2 + 5}`}
                  fill="#585b70"
                  transform={`rotate(${Math.atan2(a.y2 - a.y1, a.x2 - a.x1) * 180 / Math.PI}, ${a.x2}, ${a.y2})`}
                />
              )}
            </g>
          );
        })}

        {/* Boxes */}
        {BOXES.map((b, i) => {
          const boxFrame = i * 25;
          const opacity = interpolate(f, [boxFrame, boxFrame + 20], [0, 1], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
          });
          const translateY = interpolate(f, [boxFrame, boxFrame + 20], [20, 0], {
            extrapolateLeft: "clamp",
            extrapolateRight: "clamp",
          });
          return (
            <g key={i} opacity={opacity} transform={`translate(0, ${translateY})`}>
              <rect x={b.x} y={b.y} width={b.w} height={b.h} rx={10} fill={b.color} fillOpacity={0.15} stroke={b.color} strokeWidth={1.5} />
              {b.label.split("\n").map((line, li) => (
                <text
                  key={li}
                  x={b.x + b.w / 2}
                  y={b.y + b.h / 2 + (li - (b.label.split("\n").length - 1) / 2) * 18}
                  textAnchor="middle"
                  dominantBaseline="middle"
                  fill={b.color}
                  fontSize={20}
                  fontFamily="sans-serif"
                  fontWeight={600}
                >
                  {line}
                </text>
              ))}
            </g>
          );
        })}
      </svg>

      {/* Legend */}
      <div style={{ display: "flex", gap: 32, marginTop: 20 }}>
        {[["#89b4fa", "Client"], ["#a6e3a1", "Daemon"], ["#cba6f7", "VM"]].map(([color, label]) => {
          const legendOpacity = interpolate(f, [200, 220], [0, 1], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
          return (
            <div key={label} style={{ display: "flex", alignItems: "center", gap: 8, opacity: legendOpacity }}>
              <div style={{ width: 16, height: 16, borderRadius: 3, background: color }} />
              <span style={{ color: "#a6adc8", fontFamily: "sans-serif", fontSize: 20 }}>{label}</span>
            </div>
          );
        })}
      </div>
    </AbsoluteFill>
  );
};
