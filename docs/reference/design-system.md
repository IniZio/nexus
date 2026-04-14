# Nexus macOS Design System

> Source of truth for all visual decisions in `packages/nexus-swift`.  
> Every value here is measured from the Conductor reference and verified in production.  
> **When adding any new UI surface: consult this document first, reach for `Theme.`* tokens, never hardcode.**

---

## 1. Guiding principle

The Nexus UI targets **Conductor-class polish**: a light, native macOS app where the terminal is an inset dark card within a predominantly light chrome — not a full-bleed black window. This distinction is the single biggest factor in perceived quality.

```
┌──────────────────────────────────────────────────────────────┐
│ Window chrome — bgApp #F5F4F2 (warm off-white)               │
│  ┌──────────────┬─────────────────────────────────────────┐  │
│  │ SIDEBAR      │ Toolbar breadcrumb (unified titlebar)    │  │
│  │ vibrancy     ├─────────────────────────────────────────┤  │
│  │ material     │ Session info strip — bgContent #FFFFFF  │  │
│  │              ├─────────────────────────────────────────┤  │
│  │  ● ws-name   │ ┌─ Terminal card ─────────────────────┐ │  │
│  │  ○ ws-name   │ │  bgTerm #1C1C1C  (12px inset, r=8) │ │  │
│  │              │ └─────────────────────────────────────┘ │  │
│  │              ├─────────────────────────────────────────┤  │
│  │  ⌘N New     │ Tab strip (vibrancy) │ Content (white)   │  │
│  └──────────────┴─────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────┘
```

---

## 2. Color tokens

All tokens live in `Theme.swift`. Never use raw hex values outside that file.

### 2.1 Backgrounds


| Token             | Value                          | Usage                                                            |
| ----------------- | ------------------------------ | ---------------------------------------------------------------- |
| `Theme.bgApp`     | `#F5F4F2`                      | Window chrome, main pane background, any non-content surface     |
| `Theme.bgContent` | `#FFFFFF`                      | Content panels: session info strip, bottom panel tab content     |
| `Theme.bgTerm`    | `#1C1C1C`                      | Terminal card only — always dark regardless of app theme         |
| Sidebar           | `NSVisualEffectView(.sidebar)` | Never override with a flat color; always use `SidebarMaterial()` |


**Rule:** If you are unsure whether to use `bgApp` or `bgContent`, use `bgApp`. `bgContent` is reserved for areas that present data (not chrome).

### 2.2 Text — always use `NSColor` variants

Text colors must use `NSColor`-backed tokens, not hex, because they automatically adapt to the sidebar vibrancy material. Hardcoded hex on top of a vibrancy background will render incorrectly.


| Token                  | NSColor backing        | Approximate hex (light mode) | Usage                                                             |
| ---------------------- | ---------------------- | ---------------------------- | ----------------------------------------------------------------- |
| `Theme.label`          | `.labelColor`          | `#1C1C1E`                    | Primary text: workspace names, values, commands                   |
| `Theme.labelSecondary` | `.secondaryLabelColor` | `#3C3C43` @ 60%              | Secondary text: section headers, branch names, status pill labels |
| `Theme.labelTertiary`  | `.tertiaryLabelColor`  | `#3C3C43` @ 30%              | Muted text: repo labels, timestamps, placeholder text, icons      |


**Rule:** Never use `Theme.label` on a terminal surface. Never use a terminal color (`termText`, `termDim`, etc.) outside the terminal card.

### 2.3 Sidebar interaction states


| Token                   | Value                 | Usage                             |
| ----------------------- | --------------------- | --------------------------------- |
| `Theme.sidebarSelected` | `black.opacity(0.13)` | Selected workspace row background |
| `Theme.sidebarHover`    | `black.opacity(0.05)` | Hovered row/button background     |


**Rule:** Do not use `accent` color for selection in the sidebar. Conductor uses neutral gray — it feels more native and less jarring.

### 2.4 Borders and separators


| Token                | Value                 | Usage                                 |
| -------------------- | --------------------- | ------------------------------------- |
| `Theme.separator`    | `black.opacity(0.08)` | All dividers, borders between regions |
| `Theme.separatorMid` | `black.opacity(0.12)` | Snapshot timeline connector lines     |


Apply `Divider().overlay(Theme.separator)` for all dividers. Add `.opacity(0.6)` on sidebar dividers (header/footer) to make them whisper-level subtle.

### 2.5 Accent


| Token          | Value     | Usage                                                                  |
| -------------- | --------- | ---------------------------------------------------------------------- |
| `Theme.accent` | `#E84343` | Active tab indicator underline, active snapshot dot, hover-state links |


This coral-red matches Conductor's brand accent. Do not use `Color.accentColor` (system blue) — it creates visual inconsistency with the reference.

### 2.6 Status colors


| Token          | Value     | Workspace status | Terminal use             |
| -------------- | --------- | ---------------- | ------------------------ |
| `Theme.green`  | `#28CD41` | Running          | `termGreen` prompt glyph |
| `Theme.orange` | `#FF9500` | Paused           | —                        |
| `Theme.red`    | `#FF3B30` | Error states     | —                        |


Use `Theme.statusColor(workspace.status)` rather than switching on status manually.

### 2.7 Terminal syntax colors

These are only for use inside `TerminalView`. Never use them in chrome.


| Token       | Value     | Usage                               |
| ----------- | --------- | ----------------------------------- |
| `termText`  | `#D4D4D4` | User-typed commands, primary output |
| `termDim`   | `#6B6B6B` | Agent output, secondary output      |
| `termGreen` | `#4EC994` | Prompt glyph `❯`                    |
| `termBlue`  | `#569CD6` | Agent prefix glyph `◆`              |


---

## 3. Typography

All type uses **SF Pro** (the system font). Never specify a family name explicitly.


| Token            | Definition                              | Usage                                          |
| ---------------- | --------------------------------------- | ---------------------------------------------- |
| `Theme.fontSm`   | `system(size: 11)`                      | Timestamps, small labels, empty-state captions |
| `Theme.fontBody` | `system(size: 13)`                      | Workspace names, body text                     |
| `Theme.fontMono` | `system(size: 12, design: .monospaced)` | Port numbers, branch inline mono spans         |


### Weights by context


| Context                            | Weight                           | Example                               |
| ---------------------------------- | -------------------------------- | ------------------------------------- |
| Sidebar workspace name             | `.regular`                       | `auth-feature`                        |
| Sidebar section header             | `.regular`                       | `› nexus`                             |
| Sidebar "Workspaces" header        | `.regular`                       | Always regular — semibold feels heavy |
| Toolbar breadcrumb: workspace name | `.semibold`                      | `auth-feature`                        |
| Toolbar breadcrumb: branch         | `.regular`                       | `feat/oauth`                          |
| Active tab label                   | `.medium`                        | `Snapshots`                           |
| Inactive tab label                 | `.regular`                       | `Ports`                               |
| Status pill label                  | `.medium`                        | `Running`                             |
| Terminal prompt line               | `.regular` (monospaced inherits) | `❯ claude --continue`                 |


**Rule:** When in doubt, use `.regular`. Semibold/bold is for exactly one thing per component — the primary label in a toolbar or card header. Overusing weight is the fastest way to make a UI feel heavy.

---

## 4. Spacing and sizing

### 4.1 Sidebar


| Element                                         | Value                         |
| ----------------------------------------------- | ----------------------------- |
| Column width: min / ideal / max                 | `200 / 220 / 280 pt`          |
| Header ("Workspaces") height                    | `36 pt`                       |
| Header left padding                             | `16 pt`                       |
| Header icon button frame                        | `28 × 28 pt`                  |
| Repo section label left indent                  | `14 pt`                       |
| Repo section label top padding                  | `8 pt`                        |
| Workspace row height                            | `30 pt`                       |
| Workspace row left indent (leading edge to dot) | `20 pt`                       |
| Workspace row right padding                     | `10 pt`                       |
| Row selection corner radius                     | `6 pt`                        |
| Row selection horizontal inset                  | `6 pt` each side              |
| Footer height                                   | `34 pt`                       |
| Footer icon left padding                        | `8 pt`                        |
| Status dot size                                 | `7 pt` (fill) / `14 pt` frame |
| Status pulse halo max scale                     | `2.0×`                        |
| Status pulse duration                           | `1.9 s` easeOut, repeating    |


### 4.2 Main content


| Element                                | Value                             |
| -------------------------------------- | --------------------------------- |
| Session info strip height              | `34 pt`                           |
| Session info strip horizontal padding  | `16 pt`                           |
| Terminal card inset (all edges)        | `12 pt`                           |
| Terminal card corner radius            | `8 pt`                            |
| Terminal card border                   | `white.opacity(0.06)` 1 pt stroke |
| Terminal content padding (inside card) | `14 pt` all edges                 |
| Terminal line vertical padding         | `1.5 pt` each side                |
| Terminal font size                     | `13 pt` monospaced                |
| Bottom panel height                    | `185 pt`                          |
| Bottom panel tab strip height          | `30 pt`                           |
| Bottom panel tab horizontal padding    | `10 pt` each side                 |
| Bottom panel active tab underline      | `2 pt` height, `Theme.accent`     |


### 4.3 Toolbar breadcrumb


| Element                | Value                                             |
| ---------------------- | ------------------------------------------------- |
| Window toolbar style   | `.unified(showsTitle: false)`                     |
| Breadcrumb chevron `›` | SF Symbol `chevron.right`, size 10, weight medium |
| Breadcrumb spacing     | `6 pt`                                            |


### 4.4 Window


| Element               | Value            |
| --------------------- | ---------------- |
| Minimum size          | `860 × 520 pt`   |
| Preferred launch size | `~1000 × 640 pt` |
| Color scheme          | `.light` forced  |


---

## 5. Component patterns

### 5.1 Sidebar vibrancy

Never apply a flat background color to any part of the sidebar column. Instead:

```swift
.background(SidebarMaterial().ignoresSafeArea())
```

`SidebarMaterial` is an `NSViewRepresentable` wrapping `NSVisualEffectView(material: .sidebar, blendingMode: .behindWindow)`. This gives the frosted glass effect that adapts to whatever is behind the window.

**Anti-pattern:**

```swift
.background(Color(hex: "#F4F4F4"))  // flat — kills vibrancy, looks dead
```

### 5.2 Sidebar dividers

```swift
Divider().overlay(Theme.separator).opacity(0.6)
```

The double-opacity keeps dividers whisper-level. At full opacity they look like hard borders; at `0.6` they are visible only when you look for them.

### 5.3 Row hover/selection

```swift
RoundedRectangle(cornerRadius: 6)
    .fill(isSelected ? Theme.sidebarSelected : hover ? Theme.sidebarHover : .clear)
    .padding(.horizontal, 6)
```

Never use `Theme.accent` for row selection. The neutral gray selection (`black.opacity(0.13)`) is what makes the sidebar feel native — not a blue highlight.

### 5.4 Terminal inset card

```swift
ScrollView {
    TerminalView(workspace: workspace)
}
.background(Theme.bgTerm)
.clipShape(RoundedRectangle(cornerRadius: 8))
.overlay(RoundedRectangle(cornerRadius: 8).stroke(Color.white.opacity(0.06), lineWidth: 1))
.padding(12)
.background(Theme.bgApp)
```

The outer `.background(Theme.bgApp)` is what creates the visible light border around the dark card. The `.overlay` adds a subtle 1 pt inner glow that defines the card edge without looking harsh.

### 5.5 Tab active indicator

```swift
Rectangle()
    .fill(active ? Theme.accent : .clear)
    .frame(height: 2)
```

Placed at the `.bottom` of the tab button frame via `VStack(spacing: 0) { Spacer(); Text(...); Spacer(); Rectangle() }`. The active text weight changes to `.medium` (not `.semibold`) — enough to signal active without being heavy.

### 5.6 Status dot

The running state has a pulsing halo:

```swift
// Halo (only for .running)
Circle()
    .fill(Theme.green.opacity(0.22))
    .frame(width: 14)
    .scaleEffect(pulse ? 2.0 : 1.0)
    .opacity(pulse ? 0 : 0.5)
    .animation(.easeOut(duration: 1.9).repeatForever(autoreverses: false), value: pulse)

// Core dot
Circle()
    .fill(...)  // green / orange / clear
    .overlay(Circle().stroke(Theme.labelTertiary, lineWidth: 1.5))  // stroke only for .stopped
    .frame(width: 7)
```

Stopped state uses an **outline** (not fill) so the dot is present but clearly indicates inactivity.

### 5.7 Toolbar buttons

```swift
Button(action: action) {
    Image(systemName: icon)
        .font(.system(size: 13))
        .foregroundColor(hover ? Theme.label : Theme.labelSecondary)
        .frame(width: 28, height: 28)
        .background(RoundedRectangle(cornerRadius: 5)
            .fill(hover ? Color.black.opacity(0.06) : .clear))
}
.buttonStyle(.plain)
.onHover { hover = $0 }
```

Frame is always `28 × 28`. Background only appears on hover. Icon is `size: 13` in the toolbar.

---

## 6. Layout regions and their backgrounds


| Region                   | Background token                | Notes                     |
| ------------------------ | ------------------------------- | ------------------------- |
| Sidebar column           | `SidebarMaterial()`             | Vibrancy only, never flat |
| Sidebar header           | transparent (inherits vibrancy) | Do not set background     |
| Sidebar footer           | transparent (inherits vibrancy) | Do not set background     |
| Tab strip (bottom panel) | `SidebarMaterial()`             | Matches sidebar visually  |
| Window/main pane         | `Theme.bgApp`                   | Warm off-white frame      |
| Session info strip       | `Theme.bgContent`               | White — data surface      |
| Terminal card interior   | `Theme.bgTerm`                  | Always dark               |
| Bottom panel content     | `Theme.bgContent`               | White — data surface      |
| Empty states             | `Theme.bgApp`                   | Warm off-white            |


---

## 7. What NOT to do

These are the most common mistakes that break the polish:


| Anti-pattern                                                    | Why it's wrong                                      | Correct approach                                    |
| --------------------------------------------------------------- | --------------------------------------------------- | --------------------------------------------------- |
| `.background(Color(hex: "#F4F4F4"))` on sidebar                 | Kills vibrancy, flat gray                           | `SidebarMaterial()`                                 |
| `.preferredColorScheme(.dark)`                                  | Inverts all semantic colors, breaks vibrancy        | Always `.light`                                     |
| `Color.accentColor` (system blue) for tabs/selection            | System blue conflicts with Conductor's coral        | `Theme.accent` for tabs; neutral gray for selection |
| `.semibold` on sidebar workspace names                          | Makes rows feel heavy                               | `.regular` always in sidebar list                   |
| Full-bleed dark terminal (no card inset)                        | The #1 source of "looks ugly" — dark dominates      | Terminal as inset card with `padding(12)`           |
| Hardcoded `#1C1C1E` for text                                    | Doesn't adapt to vibrancy                           | `Theme.label` / `Color(nsColor: .labelColor)`       |
| `.scrollContentBackground(.hidden)` without `SidebarMaterial()` | Transparent bg with no material = invisible content | If hiding scroll background, always apply material  |
| Custom divider color                                            | Inconsistent separator weight                       | `Divider().overlay(Theme.separator).opacity(0.6)`   |
| Row heights below 28 pt                                         | Rows feel cramped                                   | Minimum 28 pt; sidebar uses 30 pt                   |


---

## 8. Adding a new screen or panel

Checklist before shipping any new surface:

1. **Background**: Is it in the sidebar column? Use `SidebarMaterial()`. Is it a data surface? Use `bgContent`. Is it window chrome? Use `bgApp`.
2. **Text**: Are you using `Theme.label / labelSecondary / labelTertiary`? No hardcoded hex for text.
3. **Font weight**: Regular for lists, medium for active states, semibold for exactly one headline per screen.
4. **Spacing**: Row heights ≥ 28 pt. Horizontal padding ≥ 14 pt inside sidebar; ≥ 16 pt inside content areas.
5. **Interactive states**: Every tappable/hoverable element has `.onHover` with `sidebarHover` (in sidebar) or `black.opacity(0.06)` (in content).
6. **Accent usage**: `Theme.accent` only for active indicators (tab underlines, current timeline dot). Not for selections.
7. **Terminal surfaces**: Dark content lives inside a rounded card with `cornerRadius: 8` and `padding: 12`. Never full-bleed.
8. **Animations**: Tab switches use `.easeInOut(duration: 0.12)`. Status pulse uses `.easeOut(duration: 1.9)`. Use system spring for modal sheets.

---

## 9. Source file map

```
packages/nexus-swift/Sources/NexusApp/
├── Theme.swift                    ← ALL design tokens live here
├── NexusApp.swift                 ← Window style (.unified, showsTitle: false)
├── AppState.swift                 ← Root state, daemon client selection
├── Models/Workspace.swift         ← Data model (no UI)
├── Client/
│   ├── DaemonClient.swift         ← Protocol
│   ├── MockClient.swift           ← Offline demo
│   └── WebSocketClient.swift      ← Live daemon (JSON-RPC 2.0 / WS)
└── Views/
    ├── ContentView.swift          ← NavigationSplitView root, column widths
    ├── SidebarView.swift          ← Sidebar: header, repo sections, rows, footer
    ├── WorkspaceDetailView.swift  ← Breadcrumb toolbar, session strip, terminal card
    ├── TerminalView.swift         ← Terminal lines, cursor, mock content
    └── BottomPanelView.swift      ← Tab strip, Snapshots, Ports, Log panels
```

The single design system entrypoint is `Theme.swift`. A designer updating colors, fonts, or spacing should only ever need to edit that file.