// Group frame (node kind "group", docs/API-orchestration.md §1, D14): a tinted halo behind
// its member consoles with a header (name · role · members · states). The header drags
// the whole team; drag a console in or out of the frame to change membership. Only the
// header takes pointer events, so marquee selection still works inside the frame.

import { memo, useState, type CSSProperties, type MouseEvent as ReactMouseEvent } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { agentPill } from '../../agents/state';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { Menu, type MenuEntry } from '../../components/Menu';
import { toast } from '../../components/Toast';
import { killSessions, pauseSessions, resumeSessions } from '../../orch/actions';
import { GROUP_COLORS, ROLE_COLOR, ROLE_LABEL, ROLES, type Role } from '../../orch/types';
import { useStoreSessions } from '../../orch/hooks';
import type { FlowNode } from '../flow';
import { groupNameProblem, groupSessions } from '../groups';
import { readGroup } from '../kinds';
import { useBoardEnv } from '../ui';

function GroupFrameImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl, ui } = useBoardEnv();
  const g = readGroup(data.node);
  const [renaming, setRenaming] = useState(false);
  const [draft, setDraft] = useState('');
  const [problem, setProblem] = useState('');
  const [confirm, setConfirm] = useState<'kill' | 'dissolve' | null>(null);
  useStoreSessions(); // re-render on state changes of members
  const sessions = groupSessions(ctl.nodes, data.node);
  const working = sessions.filter((s) => s.agentState === 'working').length;
  const needs = sessions.filter((s) => s.agentState === 'needsYou').length;
  const paused = sessions.filter((s) => s.paused).length;

  const startRename = () => {
    setDraft(g.name);
    setProblem('');
    setRenaming(true);
  };
  const commit = () => {
    const v = draft.trim();
    if (!renaming) return;
    if (!v || v === g.name) {
      setRenaming(false);
      return;
    }
    const p = groupNameProblem(v, ctl.nodes, id);
    if (p) {
      setProblem(p);
      return;
    }
    setRenaming(false);
    ctl.updateData(id, { name: v });
  };

  const setRole = (role: Role) => ctl.updateData(id, { role, color: ROLE_COLOR[role] });

  const items: MenuEntry[] = [
    { label: 'Rename group', icon: 'pencil', onSelect: startRename },
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
    { label: 'Pause group', icon: 'pause', disabled: !sessions.some((s) => !s.paused && s.status === 'running'), onSelect: () => void pauseSessions(sessions) },
    { label: 'Resume group', icon: 'play', disabled: !paused, onSelect: () => void resumeSessions(sessions.filter((s) => s.paused)) },
    'sep',
    { label: 'Ungroup', icon: 'ungroup', shortcut: 'Ctrl+Shift+G', onSelect: () => setConfirm('dissolve') },
    { label: 'Kill group…', icon: 'power', danger: true, disabled: !sessions.some((s) => s.status !== 'exited'), onSelect: () => setConfirm('kill') },
  ];

  const onHeadClick = (e: ReactMouseEvent) => {
    if ((e.target as HTMLElement).closest('button, input, .menu')) return;
    if (e.shiftKey || e.metaKey || e.ctrlKey) {
      const next = new Set(ctl.selection);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      ctl.select(next);
    } else ctl.select([id]);
  };

  const style = { '--group-c': `var(--tag-${g.color})` } as CSSProperties;
  const summary = [working && `${working} working`, needs && `${needs} need${needs === 1 ? 's' : ''} you`, paused && `${paused} paused`].filter(Boolean).join(' · ');

  return (
    <div className={`group-frame ${selected ? 'selected' : ''} ${g.syncInput ? 'is-linked' : ''}`} style={style} role="group" aria-label={`Group ${g.name}`}>
      <Handle type="target" position={Position.Top} id="in" className="nb-handle nb-handle-in" isConnectable={false} />
      <Handle type="source" position={Position.Right} id="out" className="nb-handle nb-handle-out" title="Drag to forward this group’s results" />
      <div className="group-head" onClick={onHeadClick} onDoubleClick={(e) => e.stopPropagation()}>
        <span className="tile-mark" aria-hidden="true">
          <span className="group-dot" />
        </span>
        {renaming ? (
          <span className="node-rename-wrap nodrag">
            <input
              className="control control-sm group-rename"
              value={draft}
              autoFocus
              maxLength={24}
              aria-label="Group name"
              aria-invalid={problem ? true : undefined}
              title={problem || undefined}
              onFocus={(e) => e.currentTarget.select()}
              onChange={(e) => {
                setDraft(e.target.value);
                setProblem('');
              }}
              onKeyDown={(e) => {
                e.stopPropagation();
                if (e.key === 'Enter') commit();
                else if (e.key === 'Escape') setRenaming(false);
              }}
              onBlur={() => {
                if (problem) {
                  toast.error(problem);
                  setRenaming(false);
                } else commit();
              }}
            />
            {problem && (
              <span className="node-rename-error" role="alert">
                {problem}
              </span>
            )}
          </span>
        ) : (
          <span
            className="group-name"
            title={`${g.name} — double-click to rename`}
            onDoubleClick={(e) => {
              e.stopPropagation();
              startRename();
            }}
          >
            {g.name}
          </span>
        )}
        <span className="group-role">{ROLE_LABEL[g.role]}</span>
        <span className="group-meta tabular">{g.members.length === 1 ? '1 console' : `${g.members.length} consoles`}</span>
        {summary && <span className="group-meta group-states">{summary}</span>}
        <span className="spacer" />
        <button
          type="button"
          className={`group-link nodrag ${g.syncInput ? 'is-on' : ''}`}
          aria-pressed={g.syncInput}
          disabled={!g.syncInput && g.members.length < 2}
          title={g.syncInput ? 'Typing in one console goes to all of them, including Ctrl+C. Click to unlink.' : 'Type in one console and the others get the same keys'}
          onClick={() => ctl.updateData(id, { syncInput: !g.syncInput })}
        >
          <Icon name="link" />
          <span>{g.syncInput ? 'Input linked' : 'Link input'}</span>
        </button>
        <Menu className="nodrag" label={`Actions for group ${g.name}`} items={items} />
      </div>
      {g.members.length === 0 && (
        <div className="group-empty">
          <Icon name="users" />
          <span>No consoles. Drag consoles into this frame, or add some from the menu.</span>
        </div>
      )}
      <span className="sr-only">{sessions.map((s) => `${s.handle ?? s.name}: ${agentPill(s).label}`).join(', ')}</span>

      {confirm === 'dissolve' && (
        <ConfirmDialog
          title={`Ungroup ${g.name}?`}
          confirmLabel="Ungroup"
          icon="ungroup"
          onClose={() => setConfirm(null)}
          onConfirm={() => {
            ctl.apply([{ op: 'del', id }]);
            ctl.select([]);
          }}
        >
          <p>The frame goes away; its {g.members.length === 1 ? 'console keeps' : `${g.members.length} consoles keep`} running where they are. Wires to the group are removed.</p>
        </ConfirmDialog>
      )}
      {confirm === 'kill' && (
        <ConfirmDialog title={`Kill ${g.name}?`} confirmLabel={`Kill ${sessions.length === 1 ? '1 console' : `${sessions.length} consoles`}`} danger onClose={() => setConfirm(null)} onConfirm={() => killSessions(sessions)}>
          <p>
            {sessions.map((s) => (s.handle ? `@${s.handle}` : s.name)).join(', ')} stop. The tiles stay on the board marked DONE; restart them from their menus.
          </p>
        </ConfirmDialog>
      )}
    </div>
  );
}

export const GroupFrame = memo(GroupFrameImpl);
