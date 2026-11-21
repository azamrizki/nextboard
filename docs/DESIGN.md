# NextBoard — design system

**Intent (one sentence):** a neutral monochrome system with light and dark themes: the primary color is the ink itself, hairline borders, the Geist font, thin floating glass chrome, and work content (terminal, forms, data) on solid high-contrast surfaces so it reads fast.

Applies to the web (`nextboard/web`, also used by the desktop app through a proxy). The desktop Hubs screen (`nextboard-desktop/src`) and Android (`nextboard-android`, Compose) still use the old blue palette and are dark only: not moved yet because the focus is now on the hub (see decision 27 in ROADMAP). `web/src/styles/tokens.css` is the source of truth for the values; the tables below list only the most used ones. Raw values outside the tokens **must not** be used in components.

## 1. Context and goals

- **Two themes, one component.** Components use only tokens; a theme only changes their values. `tokens.css` has three blocks: `:root` (sizes, typography, motion), `:root, [data-theme='dark']`, and `[data-theme='light']`. A new color token must be in both color blocks.
- **Choice per browser:** System (the default), Light, Dark in Settings › Appearance; a sun/moon button in the header to switch fast. `data-theme` is set on `<html>` before the first paint by `public/theme-boot.js` (a separate file because the hub's CSP forbids inline scripts), then carried on by `lib/theme.ts`.
- **`data-theme` works on any element:** a subtree can stay dark on a light page. Used by the Memory sky (always dark) and consoles with the "Always dark" choice.
- **Color only for meaning:** status, agents, code, and what people draw. Everything else is grey.
- **One metaphor only: glass.** Not mixed with neumorphism, skeuomorphism, or flat-outline. Depth = blur + transparency + light border, not heavy shadows.
- **Glass for chrome, solid for content.** Header, toolbar/dock, cards, modals, menus, toasts = glass. Terminal content, long text areas, and inputs = solid or near-solid surfaces.
- **Comfortable density**: 40 px controls, 16 px gap between fields, 20 px card padding.
- **Bento**: list pages (Machines, Devices, Setup steps) use a card grid with sizes that differ by the weight of the information.

## 2. Tokens and foundations

### 2.1 Base colors

The primary color is ink: the primary button, the active choice, and the focus ring are white in the dark theme, black in the light theme.

| Token | Dark | Light | Used for |
|---|---|---|---|
| `--c-primary` | `#F5F5F5` | `#0D0D0D` | primary button fill, active choice (segmented, tool rail) |
| `--c-primary-300` | `#E5E5E5` | `#262626` | active lines, dots, and marker text |
| `--c-primary-600` | `#D4D4D4` | `#2E2E2E` | primary button hover |
| `--c-success` | `#22C55E` | `#15803D` | running/idle/busy status |
| `--c-warning` | `#F59E0B` | `#B45309` | reconnecting/starting, warnings |
| `--c-danger` | `#EF4444` | `#DC2626` | error accent, error border |
| `--c-danger-300` | `#F87171` | `#B91C1C` | small error text (≥ 4.5:1) |
| `--c-danger-600` | `#D13030` | `#C52020` | destructive button fill |
| `--c-info` | `#A3A3A3` | `#6B6B6B` | neutral info: no blue |
| `--c-grey` | `#8C8C8C` | `#737373` | exited status (code 0) |

### 2.2 Text

| Token | Dark | Light | Role |
|---|---|---|---|
| `--text` | `#F5F5F5` | `#0D0D0D` | titles, body |
| `--text-2` | `#A8A8A8` | `#525252` | labels, secondary text |
| `--text-3` | `#8C8C8C` | `#6B6B6B` | hints, placeholders, metadata |
| `--text-on-primary` | `#0A0A0A` | `#FFFFFF` | on `--c-primary` only |
| `--text-on-danger` | `#FFFFFF` | `#FFFFFF` | on danger fill |
| `--text-on-color` | `#FFFFFF` | `#FFFFFF` | on an agent or project color |
| `--text-on-warning` | `#1C1204` | `#FFFFFF` | on `--c-warning` |

`--text-3` is the lower limit for text. No text may be dimmer than that (wrong example: placeholder `rgba(255,255,255,.3)`).

### 2.3 Background and glass layers

| Token | Dark | Light |
|---|---|---|
| `--bg` | `#0A0A0A` | `#F2F2F2` |
| `--aura` / `--bg-ambient` | the backdrop the islands float on: a soft white glow from the top left and a fainter one from the bottom right, monochrome | a white glow over light grey, a faint shade at the bottom right |
| `--page-grid` | a very faint 64 px grid under the top-left glow, fading out; not on the board (it already has dots) or Memory | same |
| `--glass-1` | `rgba(20,20,20,.62)` chrome (header, dock, toolbar, rails) | `rgba(255,255,255,.72)` |
| `--glass-2` | `rgba(20,20,20,.8)` cards, modals | `rgba(255,255,255,.86)` |
| `--glass-3` | `rgba(23,23,23,.86)` menus, popovers, toasts | `rgba(255,255,255,.92)` |
| `--glass-border` | `rgba(255,255,255,.09)` | `rgba(0,0,0,.09)` |
| `--glass-border-strong` | `rgba(255,255,255,.17)` | `rgba(0,0,0,.17)` |
| `--surface-solid` | `#0F0F0F` | `#FFFFFF` |
| `--tile-bg` | `#131313` board cards and the Code view's islands, with `--tile-shadow` | `#FFFFFF` |
| `--island-solid` | `#111111` an island that never shows what is behind it | `#FFFFFF` |
| `--surface-hover` | `rgba(255,255,255,.07)` | `rgba(0,0,0,.05)` |
| `--surface-active` | `rgba(255,255,255,.11)` | `rgba(0,0,0,.085)` |
| `--backdrop` | `rgba(0,0,0,.6)` + `blur(6px)` | `rgba(10,10,10,.3)` + `blur(6px)` |

Blur: `--glass-blur-1` `blur(18px) saturate(140%)`, `-2` `blur(24px) saturate(150%)`, `-3` `blur(14px) saturate(130%)`.

Required fallback: `@supports not (backdrop-filter: blur(1px))` and `@media (prefers-reduced-transparency: reduce)` → every `--glass-*` is replaced by that theme's `--solid-1/2/3`. Settings › Appearance › **Surfaces** (Glass | Solid, per browser, `nb.glass`, set before the first paint by `public/theme-boot.js`) does the same with `<html class="no-glass">`.

**Islands** (2026-11-20, "Glass Islands"): every panel, card and tile is an island, a rounded surface (`--r-island 12px`, cards and tiles `--r-lg`) with a hairline border and a soft shadow, floating on the backdrop. Between islands is a gap (`--island-gap 6px`), not a line: in the Code view the gap is also the sash, which shows a short line only under the pointer. Bars that are not islands (the Code view's top bar and status bar) lie on the backdrop with no fill or border. The accent stays the monochrome ink (white on near-black, black on white): islands bring no new colour. States glow: selected is the ink ring with a soft halo (`--ring-selected`), needs you the amber ring with an amber halo (`--ring-needs-you`), connecting the wire-grey ring with a halo. A console's screen is a well inside its island: inset by the gap, `--r-md`, in `--term-bg` with a hairline. Card headers have no fill or line of their own, they are part of the island.

**Colors stored on the board** (`#E6EAF2` "Chalk" and seven other swatches) stay hex in the data, because a board is shared between clients whose themes can differ. When drawing, `inkColor()` (`board/palette.ts`) maps them to `--ink-*`, so Chalk becomes dark ink on a white canvas. Other colors show as they are. Sticky note paper is the same in both themes.

**Charts:** `--viz-1..4` are picked separately per theme, not inverted, and pass the dataviz validator on each theme's `--tile-bg`.

### 2.4 Typography

- Fonts: **Geist** (UI & display), **Geist Mono** (paths, fingerprints, code on cards), **JetBrains Mono** (only the console and Monaco, because its glyph coverage is wider for TUIs). Features `ss01` and `cv11` are on, as in the reference. Bundled locally (web: `@fontsource-variable/*`), not from a CDN — the hub's CSP only allows `font-src 'self' data:`.
- Scale (mobile-first, compact): `--fs-xs 12px` · `--fs-sm 13px` · `--fs-md 14px` (body) · `--fs-lg 16px` · `--fs-xl 18px` · `--fs-2xl 22px` · `--fs-3xl 28px`.
- Weights: 400 body, 500 labels/buttons, 600 card and page titles, 800 brand. Line-height 1.5 body, 1.25 titles. Tight tracking on titles: `--tracking-title -0.025em` (h1), `--tracking-snug -0.012em` (small titles).
- Tabular numbers (`font-variant-numeric: tabular-nums`) for times, ports, sizes.

### 2.5 Spacing, radius, elevation, motion

- Base-4 spacing: `--s-1 4` `--s-2 8` `--s-3 12` `--s-4 16` `--s-5 20` `--s-6 24` `--s-8 32` `--s-10 40` `--s-12 48` (px). No values outside this scale.
- Radius: `--r-sm 8px` (small chips, icons) · `--r-md 10px` (inputs, buttons) · `--r-island 12px` (the Code view's islands) · `--r-lg 14px` (terminal nodes, menus) · `--r-xl 20px` (cards, modals) · `--r-pill 999px`. Gap between islands: `--island-gap 6px`.
- Elevation `--e-1/2/3`: dark has dense, tight shadows, light thin and wide ones (see `tokens.css`); always paired with `--glass-highlight`.
- Motion: `--t-fast 120ms` (hover, color) · `--t-base 180ms` (modals, menus) · easing `cubic-bezier(.2,.8,.2,1)`. Only for state feedback; no decorative animation. The only repeating animation: the **busy** dot pulse (1.6 s). `prefers-reduced-motion: reduce` → 0 ms transitions, the pulse is replaced by a solid dot with a static ring.

## 3. Components

Every interactive component must have the states **default, hover, focus-visible, active, disabled**, plus **loading** and **error** where relevant.

### 3.0 Layout: phone, tablet, desktop

One place decides (`web/src/lib/layout.ts`, and `public/theme-boot.js` before the first paint with the same rules): `<html data-layout="phone|tablet|desktop" data-input="touch|mouse">`, plus `data-keyboard` while the on-screen keyboard is open. CSS and code read the same answer; do not add new width breakpoints to pick a layout.

| Layout | When |
|---|---|
| phone | window < 600 px, or a touch screen whose short side is < 600 px (stays phone when rotated and when the keyboard is open) |
| tablet | window < 1200 px, or any other touch screen (Xiaomi Pad 6, Galaxy Tab S9 in both orientations) |
| desktop | ≥ 1200 px with a mouse |

- Settings › Appearance › Layout can pin one layout per browser (Auto, Phone, Tablet, Desktop), for example a tablet with a keyboard and trackpad.
- **Shell space:** `--shell-top` and `--shell-bottom` are what the app bars take from the screen (header, tab bar, safe area). Whatever sticks to the screen uses both, rather than adding up the header height itself.
- **Safe area:** `viewport-fit=cover`; the header, tab bar, sheets, and toasts use `--safe-*`.
- **On-screen keyboard:** `interactive-widget=resizes-content`, so on Android the bottom bar and sheets rise above the keyboard; the phone tab bar hides during `data-keyboard`.
- **Touch:** `data-input="touch"` raises controls to 40/44 px and menu items to 44 px. Board content keeps the old size until the board gets its turn; menus and dialogs opened from the board are already touch-sized. Dialogs do not focus a text field by themselves, so the keyboard does not cover them.
- Tests: Playwright projects `phone` (393×873), `tablet-portrait` (800×1280), `tablet-landscape` (1280×800), all touch, plus desktop light and dark. `e2e/layout.spec.ts` checks each page: no sideways scroll, a one-line header, header controls do not overlap and do not overflow, titles are not covered, touch targets ≥ 40. `e2e/sweep.spec.ts` checks every page, each Settings pane, dialogs, and each card type in focus mode: no element or line of text runs past the screen edge or is cut off by a box that cannot scroll. Ellipsis truncation and boxes that are meant to scroll sideways (tab strip, kanban columns) are fine. `SWEEP_SHOTS=<folder>` saves screenshots of all of them. `e2e/console-gl.spec.ts` checks the GPU-drawn console at pixel ratios 1, 2, and 2.75, `e2e/console-pool.spec.ts` the sharing of WebGL contexts (§3.9).
- **Measure with the screen width, not `window.innerWidth`.** Chrome on a phone widens a page that is too wide and then shows it smaller, so `innerWidth` grows too and an overly wide page looks right in tests.

### 3.1 Buttons

| Variant | Default | Hover | Active | Disabled |
|---|---|---|---|---|
| `primary` | `--c-primary` fill, white text, top highlight `inset 0 1px 0 rgba(255,255,255,.18)` | `--c-primary-600` | down 1px, highlight gone | opacity .45, cursor not-allowed |
| `secondary` (glass) | `--surface-input` + `--glass-border` border | `--surface-hover`, `--glass-border-strong` border | `--surface-active` | same |
| `ghost` | transparent, `--text-2` text | `--surface-hover`, `--text` text | `--surface-active` | same |
| `danger` | `--c-danger-600` fill, white text | 8% darker | down 1px | same |
| `danger-ghost` | `--c-danger-300` text | bg `rgba(234,33,67,.12)` | bg `.18` | same |

- Sizes: `md` height 40, padding-x 16, `--fs-md` 500 · `sm` height 32, padding-x 12, `--fs-sm` · `icon` 36×36 (sm 32×32), 16–18 px icon, `aria-label` **required**.
- Loading: a 14 px spinner replaces the leading icon, the label stays, `aria-busy="true"`, not clickable.
- Button label = verb + object: "Add machine", "Pair device", "Open board". Wrong examples: "OK", "Submit", "Go".
- Focus-visible (every interactive element): `outline: 2px solid var(--c-primary-300); outline-offset: 2px`. No `outline: none` without a replacement.

### 3.2 Form fields

Fixed anatomy, from top to bottom:

1. **Label** — `--fs-sm` 500 `--text-2`, 6 px gap to the control. An optional field is marked "(optional)" in the label, not in the placeholder. Required fields get no mark.
2. **Control** — height 40, radius `--r-md`, bg `--surface-input`, 1px `--glass-border` border, padding-x 12, text `--fs-md` `--text`.
   - hover: border `--glass-border-strong`
   - focus-visible: border `--c-primary`, ring `0 0 0 3px rgba(24,86,255,.30)`, bg `rgba(255,255,255,.06)`
   - error: border `--c-danger`, ring `rgba(234,33,67,.25)` on focus, `aria-invalid="true"`
   - disabled: opacity .5
   - placeholder `--text-3`; a placeholder is only an example value, not a replacement for the label.
3. **Hint** (optional) — `--fs-xs` `--text-3`, 6 px gap.
4. **Error** — replaces the hint, `--fs-xs` `--c-danger-300` with a 12 px icon, linked via `aria-describedby`.

Layout rules:
- Vertical gap between fields 16 px; between groups (sections) 24 px, with a section title `--fs-xs` 600 uppercase tracking .06em `--text-3`.
- Side-by-side fields use a 12-column grid (example: Host 9 / Port 3). Below 560 px every field is one column.
- Custom **select**: `appearance: none` + a 16 px SVG chevron on the right (padding-right 36).
- **Textarea** (private key): `--surface-solid`, mono font `--fs-sm`, min-height 120, vertical resize only.
- **File input**: the native input is hidden (still focusable through the label), shown as a `secondary sm` button "Load from file" + the file name in `--text-3`.
- **Input group**: a 16 px leading icon (`--text-3`) or a trailing addon (an attached "Browse" button, right radius only).
- **A choice of 2–4 options** (auth method, persist mode, host source tab) uses a **segmented control** or **radio cards**, not a `<select>`. Radio card: a glass card with a 1px border, a 500 title + an `--fs-xs` description; selected = `--c-primary` border + bg `rgba(24,86,255,.10)` + a filled radio dot. Keyboard: the left/right/up/down arrows move the choice (`role="radiogroup"`).
- Custom checkbox/switch 18 px / 36×20, focus follows the focus rules.
- Form footer (in a modal): buttons on the right, in the order `secondary` (Cancel) then `primary`; help text on the left in `--fs-xs` `--text-3`. The primary button shows loading on submit and the form is locked.

### 3.3 Cards (bento)

- Glass `--glass-2` + `--glass-blur-2`, `--glass-border` border, radius `--r-xl`, padding 20, `--e-2` + highlight.
- Grid: `repeat(auto-fill, minmax(320px, 1fr))`, gap 16. An important card may use `grid-column: span 2` (≥ 960 px), for example the "Pair new device" card in Devices.
- Anatomy: header (icon/status + 600 `--fs-lg` title + `--text-3` meta) → body (two-column key-value list: key `--fs-xs` `--text-3`, value `--fs-sm` `--text`) → action footer (`sm` buttons, left-aligned, the destructive action at the far right, separated by a spacer).
- Hover on a non-interactive card: no change. A clickable card: `--glass-border-strong` border + rises 1px.
- Long text (hostname, path): `text-overflow: ellipsis` + a `title` holding the full value. Fingerprint: mono, broken into groups of 4 characters, may wrap.

### 3.4 App header & navigation

- Floating header: 12 px margin from the edges, height 56, radius `--r-xl`, `--glass-1` + blur-1, border + highlight.
- **Uniform sizes:** every control in the header is 32 high (`--h-control-sm`), icons 16 (`--size-icon`), text `--fs-sm`; 12 to the header edge on all sides. The tab strip and segmented controls are also 32 outside (item 26, padding 2, border 1). Gaps: 12 between blocks (brand, navigation, right side), 8 between controls, 4 between icon buttons and between the tab strip and the add button. Icon buttons are clickable across the full 32×32. The hub name chip is a label, not a control, so it stays 22.
- Group with linked input: an "Input linked" button in the frame header and a `--tint-warning-*` frame border; each member console shows a link icon in its header, because even Ctrl+C goes to all of them.
- Left: brand mark (32 px logo, inverted like the primary button) + "NextBoard" 800 + the hub name chip.
- Center/left: navigation = a glass segmented control (Board · Machines · Devices); the active item has bg `--surface-active` + `--text` text + `aria-current="page"`.
- Right: events connection indicator (dot + "Live"/"Reconnecting…"), the Credentials lock button (a small dot in the text color while the vault is open; opens the vault window, §3.16), the Settings icon button, theme (sun/moon), the build label, Log out (ghost).
- **Build label:** which build runs, `v0.9.0 · 3f2a1c9` (`dev · 3f2a1c9*` for a dev build, `*` = uncommitted changes) in Geist Mono `--fs-xs` `--text-3`: a ghost button that opens About this hub (the version and its channel, the commit linked to GitHub, the build time, the update). A `dot-info` before it when a newer release is out. Until `/api/hub` answers it shows the last label this browser saw, so the header does not shift. Desktop and tablet; a phone has it in `…` › About this hub.
- **Update reminder** (`components/UpdateReminder.tsx`): when the app opens and a newer release is out, the owner gets a small modal "Update available": "**NextBoard v0.9.1** is out. This hub runs 0.9.0." with a What’s new link, one line on what the update does (or why this hub cannot update itself, with the terminal command), a small line "Skip for now asks again in 24 hours. Skip this version waits for the next release.", and one footer row Skip this version (ghost) · Skip for now (secondary) · Update now (primary, focused); the modal is `md` so the three fit on one row. Closing it is Skip for now. Update now keeps the modal open with the progress (download %, Restarting…, then Updated with Reload the page). The answer is kept by the hub, so the other browsers and devices follow it. Never for a device or a dev build; a phone gets it as a sheet.
- **Tablet:** stays one row, with the build label before `…`. Settings, theme, About, and Log out (inside a project also Projects, Memory, Machines, Devices) move into the `…` menu; the hub name disappears; below 1024 px the NextBoard name disappears outside a project, and below 900 px the navigation labels become icons only.
- **Phone:** the bar sticks to the top edge (56 px + safe area, no rounded corners) with the mark, then the usage chip, needs-you, live, the vault lock, and `…`; the connection dot only shows when not live. Inside a project, one button for the project and its workspace (project name on top, workspace below); its menu holds the workspaces (switch, New workspace, Rename, Close) and then the project list. Outside a project there is a **tab bar** at the bottom edge: Projects, Memory, Machines, Devices, Settings, with the icon in a `--surface-active` pill for the active one.
- **Workspace tabs:** right-click (long-press on a touch screen) opens Rename, Move left, Move right, Close, so nothing is reachable only by double-click or a shortcut.

### 3.5 Board

- **Relation arrows between tiles:** straight when two tiles face each other, one bend with a rounded corner when they do not, and routed around when a tile is in the way; a line never passes under a tile. Marks on the canvas (shapes, lines, ink, text) do not count as obstacles. Each line has a port dot at the source and an arrow at the target, the same thickness at every zoom. Color per kind: wire violet, forward orange dashed, preview cyan. The route is computed by `board/wireRoute.ts`.
- **An orchestrator wire at work:** while the console at its end is `working` or has a queue, the wire is brighter (`--wire-flow`) with a soft flow and three sparks of light running in the direction of the work, fading at both ends. `prefers-reduced-motion`: no motion, only the bright wire. Memory card wires and forward wires do not take part.
- The canvas uses `--bg-ambient` + the React Flow dot pattern `rgba(255,255,255,.06)`, gap 24.
- **Panning the board:** the Hand tool (`H`, or hold `Space` while push-to-talk is off) makes the whole board content pointer-transparent, so drag and swipe pan the board from anywhere, including over terminals and iframes. Without Hand, scroll and pinch belong to the board unless the pointer is on the focused tile: the only selected tile, or the tile that holds the keyboard. Over any other tile, scrolling in any direction pans the board and pinch zooms the board; the tile content does not move. Click a tile to give it scroll, click empty board to take it back. A tile inside a multi-tile selection does not count as focused. A swipe that pans the board keeps panning even when a focused tile passes under the still pointer, until the pointer moves more than 4 px or presses. The first click on a web tile or app tile only gives focus; its content gets the next click.
- **Right rail:** only what is used to work on the project: Code editor, Studios, then Chats, Memory, Git, Loops, Mission Map, then Auto arrange (rearranges the board, active from two cards). What gets placed on the board is in the right-click ring. Narrow screens have no right rail and no right-click, so everything stays in the left rail's `+` flyout.
- **Focus mode:** one card fills the screen, on this device only. The shared board is not written: the card gets the size of the area below the Focus bar at zoom 1 (`board/focus.ts`); other cards, lines, rails, the command bar, zoom, and the minimap are hidden. The card is in the URL (`?card=<id>`): Back on Android and Esc (as long as no field holds the keyboard) return to the board and the previous viewport; a reload keeps it, and the short link `/p/<project>?card=<id>` takes it to the workspace. The Focus bar (glass `--glass-1`, control height + 8) holds ‹ Board, the card name, its position number, and the previous/next card in reading order (rows from the top, then left to right). Enter through the focus button in the card header (next to the display button; on hover or when selected with a mouse, always on touch screens) or Focus in the card menu. A console follows the shown size (the last resize wins). On touch screens the focused card's content uses touch sizes and a 48 header. While focused, the focus and display buttons in the card header are hidden (the Focus bar already gives a way out, display sizes do not apply), so the title gets the room. A card near the board edge (±20000) that is enlarged to screen size is moved back inside the board, and the focus view follows it.
- **Touch screens** (`data-input="touch"`):
  - the Insert button on the left rail is always there and opens a labeled grid of every card type (no tooltips, so the names are written), plus Select several; the right rail's items come along only when the right rail is hidden;
  - long-press (500 ms, moving < 10 px) = right-click on the board and on `data-longpress` elements (`lib/longpress.ts`); Android Chrome's own `contextmenu` that arrives second is ignored, and the finger lift after it is not a tap;
  - two fingers pan and zoom the board over any card, including consoles and the editor; one finger inside a card still belongs to the card; the board page is not zoomed by the browser;
  - tapping a line opens its menu; Select several makes a tap add or remove a card from the selection, with a banner showing the count, All, and Done;
  - board controls 40–44 px, resize handles and line knobs 22 (board units), the minimap hidden; card content on the canvas stays at desktop size because it is zoomed along;
  - **card content:** whatever needs hover, double-click, or drag and drop on desktop has a way to do it with a finger:
    - Kanban: a finger scrolls the columns and the card list (columns snap sideways); a card is moved by the ⋮⋮ grip on its left (a `button`, so Chrome's touch targeting does not move the touch to the card's button); holding a card at the edge scrolls the column (with a mouse too); a card's `…` always shows; long-press on a card opens that card's menu (right-click too, in every layout); tap a column title = rename;
    - Checklist: tap an item's text = edit, × always shows, the list scrolls with a finger and the card is moved by its header;
    - Memory tile: bricks are moved by pointer drag (a mouse anywhere, a finger by the grip), not HTML drag and drop, which does not exist on touch screens;
    - charts (response time, heartbeat, tokens): a tap or a finger slide shows the value, and the value stays until the next tap;
    - Mission Map writes tap hints, not double-click and Ctrl+G.
- **Board on a phone** (`board/PhoneBoard.tsx`): a bottom tab bar **Board · Cards · Lead** (Lead uses the lead's name, e.g. Aruna); the last used tab is remembered per device, the default is Cards.
  - Board: the canvas with a short rail at the top left (Select, Hand, Spawn, Insert, Undo, Fit). Shapes, notes, text, pen, eraser, images, Redo, and New console are in the Insert grid, which stops above the tab bar. The command bar and zoom do not show: commands are in the Lead tab, zoom is by pinch.
  - Cards: a list of cards in reading order, one 64 px glass row each (icon, name, the console's agent and status, status dot, "Needs you"). A tap opens Focus mode with a "Cards" back button; a sideways swipe on the Focus bar or the card header moves to the previous/next card; Back returns to the list. Above the list: New console and Add a card (a sheet with a grid of card types). The badge on the Cards tab counts the consoles that need you.
  - Lead: the lead thread fills the screen above the command bar, with no close button.
  - The canvas stays rendered under Cards and Lead (opacity 0), so consoles keep their size and Focus mode can show right away. A newly made console, or one jumped to through needs-you, opens straight in Focus mode, without opening the keyboard by itself.
- **Command bar and bottom panels:** the bar height is measured (`--cmdbar-h`, used by `--h-cmdbar`); below 1380 px the zoom, the Git pill, and the minimap move up above the bar (the rules are in `board.css`, because that file can load after `orch.css`).
- **Right-click ring:** right-click on empty board opens seven round glass buttons (`--glass-3`) around More, clockwise from the top: Auto arrange (active from two cards), Spawn agent, Insert image, Web page, Paste, Shapes and notes, Compose group. The ring is centered on the pointer and stays inside the window. A single caption below the ring names the button being hovered or focused, so the names are not scattered around the ring. Arrows move the focus, Esc closes. A button with a panel replaces the ring with a glass panel that holds a back button, and Esc in the panel returns to the ring. More is a second, wider ring: 12 buttons (Checklist, Kanban, Calendar, Uptime monitor, File from project, Branches card, Memory card, Orchestrator card, Desktop app, Layouts, Snapshots, Select all) around Back, and Esc returns to the first ring. What already has its own button is not repeated in More: Code editor on the right rail, Fit on the left rail, zoom at the bottom left. Right-click on cards and lines still uses the normal menu.
- **Spawn agent in the ring:** an agent tile selects, the Login profile select (for agents that have profiles) then model and effort below it, the Spawn button places; a double-click on a tile places right away. The profile always starts from the default (NextBoard, unless Settings says otherwise) every time the ring opens or the agent changes, whatever other forms chose.
- **Compose group:** the same form in the ring panel and in the Group chip dialog: color dot + name, role chips, 4-column slots in agent colors, a Fill all row, one model and effort row per agent used by the slots, stepper + Spawn N. Clicking a slot changes the agent, right-click opens the catalog. From the ring, the team lands at the clicked point. The agent mark (`AgentBadge`): the real logo (`agents/logos/`, from `@lobehub/icons-static-svg` under the MIT license; the trademarks stay with their owners) on that agent's color; one-color shapes are painted in the agent color through a mask. An agent without a logo gets two letters of its name on its color gradient, a shell gets a terminal icon. The usage pill uses the same logos.
- **Code card by its width** (`studio/code/CodeStudio.tsx`), not by the screen, so a small card on desktop follows too: below 1000 px the agent side bar covers the editor as a drawer from the right (card-wide, at most 460), with a scrim that closes it when touched; below 640 px (phone) the left side bar also becomes a drawer over the editor (at most 360), closes by itself as soon as it opens a file, and only the active editor group shows; between 640 and 1000 the left side bar is at most 40 % wide. The bottom panel is never more than 60 % of the editor height; on a phone its panel tabs scroll sideways and the terminal list becomes a row above the terminal. A narrow title bar shows Go to file as an icon; a narrow status bar shows only the branch, problems, cursor position, and language. The Fill button is hidden in focus mode. On touch screens the tab close button always shows and sashes are wider for fingers.
- **Card header:** one arrangement for every card (`TileHead`, and `NodeHeader` for consoles, same size): 20 px mark, title, meta, spacer, `sm` ghost actions, display button, `…`, height `--h-node-header`, left padding 12, right 4. A header narrower than 440 px (phone, or a small card) shows its action buttons as icons only, and the meta gives way before the title. A card whose title can be changed has **Rename** in its menu (and on long-press / right-click); double-click on the title still works with a mouse. A bent line or arrow has **Straighten** in its menu. **Calendar** counts events per day from the height of the cell shown (focus mode shows more), "+N more" opens the list of that day's events (tap = edit, plus New event), and a calendar narrower than 520 px puts the month controls on a row of their own with short month names. The card type only sets the accent color (`--tile-accent`: top line and icon): blue by default, violet for orchestrator and memory, cyan for web. The `…` menu holds that card's actions and then the same actions as right-click. The group frame stays 32 px because the hub computes its geometry with that number (API-orchestration §1).
- **Card size:** the display button in the header opens Fit tile, Displays (24 in 1920×1080, 27 in 2560×1440, 32 in 4K 3840×2160), and Phones (iPhone SE 375×667, iPhone 14 Pro 393×852, iPhone 14 Pro Max 430×932, Pixel 7 412×915, Galaxy S8+ 360×740). A web tile always shows this button; other cards on hover or when selected, or while using one of those sizes. Picking one resizes the card from its top left corner, can be undone, and the view shifts when the card does not fit. Fit tile returns the card type's initial size. A dev server web tile: the size becomes the page's viewport and the card wraps around it. Images keep their proportions. Options narrower than the card's limit are disabled (editor 560, kanban and calendar 420). A card without a header, or several cards at once, has this list in the right-click menu. Groups, text, shapes, lines, and ink do not have this list.
- **Layouts:** a popup above the board, the same as the desktop app picker: the layout list on the left, lanes and save layout on the right, each scrolling on its own, the Deploy button in the footer so it is always visible. A successful deploy closes the popup; a failed one leaves it open with the lane choices intact.
- **Lead agent:** the lead's face is the first letter of its name in a `--wire`→`--c-primary` gradient circle, the only face in the thread. Its messages carry the face and name; its plans are `proposal` cards without a countdown, because they are not urgent. Message actions (`.msg-act`) appear on hover, always visible on touch screens; a reply uses a `--wire` line on the left, both above the box (`.orch-reply`) and above the bubble (`.msg-quote`).
- **Orchestrator:** the card and the editor tab use the same material. The team shows as chips per group so the thread gets room; the dot shows the status, a click inserts `@handle` or `@Group` into the command box without sending, right-click opens that agent's actions. The chevron opens the full list (remembered per browser), with row actions that appear when the cursor is over them and always show on touch screens. The card header shows the agent count and the most important status, the tooltip counts them all, and Pause all turns into Resume all once every agent has stopped; consoles that are at a shell are not counted. Long commands in the compact thread are folded with Show all.
- **Chats** (`chats/`, owner mockups 2026-11-17): every conversation an agent had in the project, and the lead's thread as a list of chats. A chat resumes only in the folder it was started in, so everything about it names its place.
  - The **Chats panel** (right rail, `--w-git-panel` wide): tabs **Agents · N / Lead (name) · N**; search (titles, and the hub's search in the transcripts), an agent select, and place chips (All places, Root, each part) in a project of several parts. Rows are grouped by day (Today, Yesterday, Earlier this week, Earlier): agent badge, title (semibold), then a small outlined place chip (`Root · all parts`, `web · This PC`, a server part with its server), the status as a dot and words (`live in @Arkan`, `asleep in @Daffa`, `closed`), size and age. One action per status: Show (live), Wake (asleep), Resume (closed); a chat whose part is gone or whose folder moved has a dashed warning chip (`legacy · part removed`) and Transcript / Continue elsewhere…. Rename, Transcript, Continue elsewhere and Hide are in the row's `…` menu, on right-click and on long-press. The foot says why: "A chat resumes where it started".
  - The **Resume dialog**: the facts in an input-colored box (Agent and account, Where = place chip + folder with "The folder it was started in", Last reply as a quote, Size), then radio cards: New console (under its old handle) or Into @X, an idle console of the same agent in the same folder (disabled with the reason otherwise), and "Tell it what changed since" with the hub's one line (default from Settings › Agents). When another console holds the old handle, a warning box names it and its chat, and the new console takes the next free name in an editable field ("The thread notes that this chat, once @Bintang's, is now in @Cantika"); Swap into @Bintang keeps the name.
  - **New console › Resume a chat**: the new console form (popover and sheet) has the tabs Start fresh / Resume a chat; the second lists the chats of the place picked, the armed agent's first. The editor's agent menus have **Resume a chat…** for the card's place; the console opens as a pane of that card.
  - **Lead chats**: every lead thread header (command bar, orchestrator card, the editor's orchestrator pane) has `<chat title> ▾` (an outlined `sm` button) and **+ New chat** (an icon on narrow headers). The list (popover, 420): CHATS WITH ARUNA · SHOP, the current chat marked `current` on `--surface-active`, the others with Resume, a mission chip in words, size and age; the command bar's list also holds the board's orchestrator cards' chats (a `card` chip; Resume shows the card); a New chat row at the bottom says the old chat is kept. New chat and Resume are disabled, with the reason as their tooltip, while the lead is answering in that thread. Only the active chat's messages show; a question still open from another chat is pinned above them ("Asked in another chat"), since the next command answers it from any chat. Where a chat was resumed the thread has a divider: the hub's note in `--text-3` between two hairlines. The Chats panel's Lead tab lists the chats something was said in, with Rename… and Delete… in the row's menu (not the chat in use, nor one a running mission or the lead's answer still writes to).
- **Missions** (`orch/MissionCard.tsx`): while a mission holds a thread (command bar, orchestrator card, the editor's orchestrator pane), its card is pinned between the thread's head and its messages, edged in the lead's `--wire` color like a proposal: flag, **Mission**, the state as a tinted chip in words (Awaiting approval and Waiting for you use the needs-you fill), done/total, the goal in two lines, the order progress bar, then `Round 4/30`, time left and open questions in `--text-3`; Pause or Resume and Stop (danger-ghost, confirm dialog) on the left, the open question's own buttons (Approve, Continue) on the right. The chevron opens details that scroll on their own: plan, Done when, tasks per agent (dot, title, `after t1`, state in words, result folded), and **Shared brief** and **Team conversation** toggle rows. The same card without controls is the mission's first message in the history. The approval (confirmation kind `mission`) shows goal, plan, tasks per agent with their instructions folded, and the brief, with no countdown. Team messages are compact rows with a 2 px left line: kind icon, `@from → @to` (the lead by its name), time, two lines of text; three or more in a row fold into **Team conversation · N messages** showing the latest. The final summary is a card with a `--c-primary-300` hairline and rendered Markdown. An orchestrator card's header has a `Mission · 3/5` chip while one is active. Touch: chevron and toggle rows are 40 px; there is no menu, so no long-press. Settings › Lead agent › **Mission limits**: rounds and hours, number fields with a unit.
- **Mission tile** (`board/tiles/MissionTile.tsx`, API-orchestration §22.5a): flag icon and title in the head, the active goal (or `Last: …`) as meta, a `Running · 2/4` chip and an icon-only Connect. Under it a small block segmented control **Active · New · History · N**, a body that scrolls as a whole, and a footer `Team: N consoles wired` with the lead's avatar and `Led by <lead>`. Active: the pinned mission card with details open and no inner scroll, then the lead's question (amber tint box) and a one-line answer box that grows to 8 em. No mission: a flag badge, **No mission running**, a line on what to do (wire consoles first when none are), **New mission**, and the last mission's card. New: goal (prose textarea), Done when, team chips plus **Wire more**, task boxes (`t1` in `--wire`, console select, title, instruction, **Starts after** pill toggles in the wire tint), **Add task**, **Add a shared brief** (mono), the review switch with a hint that says what Start does, the limits note, then Cancel and **Start mission** (or **Send to <lead>** when the lead reviews first). The first problem shows as a field error after the first Start. History: rows of goal and chevron over the state chip, task count and end time; open, the mission card and **Run again**.
- **"New terminal" dock**: glass `--glass-1` floating at the **bottom center** (bottom 16), radius `--r-xl`, padding 8, holding `sm` controls (height 32): machine select (with a status dot), preset select (preset color dot), cwd input group (folder icon + Browse button), name (optional), the primary button "New terminal". < 900 px: the dock becomes a round "+" button that opens a sheet/modal with the same form.
- **Place in** (a project of several parts only, `board/PlacePicker.tsx`): where a new console, group, editor card or run goes, "Root (all parts)" first, then each part by name in display order (none is marked as the main one). Up to four choices and short names it is a segmented control (`sm`, filling the width, names truncate); more, a select. A part's machine and folder are its tooltip and the hint line under it; a read-only part carries the lock icon. Its default is the focused tile's placement, else the last used, and the agent chip and the phone's New console button name it ("Claude Code · Root"). Dense rows (layout groups and lanes) use a `sm` select instead. A single-part project never shows it.
- **Part chips:** a console, file card or kanban task in a project of several parts shows its part as the small outlined `.node-part` chip; a console at the root shows "Root" with the layers icon. A console in no part's folder shows none: never the first part.
- Zoom controls at the bottom left & minimap at the bottom right: glass `--glass-1`, radius `--r-lg`.
- **Terminal node**:
  - Frame radius `--r-lg`, `--glass-border` border, `--e-3`.
  - 38 px header in glass `--glass-1` (no backdrop-filter per node, for pan/zoom performance — use bg `rgba(16,21,36,.92)`), a 2 px accent line at the top edge in the preset color (`session.color`) fading to transparent.
  - Header content: status dot → 600 `--fs-sm` title → "@ machine" `--text-3` → spacer → machine badge (when remote) → exit code (when ≠ 0, a danger chip) → `sm` menu icon button.
  - Solid body `--surface-solid`, padding 8, JetBrains Mono 13 px; xterm colors from the `--term-*` tokens (see 3.9).
  - Selected: ring `0 0 0 1px var(--c-primary-300), 0 0 0 4px rgba(24,86,255,.25)`.
  - The resize handle shows only on hover/selected, 10 px, color `--c-primary-300`.
- Board empty state: a small glass card in the center, an icon, "No terminals yet", one sentence, an arrow/text pointing at the dock.

**Card layering.** The card touched last rises to the top like a window. This is only `zIndex` on that client: it is not sent to the hub, it does not move the DOM (iframes and terminal focus are not disturbed), and a reload returns to the saved order. Bring to front and Send to back still change the saved order and win over it. Group frames, shapes, lines, and ink do not rise, because they are background. 

**Making room on expand.** An editor that fills the screen does not cover other cards: the cards to its right shift right together and the cards below it shift down, like inserting columns and rows, so the arrangement between cards stays and no new overlap appears. The shift is as far as the nearest card needs. Cards to the left and above stay still. A card that already covered the editor moves to the nearest empty spot. A group moves with its frame. A locked card stays still (the editor sits on top of it), as does a shape or line that surrounds the editor; pen strokes shift along but never push. Collapse returns everything by the same distance, so a card moved by hand in the meantime stays in its new place. `data.fill` stores the original size and the list of shifts, so this can still be undone after a reload.

### 3.6 Modal, menu, popover, toast

- Modal: `--glass-2` + blur-2, radius `--r-xl`, `--e-3`; width `sm 440` / `md 560` / `lg 720`; max-height `calc(100dvh - 48px)`, the body scrolls, sticky header & footer with a `--glass-border` divider. Enter: fade + scale .98→1 in `--t-base`. Focus is trapped inside; Esc closes; focus returns to the trigger.
- Menu/popover: `--glass-3` + blur-3, radius `--r-lg`, padding 6, 34 px items with radius `--r-sm`, danger items `--c-danger-300`, up/down arrow navigation.
- Toast: bottom right (above the minimap, bottom 16 + the minimap height), `--glass-3`, a 3 px left line in the kind's color (success/danger/info), disappears by itself after 4 s (error 7 s), `role="status"` / `role="alert"` for errors.
- **Studio** (Documents, Markdown, Skills): the list and its content side by side, stacked below 900 px, and one at a time on a phone: picking from the list shows its content with a back button named after that list (‹ Library, ‹ Notes, ‹ Skills) above it (`studio/StudioPane.tsx`). A studio opened for one item (a note from a file card) shows that item right away. Menus on a phone do not show keyboard shortcuts.
- **Phone:** a modal is a full-width sheet from the bottom edge with a 36×4 handle at its head; dragging the head down more than 96 px closes it. Footer buttons are sheet-wide. Menus close on `pointerdown` outside, so a tap on a touch screen closes them too. Esc in a menu opened from a modal closes only the menu. Toasts sit above the tab bar, full-width.

### 3.7 Status & badges

| Session status | Dot | Label |
|---|---|---|
| agent in a turn | `--c-success` + pulse ring | "Working" |
| shell running a command | `--c-success` + pulse ring | "Running" + the command name |
| agent at its prompt, shell with no command | `--c-grey` | "Idle" |
| starting / reconnecting | `--c-warning` | "Starting" / "Reconnecting" |
| exited (0) | `--c-grey` | "Exited" |
| exited ≠ 0 / error | `--c-danger` | "Exited (N)" / "Error" |

- 8 px dot; always with text/`aria-label` (color is not the only difference).
- **The header counter, board tabs, and project cards** count what is really processing (`session.active`), not every live process. Idle ones are only given as a number, and their list shows only through "Show N idle".
- Badge/chip: height 22, pill radius, padding-x 8, `--fs-xs` 500, bg in the color at 14% + text in the tint color (success example: bg `rgba(7,202,107,.14)`, text `#3EE08F`).

### 3.8 Auth pages (Setup, Login)

- Full-screen `--bg-ambient` background; a centered glass card 420 wide (Setup 520), padding 32, radius `--r-xl`.
- Brand above the card (40 px logo + "NextBoard" 800 `--fs-2xl`), subtitle `--text-2`.
- Setup: a step indicator (1 Admin · 2 Agents) as a horizontal stepper; step 2 shows the detected CLIs as a small bento grid (2 columns) with "Found"/"Not found" chips.
- Primary button `btn-block` height 44.

### 3.10 Memory page

Visual reference: Obsidian's graph view (nodes and links, hover lights up the neighbors and dims the rest) inside a galaxy. The sky is always dark; panels on top of it use normal glass.

- **Structure:** the memory of all projects at the core, each project a star on a spiral arm, sub-memories planets orbiting their star, headings small moons, critical notes in the nearest orbit. Arms, dust, and stars follow the same curve.
- **Shape first, color second:** stars are spiked, critical notes red diamonds with a slow pulse, sub-memories spheres, agent notes rings, headings dots. There is always a label and a legend, so kinds are not told apart by color alone.
- **Color:** per project follows a fixed order (`PROJECT_HUES`, oldest project first) and does not change when another project is opened. Past six projects, the color is neutral. Red only for critical notes.
- **Motion:** the orbits and the galaxy rotation are very slow and stop while the pointer is over a node. `prefers-reduced-motion`: everything is still and only draws when something changes.
- **The list on the left holds the same things** as the canvas and can be used with the keyboard: `/` searches, Esc goes back. The canvas itself is only a visual extra (`role="img"`).
- **A project of several parts** (API-orchestration §12): the list has **Whole project** (main memory, critical notes), then **Parts**, one row per current part with its machine. A row opens that part's memory; a part without one reads "No memory for <name> yet" and opens an empty editor that the first save turns into its page. Part pages are not listed again under Sub-memories, have no title or description to edit (the hub names them after the part), and are planets of their project in the galaxy, named after the part. The board's Memory panel picks the whole project or a part under **Which memory**; Scan and Init list their facts under Whole project and each part's name, and each fact is saved where it is listed. A project of one part shows none of this.
- **Narrow screens:** controls at the bottom within thumb reach, panels become sheets.

### 3.11 Project list (Home)

- **Two views, one markup** (`home/ProjectCard.tsx`): bento cards, or rows in a single glass panel (no blur per row, §6). Below 960 px rows stack like compact cards. The view and sort choices are saved per browser.
- **Toolbar** (filter by name/folder/group/tag, Host/Group arrangement, Recent/Name sort, Cards/List) shows from 4 projects or as soon as there is a group or tag: below that, everything is already visible.
- **Host or Group:** Host groups by machine, Group follows the group tree (owner reference, 2026-11-04). Each arrangement states on the card what its heading does not say: the machine under Group, the group under Host. While there is no choice yet, Group becomes the view as soon as there is a group. The choice is saved per browser.
- **Nested groups:** a group heading is an expand/collapse button, and collapsed ones are remembered per browser. Sub-groups hang on their parent's thin line. A group's own content comes first, then sub-groups. Projects without a group are in the last section, **All projects**. The ⋯ menu on a heading: New sub-group, Edit group, Delete group; deleting a group does not delete projects, its content moves up one level. The filter opens every matching group and hides the rest.
- **Tags** are neutral in color because they only tell things apart (§2.5). The bar below the toolbar filters by one tag, as do the chips on the cards; active = full ink. A card shows 3 tags and `+N`. The card menu **Group and tags…** saves the group and tags together, and can create a new group inside the selected group.
- **The project menu in the header** follows the same tree: Home, nested groups with project counts (→ open, ← close, stays open when clicked), then **All projects** for those without a group, the machine row on the right. A group holding the open project is always open, and focus starts at that project. The menu hangs from its button and scrolls, so it never rises to cover the button.
- **Views:** a segmented control above the list: **Projects** (the active ones), **Archived** (only while there are any) and **Groups** (only while there are any), each with its count, and **Select** on the right (§3.18). The page head's **Board archive** button opens the closed and cleared boards, which are not archived projects.
- **Anatomy:** cover across the top (cards only, not list rows) → icon or monogram + name + folder → one git row → footer (terminals, boards, last opened).
- **Cover title** (`home/ProjectArt.tsx`, `.cover-frame`): every cover, uploaded or made with AI, gets the title set over it by the card (an upload had none, so its name was missing), and under it the tagline: one set by hand in **Rename** (a second field under the name, empty = the cover's own), else the one Create with AI gave, the same on every card: a glass plate across the bottom of the cover (50% near-black, 12 px blur, a 12% white hairline, `--r-md`) with the same margin on the left, right and bottom (6.4% of the cover's height); in it the project's name without working labels in Outfit 600 at 13.2% of the height, white, and the tagline under it in Geist 500 at 7%, white at 74%, one line each with an ellipsis. Every project uses the same colours: an accent stored with the cover is not shown. Sizes are container-query units of the cover, so the card and the Create with AI preview match at any width. Cards in one grid row are the same height, so their footers line up. The git row never wraps: the branch is truncated, the status stays on the right.
- **Badge** (`home/badges.tsx`): one short label per project from a list all projects share (PROD, DEV, LIVE…), with a colour (red, orange, amber, green, teal, blue, violet, pink, gray; the same on both themes) and an optional icon of the app's set. Tinted glass in its colour, white capitals: on a card in the cover's top right corner, as far from the edges as the name's glass plate (24 px high); in a list row, the project switcher and a by-badge heading after the name (18 px). **Badge…** in the project menu picks one, makes a new one, changes one (on every project that has it) or deletes one (it comes off its projects). Home arranges projects by Host, Group or **Badge** (a section per badge A to Z, then No badge) and sorts them by Recent, Name or **Badge**; the project switcher (440 px wide) has the same **Group by** Group/Badge and **Sort projects** Badge choices. Badge choices show only while there is a badge.
- **Card body:** the icon and the ⋯ menu line up with the name (top), not with the middle of the block. Under the name its folder after a folder icon (or its parts line). Then one meta line, `--fs-xs`, wrapping when it must: where it is (host or group), its tags, and its git (branch or repositories, ahead and behind); never a line each. The foot is a quieter band across the card's bottom edge (`--card-foot-bg`) with the terminals (a dot: busy green or grey), the git state and the time last opened. The git state is a dot and a word (`.project-state`: Clean in green, `N changed` in warning colour), never a pill: on a card in its foot, in a list row in the Git column.
- **Cover and icon:** the cover is a 12:5 box bleeding to the card's edges, the picture `object-fit: cover`; without one, a quiet gradient in the project's monogram tone. The icon takes the monogram's box. Both boxes have their size before the picture loads, which fades in (`--t-base`), so nothing moves; a picture that fails to load leaves the fallback. **Cover & icon…** in the project menu: per picture Upload (the browser shrinks it first: cover ≤ 1600×1200, icon the middle square ≤ 256 px), Remove, and **Create with AI** only when `GET /api/art/capability` says an image tool is there, which first opens a small dialog (**Create: cover of <name>**, or **Create again: …**) with one optional **Context** textarea (prose font, 5 rows, an example per kind as placeholder, a hint and an `n/1000` count under it, the last text of that project and kind filled in; Ctrl+Enter creates) and Cancel / **Create** (**Create with this context** when something is written); while it runs, its progress, Cancel; afterwards **Create again**. The dialog's foot says what Create with AI sends to the model.
- **A project in parts:** every part equal, none "main". Where a one-part card shows its folder, one line says "6 parts · 3 machines" and then one row of part chips (name only, machine and folder in the tooltip), as many as fit and "+N", never a second line, so the card is as tall as a one-part card. The row is a button: it opens the list of parts (name, machine, folder, branch and git state) with Edit parts…. The git row adds the parts up: "6 repos" and "All clean" or "2 with changes", the branch only when every repo is on the same one, parts on an away machine named as offline. Under Group, where a one-part card names its machine, the chip says "3 machines" (their names in its tooltip) and the parts line leaves the machine count out, so the two cards stay as tall.
- **Remove dialog** (one project or many): two radio cards, **Archive (restore any time)** chosen first, and **Delete permanently** in red. It says how many running consoles stop and that folders and files on disk stay. Delete permanently lists what goes (consoles and scrollback, boards with archives and snapshots, memory, review and history, threads, loops, monitors, pasted files) and needs the word `delete` typed. Archived projects offer only Delete permanently. An archive toast has Undo.
- **Archived view:** one glass panel of rows: icon, name, folder (or the parts line), when it was archived; Restore and Delete permanently on each, and the select mode with the same two.
- **Groups view:** every group as a row, indented under its parent (at most five levels of indent), with its projects counted over its sub-groups and how many sub-groups it has; the name shows its projects under Group; the menu holds New sub-group, Edit, Select, Delete. Select mode deletes several groups; what was inside each moves up a level.
- **Monogram:** 1–2 letters from the name, color from the id (`--mono-N-*`) so it stays on rename. Its color only tells projects apart, so it does not use the status green, orange, or red.
- **The folder** loses its middle, not its end: the last part is what tells projects apart.
- **The whole card is a button**, so that button's tooltip holds the name, the full folder, and the branch. There is no dead area inside the card other than the menu.
- **Git status:** a folder holding several repos writes "3 repositories" and their total changes, not "No git repository". A machine that is not connected writes "Machine offline" without a request that is sure to fail.

### 3.12 Command bar and usage (owner reference, 2026-11-03)

Two deliberate exceptions to §2.5 and §6, only here: a gradient outline on the command bar chips, and a repeating animation while listening. The rest of the app keeps its original rules.

- **The input panel** (owner reference, 2026-11-04) is one composer box, from the top: the rows the order carries (targets, reply, attachments) when present, separated by a panel-wide line; full-width text with no border of its own; a toolbar with the thread button and agent pill on the left, mic and Send on the right. Focus is on the box: a `--c-primary` border and the input ring. The height stays at two rows even when empty, so it does not jump when a file is pasted, and the toolbar does not shift because the panel grows upward.
- **Quick actions** (owner request, 2026-11-18): one round button (44 px, 48 on touch; `--c-primary` fill, plus icon) left of the input panel, its bottom aligned with the panel. It replaces the row of chips above the bar. A press opens the actions upward, the first one (New console) right above the button: New console, Spawn 4, Group, Layouts, Auto arrange, Pause all, Resume all, Memory, and with the thread open Loops (with its count), Mission Map and Kill all. They fade up one after the other (22 ms apart; still with `prefers-reduced-motion`), and the plus turns into a close icon. It is a menu: `aria-haspopup="menu"`, `aria-expanded`, items `menuitem`; the first action takes the focus, ↑/↓/Home/End move, Esc closes and gives the focus back to the button. A press outside, the focus leaving, the button again or picking an action closes it. Kill all's first, arming press keeps it open, so the second press is right there. While agents are paused anywhere, the closed button has an amber dot, and Resume all is amber in the list. On a phone the Board tab has no command bar (its Insert offers these); the Lead tab's bar has the same button.
- **Chips** (the actions in that list): words only, no icons. Near-solid fill with no blur (§6: no per-element blur above the board), outline from `--cmd-ring-*`. Color only tells things apart: `Kill all` and the active chip still use status colors.
- **Listening:** the input panel turns into an orb, status, waveform, Cancel, and Stop; a transcript caption hangs below the panel. The mic button and Stop are the same element, so a press-and-hold still gets its release even when the row changes. `prefers-reduced-motion` keeps the orb and waveform still.
- **The microphone level** is measured in the browser only while listening, then the microphone is closed; audio goes nowhere because of this. Android hands the microphone to speech recognition, so there the motion follows the words coming in.
- **Usage:** one accent per CLI (`--usage-claude`, `--usage-codex`) through `data-agent`; amber and red (§10.1 API-studio) still win when the quota runs low, and always come with text. The first window becomes a ring, the rest bars. The heatmap uses quartiles of active days, so one big day does not fade all the other days.

### 3.13 Pasting and dropping files

The full contract is in API-studio §7.9. What is visible:

- Console: the path appears in the program's input as if typed; the agent CLI turns images into `[Image #n]` itself. No extra UI; toasts only for failures and for uploads above 2 MB.
- Order input (command bar, orchestrator card, orchestrator pane in Code): a pasted file becomes a chip in the top row of the box, separated from the text by a thin box-wide line, not a path in the text: an 18 px thumbnail, the name, the image or file size. An × button replaces the size in the same place while the pointer is over the chip or focus is inside it; touch screens always show ×. Clicking empty space in the chip row puts the cursor in the text. During upload the size is replaced by `Attaching…` and Send is disabled; an order may hold only files. Clicking a thumbnail opens its image. An order the hub refuses keeps its text and chips.
- Thread: images show above the order text at 120 px high and open at full size when clicked, other files become chips, paths are not shown. When a file has already been deleted (7 days), the image turns into a chip. *Edit and send again* brings its chips back.
- Explorer: a cut row (and its content) dims (`.wb-row.is-cut`) until pasted or Esc. Copy has no mark, as in VS Code.

**Files from the computer** (dropping, `lib/fileDrop.ts`): every place that takes a pasted file takes a dropped one the same way. A console (on the board, an agent pane in Code, the Code terminal panel) gets the file on its machine and its path typed, and keeps the keyboard. The lead's box (command bar and its thread, orchestrator card body and box, orchestrator pane in Code) attaches it as a chip. A Code editor saves it next to the open file and writes its name where it was dropped (a picture link in Markdown); a read-only file says so. The file tree saves it in the folder under the pointer, a file's row meaning its folder; the Root of a Root card says to pick a part first. While files are dragged over such a place, a dashed `--c-primary-300` frame covers it at `--bg` 92 %, with what a drop does there in the middle ("Drop to send to this console", "Drop to attach", "Drop to add here"), and the board's own drop outline goes. Anywhere else on a board, a drop still makes cards. A folder cannot be dropped: a browser hands over none of its files, and a toast says so.

### 3.14 Uptime card

The contract is in [API-uptime.md](API-uptime.md). The monitor list on the left, the selected monitor's page on the right; a card below 620 px shows only one side, through a container query, with a back button.

- **Status colors** are only `--c-success`, `--c-warning`, `--c-danger`, for up, pending, down, and never stand alone: badges carry an icon and a label, and failed heartbeats are also taller (60, 80, 100 %), so they can be told apart without color.
- **Heartbeat:** thin cells with a 2 px gap on one baseline; the newest on the right, empty slots stay visible. 24 in the list, 60 on the monitor page, with a description of the check under the pointer.
- **Response chart:** one series, so no legend: a 2 px `--viz-1` line, a 10 % wash, a dot at the latest value, a red band at steps that have a failed check, a broken line for times with no check. Hover and the left/right arrows read the points; the table button shows the same numbers. The pointer position is computed as a fraction of the width, because board zoom changes the on-screen size.
- The most urgent at the top of the list: down, pending, waiting, up, paused. A search box from 8 monitors.

### 3.15 Machines card

Cards are the same width and the same height per row, with no `span 2`: what is compared between machines must line up. This PC is always first, the rest by name, so cards do not move when their connection changes.

- **The status color** tints the card head (`--tint-*-bg` fading over 96 px) and the icon tile. The badge still names it in words, so color is not the only difference.
- **Anatomy:** head → three compact numbers (Terminals, Agents, Probed, on `--surface-input`, not glass on glass) → specs (label on the left, value on the right, hairline divider) → Agent CLIs → Persistence → footer. Persistence and the footer stick to the bottom, so they line up between cards.
- **Not probed yet:** one explanation panel, not a row of dashes.
- **`curl` is not an agent,** so it is not among the Agent CLIs chips. A server without CLIs is normal (agents are always on This PC), and its card says so.
- **Actions:** the footer holds Connect (only when not connected; primary only when disconnected or in error) and Re-probe. Rename, Orphans, Connection log, Select, then Archive and Delete permanently… are in the More menu, the last two set apart at the bottom and disabled, with the reason as their tooltip, for a machine in use.
- **Archive:** an **Archived** view (segmented control with counts, only while there are any) lists archived machines as rows with Restore and Delete permanently. This PC, and a machine used by a project part (active or archived) or a console, cannot be archived or deleted: in select mode (§3.18) its box is disabled and the card says "Used by acme-shop and 2 more" or "Has 2 consoles open".

### 3.16 Credentials card

The contract is in [API-vault.md](API-vault.md). Three columns: sections, list, item. Below 820 px the sections become a select, and below 560 px only the list or the item shows, with a back button; all through container queries.

- **Monochrome.** Color only for master password strength (`--c-danger`, `--c-warning`, `--c-success`), always together with a bar and a label, and red for delete actions.
- **No site icons:** a hairline box with a type icon, because fetching a favicon leaks the domain.
- **Secret values:** a fixed 12 dots, not following the real length, and not in the DOM until Show is pressed. A private key in a form is blurred until focused.
- **List rows:** copy username and copy password appear on hover or focus, and the text makes room, so they do not overlap. On touch screens they are always visible.
- **Master password strength** warns, it does not refuse; only fewer than 8 characters is refused.
- **PIN:** one masked field with wide letter spacing, a numpad on phones, sent at the sixth digit. Wrong: the field empties again and the remaining attempts are stated.
- **Fill login dialog:** those that match the site at the top, the rest set apart with "saved for …". A login for another site needs a second click, and its button is red. In This PC's Chrome there is a permanent warning that the site cannot be confirmed.
- **Window from the header:** a 960 px modal, up to 760 high, with the same content as the card (`vault/Panel.tsx`). New, Lock, and `…` are in the modal head before the close button; the item count disappears below 560 px. Closing the window does not lock the vault. Esc in the `…` menu closes only the menu.
- **Lock and create-vault screens** have no extra frame, sit in the center of the card, and scroll when taller than the card. The "cannot be recovered" warning uses `.alert.warning` and a required checkbox. On plain HTTP there is one small note line below the password field, which does not block anything.

### 3.17 Install as an app (PWA)

NextBoard can be installed to the home screen and run in its own window, without the browser bar. The hub is still required: nothing runs offline.

- **Manifest:** `public/manifest.json` (not `.webmanifest`, so the hub serves it as JSON without an extra MIME table). Its content: `display: standalone`, `start_url` and `scope` `/`, and background and `theme_color` `#0a0a0a`. The bar color still follows the theme through `<meta name="theme-color">` (`lib/theme.ts`).
- **Icons:** `public/icons/icon.svg` (rounded square, same as the favicon) and `maskable.svg` (full background, the mark inside an 80 % circle). The PNGs 192, 512, maskable 512, and apple-touch 180 are made from both with `node scripts/icons.mjs`; do not edit them by hand.
- **No service worker.** Chrome no longer asks for one to install. An offline cache risks serving an old bundle after the hub is updated, and a hub that is down still cannot be used.
- **Settings › Install app** (per device), with four states (`lib/install.ts`):
  - **Installed:** the page runs as an app.
  - **Ready:** the browser offers install (`beforeinstallprompt`), so the Install button opens its dialog. While that offer exists, the `…` menu on phones and tablets also has "Install app".
  - **Through the menu:** the address is secure but the browser has not offered yet, so the pane writes the steps per browser.
  - **Plain HTTP:** an `.alert.warning` explains that *Add to Home screen* only makes a shortcut that opens in a browser tab. The way out: open the hub over HTTPS (nginx or `tailscale serve`), or in Chrome on your own device add the hub address to `chrome://flags` › *Insecure origins treated as secure*. The hub still works fully over HTTP.
- The browser's install offer is not suppressed: Chrome may still show its own prompt.

### 3.18 Select mode

For acting on several at once: projects, archived projects, groups, machines (`components/SelectBar.tsx`, rules in `lib/selection.ts`).

- **In:** the **Select** button by the view switch, **Select** in an item's menu, a long press on a touch screen (`data-longpress`, §3.0), or Shift-/Ctrl-/⌘-click on a project card or row. **Out:** Cancel, Esc, switching views, or an action that finished.
- **Picking:** a click or tap toggles one and makes it the anchor; Shift-click adds the run from the anchor to it, in screen order; Ctrl/⌘-click toggles one. The view's **Select all** / **Select none** takes only what is shown: the filter's matches, not what a folded group hides. Whatever leaves the view (a filter, an archive elsewhere) leaves the selection, so an action never reaches what is not on screen. An item that cannot be picked (a machine in use) is skipped by runs and Select all.
- **On the items:** an 18 px box (the checkbox look of §3.2) at the top left of a card, before a row; a picked card gets a `--c-primary` border, a picked row `--selected-bg`. The whole card or row picks; its menu and tags step aside in place (`visibility: hidden`), so nothing moves.
- **Bar:** fixed at the bottom of the screen above the tab bar (`--shell-bottom`), glass-3, `--r-lg`: the count ("3 selected", polite live region), the actions, Cancel. Projects: Archive, Delete permanently (both open the remove dialog, §3.11, with that choice), Move to group (a menu of the groups). Archived: Restore, Delete permanently. Groups: Delete groups. Machines: Archive or Restore, Delete permanently. On a phone it spans the width and the count takes its own line. The page gains bottom padding so the bar never covers the last row, and toasts rise above it. Bulk requests answer per item; what went through is gone at once and what did not is named, with the reason.

### 3.19 Code view

A project as a full code editor, without the board (`studio/code/CodeViewPage.tsx`, route `/p/<project>/code`). It is the editor card's workbench (§3.5) full screen, with the same tabs and the same live editor state as the project's Root editor cards: closing a tab in one closes it in the other.

- **Shell:** no app header, rails, command bar or canvas. The page binds the project's board opened last in this browser (else its first) with a board controller and UI store, so terminals and agents started in the editor land on that board as from a card; nothing is drawn. The board stays bound while the page is open, whatever board another tab opens; only if that board goes does the page bind another (its tabs stay, they are the project's). `CodeStudio` runs with `mode="page"` and `nodeId=""` (no card: the Root placement). No card chrome: no title bar, Fill or Close. Nothing adds cards to a board here: Add to Board is not offered, and a web page (the Ports panel's globe, labelled **Open in a tab** here) opens as a Browser tab on the machine of those ports. A chat continued elsewhere from the Chats tab opens its new console as a tab.
- **Islands** (wide screens, not a phone; the Code view page and the editor card on the board alike): the activity strips, the top bar (or the card's title bar) and the status bar lie on the backdrop (the card has the page's glow as its own); the side bar, the editor area, the panel and the lead-and-agents side bar are islands (`--tile-bg`, the editor and panel in `--surface-solid` like Monaco, `--r-island`, hairline, `--tile-shadow`) 6 px apart. The gaps are the sashes (a 2 px line along 40 % of the gap under the pointer). Tab strips hold 30 px pill tabs (`--r-md`, a hairline): the active one `--surface-active` with `--glass-border-strong`, in the focused group the ink. Tree rows are 26 px, rounded 7 px, inset from the island's edges; a part's row is a small uppercase heading with its machine on the right. The panel's tabs are pills; a terminal's instances are small pills along the top of the panel, New terminal after them. The active activity button is a small raised island. The lead side bar's Lead | Agents is a segmented pill. The status bar shows the branch, the problems and the active file's path (› between the parts; a click shows it in the tree).
- **Top bar** (the page, on the backdrop, 48 high): brand; the project as a chip with no box (its icon or monogram, its name, `· N parts`, chevron); the **branch chip** (branch, ↑ ahead ↓ behind; opens Source Control); **Board | Code** as a pill track, the current view raised; **Go to file** in the middle (at most 440 px, its key in a small mono box); **Run**: in a pill track, a chip with the last task run (else "Run task", opens the task picker), **Run without debugging** (Ctrl+F5, green) and **Start debugging** (F5); **Save N** (primary) while files are unsaved; the usage, active and Live pills (one height, 28 px); **Search in files**, **Settings** (the NextBoard settings page) and the hub's **avatar** (its first letter, the More menu: pages, theme, About this hub, Log out). Below 1100 px the branch and run chips go. The side bar, panel and lead toggles are on the strips: the left strip ends with **Terminal panel** (Ctrl+`), **Problems** (with the count) and Settings; a right strip holds the **bell** (the consoles that need you: a dot while any does, a click jumps to the next), **Lead and agents** (Ctrl+Alt+B), **Commit graph** and, at its foot, **Side bar** (Ctrl+B). An editor card keeps its toggles in its title bar.
- **Download** (the Explorer's file menu, after Local History, and a file tab's menu): saves the file as it is on disk through `fs/raw?download=1`, under its own name; in the tree it is disabled over 20 MB (the hub's cap), on a tab with unsaved edits its hint says to save first.
- **Inside the islands:** the Explorer's head says **Project** (the project's name and folder in its tooltip). Files without an icon theme show a 16 px badge of their type (TS, TX, JS, {}, MD, CS, GO, …) in the type's tone, in the tree, the tabs and Go to file; folders are a muted blue-grey. The editor and the panel's terminals have the islands' own surface (`--code-bg`, read by Monaco and, inside the panel, as `--term-bg`). The panel's tabs: Terminal and Problems with an icon, counts as plain small numbers; at its right, while Terminal shows, **New terminal** (+), the terminal menu (split icon) and Close; the terminals are listed on the right of the one shown, as before the islands (rounded rows, the one shown with a hairline ring; Move to the editor area and Close on each row). The lead side bar: Lead | Agents, then **New chat** (+) and a ⋮ menu (New chat, Hide the side bar); the lead's head is its avatar, its name and, under it, the current chat as a quiet switcher; the composer is one card: the chips that go with the order, the text ("Talk to … @ to reach an agent"), then + (Quick actions), @ and Send. The status bar shows the branch, errors in red and warnings in amber while there are any, and the active file's path.
- **Top bar, narrow screens:** below 1100 px quick open is an icon and the usage pill goes; below 800 px Board | Code are icons and the Live text goes.
- **Activity bar:** after Ask, a 24 px hairline separator, then the tools (Chats, Memory, Studios, Vault, Browser, Loops, Mission Map). Each opens as an editor tab, and one already open is focused; Studios opens a menu of Documents, Diagrams, Markdown and Skills beside the bar. The button of the active tab's tool is lit. The palette has the same as `View: Open <tool>`.
- **Right side bar:** the lead and the agents (Lead | Agents). Open or closed is the Code view's own, apart from the project's Root cards (`nb.codeview.right.<project>`): the first time a browser opens a project's Code view at 1000 px or wider it shows.
- **The lead's UI actions** (`studio/code/pageActions.ts`) run in the editor: a file opens in it; Memory, Loops, Mission Map, Chats and the studios open as tool tabs; Git shows Source Control; focusing a console opens it as a tab. What only a board shows (fit, zoom, a card, undo) gives a toast with **Open the board**.
- **Project switcher:** the board header's menu (groups, Sort, the current project checked) under the heading "Switch project · opens in Code view": a project picked opens its Code view, so the view stays the same. Below the projects, "Open <project> in Board view" and "All projects (Home)". With several parts the button says how many ("Shop · 3 parts"). An editor card on a board has no switcher: its title only says the project (and the part, `Shop · admin`), since the card belongs to that board's project; the board header's switcher and Board | Code are there for that. The board header's own switcher opens a project as the setting says (below); its current project stays on the board.
- **Phone** (the phone layout, or the page under 640 px), made for one hand:
  - top bar: switcher, Board | Code, `⋯` (with the status bar's safe area above it);
  - one editor group at a time; its tabs scroll sideways;
  - what would go to the right side bar (`studio/code/pageSide.ts`: `showLead`, `showAgents`, `showPane`, `placePaneSide`) opens the Lead or Agents sheet, or a console as a tab;
  - bottom navigation (the tab bar's look, in the page's flow, `--wb-bar`): **Files** (the side view full screen with the activity bar; its last item `⋯` is the Tools menu with every tool and the four studios), **Editor**, **Terminal** (the panel at full height), **Lead** and **Agents** (sheets from the bottom edge, 80 % of the height, the side bar's own panes). A file, tool or problem opened from Files shows the Editor; a terminal started shows Terminal; the panel closed from its bar goes back to the Editor. No status bar. The bar gives way to the on-screen keyboard.
- **Opening a project:** a Home card, the board header's project menu and a link `/p/<project>` that names no view (typed, bookmarked or from outside, the page a tab opened on) go where the hub setting `ui.openProjectsIn` says: the board, the Code view, or, by default, a question. The app's own ways to a board (a console's jump, workspace tabs, a merged project's old link) still open the board.
- **"Open <project>"** (`home/OpenChoice.tsx`, modal `lg`, a sheet on a phone): "How do you want to work on this project?", then two picture cards as a radio group (`role="radio"`, arrows move): **Board view** "Consoles, agents, cards and the lead on one canvas. As today." and **Code view** "A full code editor: files, terminals, agents and the lead in one window, no board." The picture is a small drawing of the view in theme colours (hidden on a phone); the chosen card has a `--c-primary` border on `--selected-bg` and a filled check box. Board view is chosen first. Below, **Remember my choice for every project** (ticked) with the hint "Change it any time in Settings › General › Open projects in. A project card's menu always offers the other view." Footer: Cancel, and the primary **Open in Board view** / **Open in Code view**, which follows the choice. A double click on a card opens it. Remember writes the setting before the view opens. Cancel from a link goes to Home.
- **Board header** (the islands header, also on Home and Settings): the project as a chip with its icon and `· N parts`; the workspaces and their + as one pill track, the current one raised; beside it **Board | Code** as a pill track (Code opens this project's Code view; below 900 px icons only, none on a phone); on the right the usage, needs-you, Live and connection pills, the vault, **Settings**, the build label (About this hub) and the hub's **avatar** with the More menu (Projects, Memory, Machines, Devices, Settings, theme, About this hub, Log out). Segmented controls everywhere are a pill track whose current choice is raised (`--tile-bg`, `--raise-shadow`, a hairline), not inverted.
- **Board islands:** a group is a dashed frame (18 px corners, a 5 % tint of its colour) with its name, role, count and Link input as a floating glass pill on its top edge. Wires glow softly in their own colour. A console's state in its header is a word in a soft pill (not capitals). Kanban columns are soft wells (`--surface-hover`, no coloured edge: the colour is on the column's dot) with raised cards (`--card-raise`); a done check is the ink.
- **Project card menu:** always **Open in Code view** and **Open in Board view** first, then a separator, whatever the setting.
- **Settings › General › Open projects in:** radio cards in one column: Ask every time, Board view, Code view, each with one line of description, and the hint "Kept on the hub, so every browser and phone follows it." The first pane of Settings.

### 3.20 NextCode chat (2026-11-20)

NextCode (docs/API-agents.md §30) is an agent whose console is a chat drawn by NextBoard instead
of a terminal (`web/src/nextcode/`, `styles/nextcode.css`; mockup outside the repo).
- **Where:** it replaces the terminal body wherever a console is drawn: the board card (its header,
  AgentBar and handles stay), a Code view tab, the bottom panel, and the phone.
  - Variants: `tab` (wide, with a toolbar), `card`, `phone`.
  - A terminal console looks exactly as before.
  - The key bar shows nothing for it.
- **Mark:** "N›" in Geist Mono on a raised square, with a dot in `--nextcode`. That violet is the
  agent's colour only, never an accent: buttons stay ink.
- **Toolbar** (the `tab` variant): the model as a chip, the permission mode as a segmented control
  (Ask, Accept edits, Plan, Auto, Bypass), and the cost on the right.
- **Messages:**
  - the owner's are right-aligned bubbles (`--sel-strong`); the agent's text is Markdown at 13.5 px;
  - thinking folds into "Thought";
  - small tools (Read, Grep, Glob, …) are 30 px rows; Edit/Write are cards with the diff; Bash shows
    its command and the end of its output;
  - a sub-agent's calls group under its Task call;
  - while text streams, a still caret ends it (only the busy dot animates, §2.5).
  - streamed text shows smoothly (`nextcode/smooth.ts`): it comes in bursts, and each burst is spread over the next ~260 ms of frames (~140 ms once the reply is complete, never under 90 characters a second), so it reads as a steady flow without falling behind. The finished reply's item takes over where its stream was. Only the last block of Markdown is rendered again per frame; the blocks before it are done. A hidden tab shows the text as it is.
- **Requests:** the needs-you card (the `--needs` tint and glow, like a needs-you tile):
  - **permission:** Allow once (primary), Always allow (only when the CLI offers a rule), and
    Deny… on the right; Deny… opens a note box with Deny and tell it / Deny and stop;
  - **question:** radio or checkbox options with their descriptions, Other…, Answer;
  - **plan:** the plan as Markdown with Approve and start / Approve, accept edits / Keep planning….
- **Bottom:**
  - a live line: the state dot, "Working · <activity>" / "Needs you · <what>" / "Ready", and the
    turn's time;
  - the composer (the lead composer's card): Enter sends, Shift+Enter is a new line, "/" lists the
    CLI's commands, Stop while it works.
- **Model menu:** the model chip (the toolbar in `tab`, the composer bar in `card` and `phone`)
  shows "Model · effort" and the engine ("Claude"/"Codex").
  - It opens a menu: Default, the agent's models, Other… (a typed name checked like the spawn
    form's), and the efforts of the model.
  - A new model switches at once; a new effort says "Applies to the next message".
- **Attachments:** paste into the composer, drop on the chat ("Drop to attach to your message"),
  or use `+`.
  - Chips with a thumbnail or file icon, name and size, removable; Send waits for the uploads.
  - The message ends with "Attached: <paths>", which a sent bubble shows as chips again.
- **@file:** `@` lists the console's files (its part, every part at a root, or its folder),
  fuzzy matched like Go to file; Enter or Tab inserts `@path`.
- **Engine:** wherever NextCode is chosen (Spawn, All agents, Agent and model…, new console), it
  is one entry with an Engine control (Claude | Codex).
  - Codex is hidden when the hub has no Codex engine, and disabled when Codex is not installed.
  - Lists without the control say "NextCode · Claude" / "NextCode · Codex".
- **Not logged in:** the error card says NextCode's login is its own and offers **Sign in
  NextCode**, which opens the sign-in dialog of the console's login right over the chat (Settings
  › NextCode on an API provider: "Open API providers"). Done after signing in sends the message
  that failed again; once the agent has replied after it, the card leaves the chat.
- **Settings › NextCode:** its own pane after Agents, with these cards:
  - one card per engine ("Claude engine", "Codex engine"): its logins (NextCode's only; each
    home one is named "NextCode"), Add login, and its model and effort defaults;
  - a shared **Defaults** card: the start permission mode as a segmented radio group, and the
    idle time in minutes.
- **Card menu:** a NextCode card has no Copy text, Pick conversation, Pause, Sleep, Keep awake,
  Text size or Kill. Its Restart says that it stops the CLI, and Close says "Close console…".
- **API engine** (2026-11-21, agent `nextcode-api`): NextCode's own agent, with no vendor CLI and
  no login, on one of the hub's API providers. It is the third choice of the Engine control
  (Claude | Codex | API), always enabled on This PC and absent on servers. Its account select lists
  the API providers only (every one can run it), preselecting Settings › NextCode's provider; with
  none, the `nextcode_needs_provider` error card offers Settings › API providers. The model menu
  lists the provider's models and the efforts low to max, and names the engine "API". Settings ›
  NextCode has an "API engine" card: its default provider (`nextcode.provider`) and its model and
  effort defaults; no logins.

### 3.9 Terminal palette (xterm)

From the `--term-*` tokens in `tokens.css`, read by `useTerminal` from the console element, so it follows the `data-theme` around it. Changing the theme does not recreate the terminal; the screen content stays.

```
dark    background #111111  foreground #E5E5E5  cursor #F5F5F5  selection rgba(255,255,255,.24)
        black #262626  red #F87171  green #4ADE80  yellow #FBBF24  blue #60A5FA  magenta #C084FC  cyan #22D3EE  white #D4D4D4
        brightBlack #737373  brightRed #FCA5A5  brightGreen #86EFAC  brightYellow #FDE68A  brightBlue #93C5FD  brightMagenta #D8B4FE  brightCyan #67E8F9  brightWhite #FFFFFF
light   background #FFFFFF  foreground #1F1F1F  cursor #0D0D0D  selection rgba(0,0,0,.14)
        black #171717  red #B91C1C  green #15803D  yellow #A16207  blue #1D4ED8  magenta #7E22CE  cyan #0E7490  white #525252
        brightBlack #737373  brightRed #DC2626  brightGreen #16A34A  brightYellow #CA8A04  brightBlue #2563EB  brightMagenta #9333EA  brightCyan #0891B2  brightWhite #262626
```

**Console on touch screens** (`board/KeyBar.tsx`, `board/consoleKeys.ts`): while a console is in Focus, or while the keyboard is in a console, a glass `--glass-3` key bar sticks above the tab bar (and above the on-screen keyboard, which pushes everything up). Order: Esc, ↑, ↓, ⏎, Tab, Ctrl, ^C, ←, →, ⇧Tab, Alt, `/ | ~ -` (scrolling sideways), then, past a divider: Copy text, Attach, and keyboard. Keys are 44×40 in a mono font; pressing one does not take focus, so the keyboard stays open. Ctrl and Alt are one-shot: lit (`aria-pressed`, white on dark) until the next key typed, then off. Arrows follow the program's cursor mode (`ESC O` or `ESC [`). Copy text opens a sheet holding the screen and scrollback (the last 2000 lines) as plain text that can be selected with a finger, plus Copy all. Attach uploads a file or photo to the console's machine and types its path, the same as paste. The command bar also has an Attach button. Terminals in the Code card panel get the same bar.

**Code editor on touch screens** (`studio/code/EditorKeyBar.tsx`): while the keyboard is in Monaco, a bar of the same shape holds Esc, Tab, ⇧Tab, ←, ↑, ↓, →, Sel, Undo, Redo, Find, `//` (comment), then Save, Go to file, and keyboard. Esc, Tab, and the arrows are sent to the editor as key presses (with `keyCode`, which Monaco reads), so they do what that key does there: accept a suggestion, close a widget, vim normal mode. Sel makes the arrows select text until it is turned off. Undo, Redo, Find, and comment call editor commands. Monaco's focus can be a textarea or an EditContext element, depending on the browser; both count.

**Console in the light theme.** A CLI set up for a dark terminal prints very light text, sometimes truecolor that does not go through the palette. `--term-min-contrast: 4.5` (xterm `minimumContrastRatio`) darkens such text per cell. Settings › Appearance › Terminals can choose "Always dark" for CLIs that are still unpleasant to read.

**Pointer under board zoom.** xterm measures the pointer in screen pixels and its cells in layout pixels, with no option to change that, so outside 100 % zoom the selection lands beside the pointer. `followBoardZoom` (`board/useTerminal.ts`) wraps its private methods: `_mouseService.getCoords` and `getMouseReportCoords` (selection, mouse reports to the program, links), `_selectionService._getMouseEventScrollAmount` (auto-scroll while dragging), and `_sliderPointerPosition` (scrollbar thumb). It applies to terminals in the Code card too. After an xterm upgrade, check the browser console: if a method name changed, a warning appears and zoom is off again. Monaco already computes its own scale.

**GPU renderer** (`board/webglPool.ts`). Browsers cap live WebGL contexts (Chrome 16 on a desktop, 8 on Android; Safari 16 and never restores a lost one) and drop the one that drew least recently, so consoles share a budget below the cap: 12 on a desktop, 8 on an iPhone or iPad, 6 on Android. The consoles on screen get it, the focused one first, then the largest; the others are drawn by xterm's DOM renderer. A console off screen keeps its context until another needs it, and one given WebGL keeps it at least 2 s unless the focused console needs it; while panning at most 4 contexts are made per tick (consoles that open together get theirs at once). A lost context is released at once and that console tries again after 2 s, doubling to 64 s; a loss while the pool holds contexts lowers the budget to what is left for the session (not when all are lost at once, which is the GPU resetting). A software GPU (SwiftShader, llvmpipe) gets no WebGL because the DOM renderer is faster there. `localStorage` `nb.webgl` = `force` allows it on a software GPU, `off` turns it off; `nb.webglMax` sets the budget. A swap never changes the columns and rows: the DOM renderer uses the WebGL renderer's cell grid (whole device pixels), so its text sits a little tighter than xterm's own DOM layout, and the PTY is never resized for a swap. The WebGL renderer sizes its canvas from the device pixels the browser reports (`devicePixelContentBoxSize`). In Chrome DevTools device mode and test browsers, the pixel ratio is emulated and CSS pixels are reported, so the canvas is only half its size: text falls outside the canvas and the console looks empty. The pool observes each WebGL terminal with the same size; when it does not match `contentRect × devicePixelRatio`, no console of the page gets WebGL again and they are redrawn with the DOM, including when device mode is turned on after the page has opened. `e2e/console-gl.spec.ts` forces WebGL in the test browser: the board console and the agent in the editor tab must show text in all five profiles. `e2e/console-pool.spec.ts` pans a board of 18 consoles: no "Too many active WebGL contexts", at most 12 contexts, every console on screen drawn by WebGL, no size sent to the hub for a swap, no swap that shows a frame without text, and a closed focused console is garbage-collected.

**xterm patches** (`board/xtermPatches.ts`). Our own fixes for xterm.js bugs still open in @xterm/xterm 6.0.0 and @xterm/addon-webgl 0.19.0, each naming its upstream issue: a renderer set or a canvas cleared while the terminal is off screen is drawn at once (#6239), one cell grid for both renderers (#6015), disposing frees the cursor-blink timer, the paused resize, the shared atlas's scratch canvas and the WebGL context (#5817, #5818, #6068), a lost context is never restored (#6257), atlas page merges and clears no longer make every console redraw in full each frame or garble the others (#6038, #6014), texture slots past a shrunk atlas are freed (#6306), no mipmaps (#5987), and merged pages stay at 4096² (#6187). Like `followBoardZoom`, each patch checks the private names it uses; after an xterm upgrade, check the browser console: a patch whose names changed is skipped with a warning saying what goes wrong again. Delete a patch once a release has its fix.

**Double click** inside card content (`.nodrag`: console, editor) belongs to the content, for example to select a word, and the board does not move. A double click on an empty spot of a card's header (every card: consoles, groups, the Code editor's title bar, and every `TileHead`) zooms the board to that card, as Focus does. On the title it renames, and on a header button or field it stays that control's.

## 4. Accessibility (test criteria)

- [ ] Body text ≥ 4.5:1, large text (≥ 18 px or ≥ 14 px bold) & UI components ≥ 3:1 against their effective background (measure on the brightest ambient glow).
- [ ] Every interactive element is reachable by Tab, in a logical order, and shows a 2 px `--c-primary-300` focus ring.
- [ ] Modals trap focus, Esc closes, focus returns to the trigger. Menus can be navigated with arrows.
- [ ] Segmented controls & radio cards use `role="radiogroup"`/`radio` with `aria-checked`, arrows move the choice.
- [ ] Every input has a linked `<label>`; errors use `aria-invalid` + `aria-describedby`.
- [ ] Touch targets ≥ 40×40 under `data-input="touch"` (checked by `e2e/layout.spec.ts` for the header and tab bar; the board dock may be 32 with a gap ≥ 8 until the board gets its turn).
- [ ] `prefers-reduced-motion` turns off transitions & the pulse; `prefers-reduced-transparency` / no `backdrop-filter` → solid surfaces.
- [ ] Status is not told apart by color alone (there is text/aria-label).
- [ ] 200% browser zoom does not cut off forms or hide action buttons.

## 5. Tone & writing

- Concise, confident, friendly, professional. Active sentences.
- Label = a clear noun ("Host", "Private key"). Button = verb + object ("Add machine").
- Error = what went wrong + how to fix it: "Host key changed for devbox:22. Connection refused — verify the server before trusting it." Wrong example: "Error 409".
- Empty state = what this is + one action: "No machines yet. Add one from your ~/.ssh/config."
- The web UI language is currently English; do not mix two languages on one screen.

## 6. Anti-patterns (forbidden)

- Glass on glass on glass (> 2 stacked blur layers) — readability drops and the GPU works hard. Example: a glass menu inside a glass modal is fine; a glass card inside a glass modal is not (use `--surface-input`).
- `backdrop-filter` on every terminal node (pan/zoom becomes choppy).
- Text below `--text-3`, placeholders as labels, `outline: none` without a replacement.
- Raw colors/spacing/radius in components (`#333`, `margin: 7px`, `border-radius: 5px`).
- Decorative animation (shimmer, floating, parallax), rainbow gradients, neon glow on text. Exception: §3.12.
- Heavy black material-style shadows mixed with glass.

## 7. Migrating from the old UI

1. Replace the old `:root` with the tokens above (`styles/tokens.css`); map: `--panel*` → `--glass-*`/`--surface-*`, `--accent` → `--c-primary-300` (text) / `--c-primary` (fill), `--green/--yellow/--red` → `--c-success/--c-warning/--c-danger`.
2. Header + toolbar merged in one row → split: a floating header + the board dock at the bottom.
3. `<select>` for auth method / persist / source tab → radio cards / segmented.
4. Devices table → a list of bento cards.
5. System font → local Plus Jakarta Sans + JetBrains Mono.

## 8. QA checklist (code review)

- [ ] No raw colors/spacing/radius outside the token file (grep `#[0-9a-f]{3,6}` in components = 0, except preset colors from the API, board content colors, and the Memory sky).
- [ ] Checked in the light and dark themes, on a production build: the minifier shortens token values (`#ffffff` becomes `#fff`), so code that reads tokens must accept both (Monaco refuses the short form).
- [ ] Every state (hover, focus-visible, active, disabled, loading, error) exists for buttons & inputs.
- [ ] Screenshots at 1440×900 and 390×844: Setup, Login, empty Board, Board with 2 terminals, node menu, Folder browser, Machines, Add machine (two tabs, auth key), Host key confirm, Host key changed, Devices, Pair, About, error toast.
- [ ] A new page, pane, dialog, or card type goes into the `e2e/sweep.spec.ts` list. Look at the screenshots of all five profiles: `SWEEP_SHOTS=<folder> npx playwright test -c e2e/playwright.config.ts sweep`.
- [ ] The fallback without `backdrop-filter` looks tidy (test by turning it off in DevTools / the `no-glass` class).
- [ ] Lighthouse a11y ≥ 95; no console errors.
- [ ] Board pan/zoom with 6 terminals stays smooth (no blur per node).
