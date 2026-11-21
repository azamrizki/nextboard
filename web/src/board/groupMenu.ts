// A group's actions (docs/API-orchestration.md §1): the same on the board's group frame and in the
// Code view's Agents list (Code view = Board view, DESIGN.md §3.19). Renaming and the two
// confirmations are the caller's: each shows them its own way.

import type { MenuEntry } from '../components/Menu';
import { pauseSessions, resumeSessions } from '../orch/actions';
import { GROUP_COLORS, ROLE_COLOR, ROLE_LABEL, ROLES, type Role } from '../orch/types';
import type { BoardNode, Session } from '../lib/types';
import type { BoardController } from './controller';
import { readGroup } from './kinds';
import type { UiStore } from './ui';

export interface GroupMenuHandlers {
  rename: () => void;
  /** Ask before ungrouping, or before killing every member. */
  confirm: (what: 'dissolve' | 'kill') => void;
}

export function groupMenuItems(ctl: BoardController, ui: UiStore, node: BoardNode, sessions: Session[], h: GroupMenuHandlers): MenuEntry[] {
  const g = readGroup(node);
  const id = node.id;
  const paused = sessions.filter((s) => s.paused).length;
  const setRole = (role: Role) => ctl.updateData(id, { role, color: ROLE_COLOR[role] });
  return [
    { label: 'Rename group', icon: 'pencil', onSelect: h.rename },
    { type: 'heading', label: 'Role' },
    ...ROLES.map((r): MenuEntry => ({ label: ROLE_LABEL[r], checked: g.role === r, onSelect: () => setRole(r) })),
    {
      type: 'swatches',
      label: 'Colour',
      value: g.color,
      options: GROUP_COLORS.map((c) => ({ value: c, label: c[0].toUpperCase() + c.slice(1), color: `var(--tag-${c})` })),
      onSelect: (v) => ctl.updateData(id, { color: v }),
    },
    'sep',
    { label: 'Add consoles…', icon: 'plus', disabled: g.members.length >= 12, onSelect: () => ui.set({ composer: { slots: Math.min(4, 12 - g.members.length), group: false, groupId: id } }) },
    {
      label: g.syncInput ? 'Unlink input' : 'Link input',
      icon: 'link',
      disabled: !g.syncInput && g.members.length < 2,
      title: g.syncInput ? 'Typing in one console goes to all of them, including Ctrl+C' : 'Type in one console and the others get the same keys',
      onSelect: () => ctl.updateData(id, { syncInput: !g.syncInput }),
    },
    { label: 'Pause group', icon: 'pause', disabled: !sessions.some((s) => !s.paused && s.status === 'running'), onSelect: () => void pauseSessions(sessions) },
    { label: 'Resume group', icon: 'play', disabled: !paused, onSelect: () => void resumeSessions(sessions.filter((s) => s.paused)) },
    'sep',
    { label: 'Ungroup', icon: 'ungroup', shortcut: 'Ctrl+Shift+G', onSelect: () => h.confirm('dissolve') },
    { label: 'Kill group…', icon: 'power', danger: true, disabled: !sessions.some((s) => s.status !== 'exited'), onSelect: () => h.confirm('kill') },
  ];
}
