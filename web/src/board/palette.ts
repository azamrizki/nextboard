// User content colours. These hex values are *stored in board data* (strokes, fills,
// text colour) and shared with other clients, like preset colours from the API — so
// they live here rather than in tokens.css (DESIGN.md §8 allows content colours).
// Named colours (notes, kanban columns, calendar events) map to CSS tokens instead.

export interface Swatch {
  value: string;
  label: string;
}

/** 8 ink / stroke / text colours. The values were tuned for the dark canvas; see inkColor. */
export const INK_COLORS: Swatch[] = [
  { value: '#e6eaf2', label: 'Chalk' },
  { value: '#8a93a6', label: 'Slate' },
  { value: '#6e95ff', label: 'Blue' },
  { value: '#b49cff', label: 'Violet' },
  { value: '#4fd6e8', label: 'Cyan' },
  { value: '#2fdc8a', label: 'Green' },
  { value: '#f2b27a', label: 'Amber' },
  { value: '#ff5c77', label: 'Red' },
];

const INK_TOKENS: Record<string, string> = {
  '#e6eaf2': 'var(--ink-chalk)',
  '#8a93a6': 'var(--ink-slate)',
  '#6e95ff': 'var(--ink-blue)',
  '#b49cff': 'var(--ink-violet)',
  '#4fd6e8': 'var(--ink-cyan)',
  '#2fdc8a': 'var(--ink-green)',
  '#f2b27a': 'var(--ink-amber)',
  '#ff5c77': 'var(--ink-red)',
};

/**
 * What to paint for a stored colour. A board stores the hex and is shared between clients, each
 * with its own theme, so the swatches are told apart here: chalk on a white canvas is dark ink.
 * Any other colour (from an import, say) is what it says.
 */
export function inkColor(stored: string): string {
  return INK_TOKENS[stored.toLowerCase()] ?? stored;
}

/** Diagram group colours (stored in board data; same order as the hub's landing). */
export const DIAGRAM_GROUP_COLORS: string[] = ['#6e95ff', '#b49cff', '#4fd6e8', '#2fdc8a', '#f2b27a', '#ff5c77', '#8a93a6', '#6e95ff'];

/** Colourful diagram arrows, one per source, so that neighbours differ (same order as the hub's landing). */
export const DIAGRAM_WIRE_COLORS: string[] = ['#6e95ff', '#f2b27a', '#2fdc8a', '#ff5c77', '#4fd6e8', '#b49cff'];

export const FILL_COLORS: Swatch[] = [{ value: 'transparent', label: 'No fill' }, ...INK_COLORS];

/** Pen widths (px in board space). */
export const INK_WIDTHS = [2, 4, 6, 10, 16] as const;

export const NOTE_COLORS = ['yellow', 'pink', 'blue', 'green', 'violet', 'orange'] as const;
export type NoteColor = (typeof NOTE_COLORS)[number];

export const TAG_COLORS = ['amber', 'sage', 'sky', 'violet', 'rose', 'teal'] as const;
export type TagColor = (typeof TAG_COLORS)[number];

export const TAG_LABELS: Record<TagColor, string> = {
  amber: 'Amber',
  sage: 'Sage',
  sky: 'Sky',
  violet: 'Violet',
  rose: 'Rose',
  teal: 'Teal',
};

/** Markup editor: 8 colours × 4 widths (DESIGN: content colours). */
export const MARKUP_COLORS: string[] = ['#ff3b5c', '#ff9f43', '#ffd43b', '#2fdc8a', '#4fd6e8', '#4c7dff', '#b49cff', '#ffffff'];
export const MARKUP_WIDTHS = [2, 4, 8, 14] as const;
/** Dark halo behind markup text so it reads on any image. */
export const MARKUP_TEXT_HALO = 'rgba(0,0,0,0.55)';
