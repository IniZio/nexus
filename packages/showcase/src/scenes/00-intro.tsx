import React from "react";
import { AbsoluteFill, interpolate, spring, useCurrentFrame, useVideoConfig } from "remotion";


export const IntroScene: React.FC = () => {
  const frame = useCurrentFrame();
  const { fps } = useVideoConfig();
  const f = frame;

  const titleOpacity = spring({ frame: f, fps, config: { damping: 20, stiffness: 80 } });


  const subtitleOpacity = spring({
    frame: Math.max(0, f - 30),
    fps,
    config: { damping: 20, stiffness: 80 },
  });

  const orbScale = interpolate(f, [0, 150], [1, 1.25], {
    extrapolateRight: "clamp",
  });
  const orbOpacity = interpolate(f, [0, 30, 130, 150], [0, 0.35, 0.35, 0], {
    extrapolateRight: "clamp",
  });

  return (
    <AbsoluteFill style={{ background: "#11111b", display: "flex", alignItems: "center", justifyContent: "center" }}>
      {/* Glowing orb */}
      <div
        style={{
          position: "absolute",
          width: 600,
          height: 600,
          borderRadius: "50%",
          background: "radial-gradient(circle, #cba6f7 0%, #89b4fa 40%, transparent 70%)",
          opacity: orbOpacity,
          transform: `scale(${orbScale})`,
          filter: "blur(80px)",
        }}
      />

      {/* Title */}
      <div style={{ textAlign: "center", position: "relative", zIndex: 1 }}>
        <div
          style={{
            fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
            fontSize: 160,
            fontWeight: 800,
            color: "#cdd6f4",
            letterSpacing: -4,
            opacity: titleOpacity,
            transform: `scale(${interpolate(f, [0, 20], [0.85, 1], { extrapolateRight: "clamp" })})`,
          }}
        >
          Nexus
        </div>
        <div
          style={{
            fontFamily: "-apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif",
            fontSize: 36,
            fontWeight: 300,
            color: "#a6adc8",
            letterSpacing: 6,
            textTransform: "uppercase",
            opacity: subtitleOpacity,
            marginTop: -8,
          }}
        >
          Remote workspace daemon
        </div>
      </div>
    </AbsoluteFill>
  );
};
