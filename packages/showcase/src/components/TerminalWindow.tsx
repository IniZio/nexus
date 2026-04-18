import React from "react";
import { interpolate } from "remotion";

// ── Types ──────────────────────────────────────────────────────────────────

export type TerminalLineType = "command" | "output" | "blank";

export interface TerminalLine {
  type: TerminalLineType;
  text: string;
  /** Frame (relative to scene start) at which this line begins appearing */
  startFrame: number;
  /** Characters per frame for typed lines (default 2) */
  charsPerFrame?: number;
  color?: string;
}

interface TerminalWindowProps {
  title?: string;
  lines: TerminalLine[];
  /** Frame offset — pass relFrame(scene, useCurrentFrame()) from parent */
  frame: number;
  width?: number | string;
  height?: number | string;
}

// ── Component ─────────────────────────────────────────────────────────────

export const TerminalWindow: React.FC<TerminalWindowProps> = ({
  title = "terminal",
  lines,
  frame,
  width = "100%",
  height = "100%",
}) => {
  return (
    <div
      style={{
        width,
        height,
        background: "#1e1e2e",
        borderRadius: 10,
        overflow: "hidden",
        fontFamily: "'JetBrains Mono', 'Fira Code', monospace",
        fontSize: 22,
        boxShadow: "0 20px 60px rgba(0,0,0,0.6)",
        display: "flex",
        flexDirection: "column",
      }}
    >
      {/* Title bar */}
      <div
        style={{
          background: "#313244",
          padding: "8px 14px",
          display: "flex",
          alignItems: "center",
          gap: 8,
          flexShrink: 0,
        }}
      >
        <div style={{ width: 16, height: 16, borderRadius: "50%", background: "#f38ba8" }} />
        <div style={{ width: 16, height: 16, borderRadius: "50%", background: "#f9e2af" }} />
        <div style={{ width: 16, height: 16, borderRadius: "50%", background: "#a6e3a1" }} />
        <span style={{ color: "#cdd6f4", fontSize: 16, marginLeft: 8, opacity: 0.7 }}>{title}</span>
      </div>

      {/* Content */}
      <div style={{ flex: 1, padding: "16px 20px", overflowY: "hidden" }}>
        {lines.map((line, i) => (
          <TerminalLineView key={i} line={line} frame={frame} />
        ))}
      </div>
    </div>
  );
};

// ── Line renderer ──────────────────────────────────────────────────────────

const TerminalLineView: React.FC<{ line: TerminalLine; frame: number }> = ({ line, frame }) => {
  if (frame < line.startFrame) return null;

  const elapsed = frame - line.startFrame;

  if (line.type === "blank") {
    return <div style={{ height: 8 }} />;
  }

  if (line.type === "command") {
    const cpr = line.charsPerFrame ?? 2;
    const charsVisible = Math.floor(elapsed * cpr);
    const text = line.text.slice(0, charsVisible);
    const showCursor = charsVisible < line.text.length;

    return (
      <div style={{ lineHeight: "1.8", color: "#a6e3a1" }}>
        <span style={{ color: "#89b4fa" }}>$ </span>
        <span>{text}</span>
        {showCursor && (
          <span
            style={{
              display: "inline-block",
              width: 8,
              height: "1em",
              background: "#cdd6f4",
              marginLeft: 2,
              verticalAlign: "middle",
              opacity: Math.floor(elapsed / 8) % 2 === 0 ? 1 : 0,
            }}
          />
        )}
      </div>
    );
  }

  // output — fade in line
  const opacity = interpolate(elapsed, [0, 6], [0, 1], { extrapolateRight: "clamp" });
  return (
    <div
      style={{
        lineHeight: "1.8",
        color: line.color ?? "#cdd6f4",
        opacity,
        paddingLeft: 16,
        whiteSpace: "pre",
      }}
    >
      {line.text}
    </div>
  );
};
