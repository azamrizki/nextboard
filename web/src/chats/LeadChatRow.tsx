// A chat with the lead as a row: its title, its last mission's state, size and age, and Resume
// (or "current"). The Chats panel's Lead tab and the thread headers' chat list share it.

import { useState, type MouseEvent as ReactMouseEvent } from 'react';
import { Button } from '../components/Button';
import { ContextMenu, Menu, type MenuEntry } from '../components/Menu';
import { missionStateLabel, missionTone } from '../orch/mission';
import { useOrch } from '../orch/store';
import { daysAgo, leadChatTitle, msgsLabel } from './helpers';
import type { LeadChat } from './types';

export function LeadChatRow({
  chat: c,
  current,
  card,
  busy,
  blocked,
  onResume,
  onOpen,
  menu,
}: {
  chat: LeadChat;
  current: boolean;
  card?: string;
  busy?: boolean;
  /** Why it cannot be resumed now (the lead is answering): Resume is off and says so. */
  blocked?: string;
  onResume: () => void;
  onOpen?: () => void;
  /** Rename and Delete (the Chats panel): the "…" button, right-click and long-press. */
  menu?: MenuEntry[];
}) {
  const mission = useOrch((s) => {
    const ids = c.missionIds ?? [];
    return ids.length ? s.missions[ids[ids.length - 1]] : undefined;
  });
  const [menuAt, setMenuAt] = useState<{ x: number; y: number } | null>(null);
  const title = leadChatTitle(c);
  const onContext = (e: ReactMouseEvent) => {
    if (!menu) return;
    e.preventDefault();
    e.stopPropagation();
    setMenuAt({ x: e.clientX, y: e.clientY });
  };
  return (
    <li className={`chat-row lead-chat-row ${current ? 'is-current' : ''}`} data-lead-chat={c.id} data-longpress={menu ? '' : undefined} onContextMenu={onContext}>
      <div className="chat-main">
        <span className="chat-title truncate" title={title}>
          {title}
        </span>
        <span className="chat-meta">
          {card && <span className="chat-chip is-card">card</span>}
          {mission && <span className={`chat-chip is-mission tone-${missionTone(mission.state)}`}>mission {missionStateLabel(mission.state).toLowerCase()}</span>}
          <span className="chat-size tabular">
            {msgsLabel(c.messages)} · {daysAgo(c.lastAt || c.createdAt)}
          </span>
        </span>
      </div>
      <div className="chat-actions">
        {current ? (
          onOpen ? (
            <Button size="sm" variant="ghost" onClick={onOpen} aria-label={`Open the current chat: ${title}`}>
              Open
            </Button>
          ) : (
            <span className="text-3 fs-sm">current</span>
          )
        ) : (
          <Button size="sm" loading={busy} disabled={!!blocked} title={blocked || undefined} onClick={onResume} aria-label={`Resume: ${title}`}>
            Resume
          </Button>
        )}
        {menu && <Menu className="chat-row-menu" label={`More for ${title}`} items={menu} />}
      </div>
      {menu && menuAt && <ContextMenu x={menuAt.x} y={menuAt.y} label={`Actions for ${title}`} items={menu} onClose={() => setMenuAt(null)} />}
    </li>
  );
}
