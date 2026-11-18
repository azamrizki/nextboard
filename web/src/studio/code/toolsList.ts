// The tools of the code view (docs/API-studio.md §7): what its activity bar adds after Ask, in
// this order, and the title and icon of a tool's tab. A click opens the tool as an editor tab
// (ws.openTool), or focuses the one already open. Studios is one item whose click opens a menu of
// its four studios. ToolTab.tsx renders what a tool tab shows.

import type { IconName } from '../../components/Icon';
import { externalUrl, localTarget, targetLabel } from '../../web/model';
import type { ToolKind } from './toolKinds';

export interface ToolItem {
  kind: ToolKind;
  icon: IconName;
  label: string;
  keys?: string;
}

/** An item of the activity bar. With `menu`, a click opens these instead of a tab (Studios). */
export interface CodeViewTool extends ToolItem {
  menu?: ToolItem[];
}

/** The studios behind the one Studios item, as the board's Studios menu has them. */
export const STUDIO_TOOLS: ToolItem[] = [
  { kind: 'documents', icon: 'book', label: 'Documents' },
  { kind: 'diagrams', icon: 'diagram', label: 'Diagrams' },
  { kind: 'markdown', icon: 'markdown', label: 'Markdown' },
  { kind: 'skills', icon: 'puzzle', label: 'Skills' },
];

export const CODE_VIEW_TOOLS: CodeViewTool[] = [
  { kind: 'chats', icon: 'message', label: 'Chats' },
  { kind: 'memory', icon: 'brain', label: 'Memory' },
  { kind: 'documents', icon: 'book', label: 'Studios', menu: STUDIO_TOOLS },
  { kind: 'vault', icon: 'key', label: 'Vault' },
  { kind: 'browser', icon: 'globe', label: 'Browser' },
  { kind: 'loops', icon: 'loop', label: 'Loops' },
  { kind: 'mission', icon: 'map', label: 'Mission Map' },
];

/** Every tool on its own, the studios opened up: a phone's ⋯ Tools menu, the command palette. */
export const TOOL_ITEMS: ToolItem[] = CODE_VIEW_TOOLS.flatMap((t) => t.menu ?? [t]);

/**
 * A browser tab's argument: its address, and, when it was opened for one (the Ports panel of a
 * part on a server), the machine whose localhost it shows: "@<machine> <address>". A plain address
 * (every tab before, and one typed) has no machine: the tab picks one as before.
 */
export function browserArg(url: string, machineId?: string): string {
  const u = url.trim();
  return machineId && /^[A-Za-z0-9_-]{1,64}$/.test(machineId) ? `@${machineId}${u ? ` ${u}` : ''}` : u;
}

export function parseBrowserArg(arg?: string): { url: string; machineId?: string } {
  const raw = arg?.trim() ?? '';
  const m = /^@([A-Za-z0-9_-]{1,64})(?:\s+|$)(.*)$/s.exec(raw);
  return m ? { url: m[2].trim(), machineId: m[1] } : { url: raw };
}

/** "http://localhost:5173/cart" → "localhost:5173", "https://example.com/a" → "example.com", none → "Browser". */
export function browserTitle(arg?: string): string {
  const raw = parseBrowserArg(arg).url;
  if (!raw) return 'Browser';
  const local = localTarget(raw);
  if (local) return targetLabel(local);
  const ext = externalUrl(raw);
  if (ext) {
    try {
      return new URL(ext).host;
    } catch {
      /* the raw text below */
    }
  }
  return raw.length > 40 ? `${raw.slice(0, 39)}…` : raw;
}

/**
 * A tool tab's title and icon; `tip` is its tooltip (a browser tab's whole address). A kind this
 * build does not know (a tab saved by a newer one) still gets a title.
 */
export function toolMeta(kind: ToolKind, arg?: string): { title: string; icon: IconName; tip: string } {
  if (kind === 'browser') {
    const title = browserTitle(arg);
    return { title, icon: 'globe', tip: parseBrowserArg(arg).url || 'Browser' };
  }
  const item = TOOL_ITEMS.find((t) => t.kind === kind);
  return item ? { title: item.label, icon: item.icon, tip: item.label } : { title: 'Tool', icon: 'grid', tip: 'Tool' };
}
