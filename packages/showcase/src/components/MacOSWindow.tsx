import React from "react";

interface MacOSWindowProps {
  title?: string;
  children: React.ReactNode;
  width?: number | string;
  height?: number | string;
  accentColor?: string;
}

/**
 * Simulated macOS window chrome — use this to wrap app UI screenshots or
 * React-rendered facsimiles of the Nexus macOS app.
 */
export const MacOSWindow: React.FC<MacOSWindowProps> = ({
  title = "Nexus",
  children,
  width = "100%",
  height = "100%",
  accentColor = "#cba6f7",
}) => {
  return (
    <div
      style={{
        width,
        height,
        background: "#1e1e2e",
        borderRadius: 12,
        overflow: "hidden",
        boxShadow: "0 24px 80px rgba(0,0,0,0.7)",
        display: "flex",
        flexDirection: "column",
        border: "1px solid rgba(255,255,255,0.08)",
      }}
    >
      {/* Title bar */}
      <div
        style={{
          background: "#181825",
          padding: "10px 16px",
          display: "flex",
          alignItems: "center",
          gap: 8,
          flexShrink: 0,
          borderBottom: "1px solid rgba(255,255,255,0.06)",
        }}
      >
        <div style={{ width: 16, height: 16, borderRadius: "50%", background: "#f38ba8" }} />
        <div style={{ width: 16, height: 16, borderRadius: "50%", background: "#f9e2af" }} />
        <div style={{ width: 16, height: 16, borderRadius: "50%", background: "#a6e3a1" }} />
        <span
          style={{
            flex: 1,
            textAlign: "center",
            color: "#cdd6f4",
            fontSize: 17,
            opacity: 0.6,
            fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
            fontWeight: 500,
            marginRight: 48, // optical balance for traffic lights
          }}
        >
          {title}
        </span>
        <div
          style={{
            width: 8,
            height: 8,
            borderRadius: "50%",
            background: accentColor,
            opacity: 0.8,
          }}
        />
      </div>

      {/* Content area */}
      <div style={{ flex: 1, overflow: "hidden" }}>{children}</div>
    </div>
  );
};
