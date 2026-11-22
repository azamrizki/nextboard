// Board cards as tabs of the Code view (DESIGN.md §3.19, Code view = Board view): the kinds that
// open there, as the board shows them, and their titles. A card tab is a tool tab of kind "card"
// whose argument is the card's node id on the board the Code view shows. Pure, so tests can read it.

import type { IconName } from '../../components/Icon';
import type { BoardNode } from '../../lib/types';

/** The cards that open as tabs, with the word and icon a list and a tab show. */
export const CARD_KINDS: Record<string, { label: string; icon: IconName }> = {
  kanban: { label: 'Kanban', icon: 'kanban' },
  checklist: { label: 'Checklist', icon: 'checklist' },
  calendar: { label: 'Calendar', icon: 'calendar' },
  orchestrator: { label: 'Orchestrator', icon: 'orchestrator' },
  memory: { label: 'Memory', icon: 'brain' },
  uptime: { label: 'Uptime', icon: 'activity' },
  resources: { label: 'This PC', icon: 'monitor' },
  note: { label: 'Note', icon: 'note' },
  credentials: { label: 'Credentials', icon: 'key' },
  branches: { label: 'Branches', icon: 'branch' },
  document: { label: 'Document', icon: 'book' },
  file: { label: 'File', icon: 'file' },
  image: { label: 'Image', icon: 'image' },
  link: { label: 'Link', icon: 'link' },
  weblog: { label: 'Console log', icon: 'terminal' },
  web: { label: 'Web', icon: 'globe' },
  app: { label: 'App', icon: 'grid' },
};

/** The cards the Code view's Cards list offers to make, as the board's Insert does. */
export const NEW_CARD_KINDS = ['kanban', 'checklist', 'calendar', 'note', 'orchestrator', 'uptime', 'resources', 'memory'] as const;
export type NewCardKind = (typeof NEW_CARD_KINDS)[number];

export const opensAsTab = (kind: string) => kind in CARD_KINDS;

const str = (v: unknown) => (typeof v === 'string' ? v.trim() : '');

/** A card's title: its own, else what it is ("Kanban"). */
export function cardTitle(n: Pick<BoardNode, 'kind' | 'data'>): string {
  const d = (n.data ?? {}) as Record<string, unknown>;
  const own = str(d.title) || str(d.name) || str(d.label) || (n.kind === 'note' ? str(d.text).split('\n')[0].slice(0, 40) : '');
  return own || CARD_KINDS[n.kind]?.label || 'Card';
}

// the titles of the cards open as tabs, as the Code view last saw them (toolMeta is pure and
// knows no board: the card tab tells it)
const titles = new Map<string, { title: string; icon: IconName }>();

/** Keep a card's title for its tab; true when it changed. */
export function rememberCard(n: Pick<BoardNode, 'id' | 'kind' | 'data'>): boolean {
  const next = { title: cardTitle(n), icon: CARD_KINDS[n.kind]?.icon ?? 'grid' };
  const was = titles.get(n.id);
  if (was && was.title === next.title && was.icon === next.icon) return false;
  titles.set(n.id, next);
  return true;
}

/** Keep the titles of a board's cards (renamed on the board or in a tab); true when one changed. */
export function rememberCards(nodes: Pick<BoardNode, 'id' | 'kind' | 'data'>[]): boolean {
  let changed = false;
  for (const n of nodes) if (opensAsTab(n.kind) && rememberCard(n)) changed = true;
  return changed;
}

export function cardMeta(id: string | undefined): { title: string; icon: IconName } {
  return (id && titles.get(id)) || { title: 'Card', icon: 'grid' };
}
