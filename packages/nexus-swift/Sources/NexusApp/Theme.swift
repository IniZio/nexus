import NexusCore
import SwiftUI
import AppKit

// MARK: - Color tokens
// All values measured directly from the Conductor screenshot.
// Text tokens use NSColor so they adapt correctly to the sidebar vibrancy material.

enum Theme {

    // ── Backgrounds ────────────────────────────────────────────────
    /// Window / main pane – warm off-white like macOS default window bg
    static let bgApp     = Color(hex: "#F5F4F2")
    /// Main content area – pure white (Conductor's center pane is #FFF)
    static let bgContent = Color(hex: "#FFFFFF")
    /// Inset terminal card background
    static let bgTerm    = Color(hex: "#1C1C1C")
    /// Sidebar – handled by NSVisualEffectView, no hex needed

    // ── Sidebar interaction ────────────────────────────────────────
    /// Conductor selected row — very light, not as dark as macOS default list selection
    static let sidebarSelected = Color.black.opacity(0.05)
    static let sidebarHover    = Color.black.opacity(0.03)

    // ── Borders ────────────────────────────────────────────────────
    static let separator     = Color.black.opacity(0.08)
    static let separatorMid  = Color.black.opacity(0.12)

    // ── Semantic text (NSColor adapts to vibrancy/appearances) ─────
    static let label          = Color(nsColor: .labelColor)
    static let labelSecondary = Color(nsColor: .secondaryLabelColor)
    static let labelTertiary  = Color(nsColor: .tertiaryLabelColor)

    // ── Status ─────────────────────────────────────────────────────
    static let green  = Color(hex: "#28CD41")
    static let orange = Color(hex: "#FF9500")
    static let red    = Color(hex: "#FF3B30")

    // ── Accent – matches Conductor's coral/red brand color ─────────
    static let accent = Color(hex: "#E84343")

    // ── Terminal syntax ────────────────────────────────────────────
    static let termText   = Color(hex: "#D4D4D4")
    static let termDim    = Color(hex: "#6B6B6B")
    static let termGreen  = Color(hex: "#4EC994")
    static let termBlue   = Color(hex: "#569CD6")
    static let termYellow = Color(hex: "#DCDCAA")

    // ── Typography ─────────────────────────────────────────────────
    static let fontSm   = Font.system(size: 11)
    static let fontBody = Font.system(size: 13)
    static let fontMono = Font.system(size: 12, design: .monospaced)

    // ── Helpers ────────────────────────────────────────────────────
    static func statusColor(_ s: WorkspaceStatus) -> Color {
        switch s {
        case .running, .restored: return green
        case .paused:             return orange
        case .stopped, .created:  return Color(nsColor: .tertiaryLabelColor)
        }
    }
}

// MARK: - Hex init

extension Color {
    init(hex: String) {
        let h = hex.trimmingCharacters(in: CharacterSet.alphanumerics.inverted)
        var n: UInt64 = 0
        Scanner(string: h).scanHexInt64(&n)
        self.init(
            red:   Double((n >> 16) & 0xFF) / 255,
            green: Double((n >>  8) & 0xFF) / 255,
            blue:  Double( n        & 0xFF) / 255
        )
    }
}

// MARK: - Sidebar vibrancy

struct SidebarMaterial: NSViewRepresentable {
    func makeNSView(context: Context) -> NSVisualEffectView {
        let v = NSVisualEffectView()
        v.material      = .sidebar
        v.blendingMode  = .behindWindow
        v.state         = .active
        return v
    }
    func updateNSView(_ v: NSVisualEffectView, context: Context) {}
}
