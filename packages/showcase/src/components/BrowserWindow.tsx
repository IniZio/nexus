import React from "react";

interface BrowserWindowProps {
  url?: string;
  children: React.ReactNode;
  width?: number | string;
  height?: number | string;
}

/**
 * Simulated browser chrome for showing web app previews.
 */
export const BrowserWindow: React.FC<BrowserWindowProps> = ({
  url = "http://localhost:3000",
  children,
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
        boxShadow: "0 20px 60px rgba(0,0,0,0.6)",
        display: "flex",
        flexDirection: "column",
        border: "1px solid rgba(255,255,255,0.07)",
      }}
    >
      {/* Browser toolbar */}
      <div
        style={{
          background: "#181825",
          padding: "8px 14px",
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

        {/* URL bar */}
        <div
          style={{
            flex: 1,
            background: "#313244",
            borderRadius: 6,
            padding: "4px 12px",
            marginLeft: 8,
            display: "flex",
            alignItems: "center",
            gap: 6,
          }}
        >
          <div
            style={{
              width: 8,
              height: 8,
              borderRadius: "50%",
              background: "#a6e3a1",
              flexShrink: 0,
            }}
          />
          <span
            style={{
              color: "#cdd6f4",
              fontSize: 18,
              fontFamily: "'JetBrains Mono', monospace",
              opacity: 0.8,
            }}
          >
            {url}
          </span>
        </div>
      </div>

      {/* Page content */}
      <div style={{ flex: 1, overflow: "hidden" }}>{children}</div>
    </div>
  );
};
