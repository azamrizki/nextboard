// A board on a phone (DESIGN.md §3.5): three tabs along the bottom edge.
//
//   Board  the canvas, with a shorter rail; drawing tools live in its Insert grid
//   Cards  every card as a list; a tap fills the screen with it (focus mode), a swipe goes on.
//          Its tab counts the consoles that need you.
//   Lead   the lead agent's thread and the command bar, full screen
//
// Which one opens is remembered per device: a phone shows the one used last.

import { useState } from 'react';
import { Button } from '../components/Button';
import { Icon, type IconName } from '../components/Icon';
import { agentById, useAgentCatalog } from '../agents/catalog';
import { agentPill } from '../agents/state';
import { prefs } from '../lib/format';
import type { BoardNode } from '../lib/types';
import { useNeedsYou, useSessions } from '../store';
import { sessionOf } from './controller';
import { cardOrder } from './focus';
import { nodeLabel } from './kinds';

export type PhoneTab = 'board' | 'cards' | 'lead';
const KEY = 'phone.tab';

export function usePhoneTab(): [PhoneTab, (t: PhoneTab) => void] {
  const [tab, setTab] = useState<PhoneTab>(() => {
    const v = prefs.get(KEY);
    return v === 'board' || v === 'lead' ? v : 'cards';
  });
  return [
    tab,
    (t) => {
      prefs.set(KEY, t);
      setTab(t);
    },
  ];
}

const TABS: { id: PhoneTab; label: string; icon: IconName }[] = [
  { id: 'board', label: 'Board', icon: 'layout' },
  { id: 'cards', label: 'Cards', icon: 'rows' },
  { id: 'lead', label: 'Lead', icon: 'orchestrator' },
];

export function PhoneTabs({ tab, onTab, leadName, waiting }: { tab: PhoneTab; onTab: (t: PhoneTab) => void; leadName: string; waiting: number }) {
  return (
    <nav className="tab-bar phone-tabs" aria-label="Board views">
      {TABS.map((t) => (
        <button key={t.id} type="button" className="tab-bar-item" aria-current={tab === t.id ? 'page' : undefined} onClick={() => onTab(t.id)}>
          <span className="tab-bar-icon">
            <Icon name={t.icon} />
            {t.id === 'cards' && waiting > 0 && (
              <span className="tab-bar-badge tabular" aria-label={`${waiting} need you`}>
                {waiting}
              </span>
            )}
          </span>
          <span className="tab-bar-label">{t.id === 'lead' ? leadName : t.label}</span>
        </button>
      ))}
    </nav>
  );
}

const KIND_ICON: Record<string, IconName> = {
  terminal: 'terminal',
  checklist: 'checklist',
  kanban: 'kanban',
  calendar: 'calendar',
  uptime: 'activity',
  credentials: 'key',
  orchestrator: 'orchestrator',
  memory: 'memoryCard',
  web: 'globe',
  weblog: 'terminal',
  file: 'fileCode',
  code: 'code',
  branches: 'branch',
  image: 'image',
  note: 'note',
  app: 'monitor',
};

/** Every card of the workspace, top to bottom as on the board. */
export function CardList({
  nodes,
  onOpen,
  onNewConsole,
  onAdd,
}: {
  nodes: BoardNode[];
  onOpen: (id: string) => void;
  onNewConsole: () => void;
  onAdd: () => void;
}) {
  useSessions(); // a console's state lives in the store: show it as it changes
  useAgentCatalog(); // and its agent's name in the catalog
  const needs = new Set(useNeedsYou().map((s) => s.id));
  const cards = cardOrder(nodes);
  return (
    <section className="card-list" aria-label="Cards">
      <div className="card-list-actions">
        <Button variant="primary" icon="terminal" onClick={onNewConsole}>
          New console
        </Button>
        <Button icon="plus" onClick={onAdd}>
          Add a card
        </Button>
      </div>
      {cards.length === 0 ? (
        <p className="card-list-empty">No cards on this board yet. Start a console, or add a card.</p>
      ) : (
        <ul className="plain card-list-rows">
          {cards.map((n) => {
            const s = n.kind === 'terminal' ? sessionOf(n) : undefined;
            const pill = s ? agentPill(s) : null;
            const name = s ? (s.handle ? `@${s.handle}` : s.name || 'Console') : nodeLabel(n);
            const sub = s ? [agentById(s.agent || s.preset)?.name ?? s.agent, pill?.activity || pill?.label].filter(Boolean).join(' · ') : n.kind === 'terminal' ? 'Console' : '';
            const needsYou = !!s && needs.has(s.id);
            return (
              <li key={n.id}>
                <button type="button" className={`card-row ${needsYou ? 'needs-you' : ''}`} onClick={() => onOpen(n.id)}>
                  <span className="card-row-mark" aria-hidden="true">
                    <Icon name={KIND_ICON[n.kind] ?? 'grid'} />
                  </span>
                  <span className="card-row-text">
                    <span className="card-row-name truncate">{name}</span>
                    {sub && <span className="card-row-sub truncate">{sub}</span>}
                  </span>
                  {needsYou && <span className="badge badge-warning">Needs you</span>}
                  {pill && !needsYou && <span className={`dot dot-${pill.dot}`} aria-label={pill.label} />}
                  <Icon name="chevronRight" className="card-row-chev" />
                </button>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}
