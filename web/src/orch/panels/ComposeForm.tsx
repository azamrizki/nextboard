// What a new team looks like (docs/API-orchestration.md §5, D14/C2): name, role, one slot per
// console, how many. Click a slot to cycle its agent, right-click for the whole catalog, or fill
// every slot at once. One POST /api/workspaces/{id}/spawn; the hub places everything in one board
// update. Shown in the board's right-click ring and in the team composer dialog.

import { useMemo, useState, type CSSProperties, type FormEvent, type MouseEvent as ReactMouseEvent } from 'react';
import { AgentBadge, shortAgentName } from '../../agents/AgentBadge';
import { byPriority, useMachineAgents } from '../../agents/catalog';
import { ModelPicker } from '../../agents/ModelPicker';
import { defaultChoice } from '../../agents/models';
import { useEffectiveSettings } from '../../agents/settings';
import { ApiError } from '../../api';
import type { BoardController } from '../../board/controller';
import { groupNameProblem, nextGroupName } from '../../board/groups';
import { readGroup } from '../../board/kinds';
import { machineReady } from '../../board/sessionStatus';
import type { ComposerOpts } from '../../board/ui';
import { Button } from '../../components/Button';
import { SelectField } from '../../components/form';
import { Icon } from '../../components/Icon';
import { ContextMenu, type MenuEntry } from '../../components/Menu';
import { toast } from '../../components/Toast';
import type { Agent, ModelChoice, Project } from '../../lib/types';
import { useMachines } from '../../store';
import { orchError } from '../actions';
import { orchApi } from '../api';
import { is404 } from '../store';
import { GROUP_COLORS, ROLE_COLOR, ROLE_LABEL, ROLES, type GroupColor, type Role } from '../types';
import '../../styles/ring.css';

/** The Fill all row: more agents than this go in its "+" menu. */
const FILL_ROW = 8;

export interface ComposeFormProps {
  ctl: BoardController;
  project: Project;
  opts: ComposerOpts;
  /** Where the team goes on the board; the hub finds a place without it. */
  at?: { x: number; y: number };
  onClose: () => void;
  onSpawned: (sessionIds: string[]) => void;
}

export default function ComposeForm({ ctl, project, opts, at, onClose, onSpawned }: ComposeFormProps) {
  const settings = useEffectiveSettings();
  const machines = useMachines();
  const [machineId, setMachineId] = useState(project.machineId || 'local');
  const ma = useMachineAgents(machineId);
  const existing = opts.groupId ? ctl.node(opts.groupId) : undefined;
  const card = opts.orchestratorId ? ctl.node(opts.orchestratorId) : undefined;
  const [grouped, setGrouped] = useState(opts.group);
  const [name, setName] = useState(() => nextGroupName(ctl.nodes));
  const [role, setRole] = useState<Role>(existing ? readGroup(existing).role : 'general');
  const [color, setColor] = useState<GroupColor | null>(null);
  const maxSlots = Math.min(12, existing ? 12 - readGroup(existing).members.length : 12);
  const [count, setCount] = useState(Math.min(opts.slots, maxSlots));
  const [picks, setPicks] = useState<Record<number, string>>({});
  /** Per agent: chosen here, else what settings give this role. */
  const [modelPicks, setModelPicks] = useState<Record<string, ModelChoice>>({});
  /** slot: that slot's agent; null: the agent of every slot. */
  const [menu, setMenu] = useState<{ x: number; y: number; slot: number | null } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const installed = useMemo(() => ma.agents.filter((a) => a.available !== false).sort(byPriority), [ma.agents]);
  const cyclable = installed.filter((a) => a.kind === 'agent');
  const roleAgent = settings.agents.roleAgents?.[role] || '';
  const fallback = (roleAgent && installed.some((a) => a.id === roleAgent) ? roleAgent : '') || ma.defaultAgent || cyclable[0]?.id || 'shell';
  const slotAgent = (i: number) => picks[i] ?? fallback;
  const agentOf = (id: string): Agent | undefined => ma.agents.find((a) => a.id === id);
  const shownColor = color ?? ROLE_COLOR[role];
  const usedRole = grouped || existing ? role : undefined;
  const modelOf = (id: string): ModelChoice => modelPicks[id] ?? defaultChoice(settings, usedRole, id);
  const slotAgents = [...new Set(Array.from({ length: count }, (_, i) => slotAgent(i)))].flatMap((id) => {
    const a = agentOf(id);
    return a?.models ? [a] : [];
  });

  const cycle = (i: number) => {
    if (!cyclable.length) return;
    const cur = cyclable.findIndex((a) => a.id === slotAgent(i));
    setPicks((p) => ({ ...p, [i]: cyclable[(cur + 1) % cyclable.length].id }));
  };
  const fillAll = (id: string) => setPicks(Object.fromEntries(Array.from({ length: 12 }, (_, i) => [i, id])));

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (grouped && !existing) {
      const p = groupNameProblem(name, ctl.nodes);
      if (p) {
        setError(p);
        return;
      }
    }
    setBusy(true);
    setError('');
    try {
      const r = await orchApi.spawn(ctl.workspaceId, {
        slots: Array.from({ length: count }, (_, i) => ({
          agent: slotAgent(i),
          machineId: machineId !== project.machineId ? machineId : undefined,
          model: modelOf(slotAgent(i)).model ?? '',
          effort: modelOf(slotAgent(i)).effort ?? '',
        })),
        ...(existing ? { group: { id: existing.id } } : grouped ? { group: { name: name.trim(), role, color: shownColor } } : {}),
        ...(card ? { orchestratorId: card.id } : {}),
        ...(at ? { at } : {}),
      });
      const n = r.sessions?.length ?? 0;
      toast.success(`Started ${n === 1 ? '1 console' : `${n} consoles`}${grouped && !existing ? ` in ${name.trim()}` : existing ? ` in ${readGroup(existing).name}` : ''}.`);
      onSpawned((r.sessions ?? []).map((s) => s.id));
      onClose();
    } catch (err) {
      if (is404(err)) setError('This hub cannot start teams yet. Update NextBoard on the hub.');
      else if (err instanceof ApiError && err.code === 'name_conflict') setError(`“${name}” is already used by a group or a console. Pick another name.`);
      else if (err instanceof ApiError && err.code === 'too_many') setError('That is more consoles than allowed (12 at once, 64 per board).');
      else setError(orchError(err));
    } finally {
      setBusy(false);
    }
  };

  const catalogItems = (slot: number | null): MenuEntry[] => {
    const set = (id: string) => (slot === null ? fillAll(id) : setPicks((p) => ({ ...p, [slot]: id })));
    return [
      { type: 'heading', label: 'Installed' },
      ...installed.map((a): MenuEntry => ({ label: a.name, icon: a.kind === 'shell' ? 'terminal' : 'bot', checked: slot !== null && slotAgent(slot) === a.id, onSelect: () => set(a.id) })),
      ...(ma.agents.some((a) => a.available === false)
        ? ([{ type: 'heading', label: 'Not installed' }, ...ma.agents.filter((a) => a.available === false).map((a): MenuEntry => ({ label: a.name, disabled: true, onSelect: () => undefined }))] as MenuEntry[])
        : []),
    ];
  };

  return (
    <form className="compose" onSubmit={submit}>
      {!existing && !card && !opts.group && (
        <label className="check">
          <input type="checkbox" checked={grouped} onChange={(e) => setGrouped(e.target.checked)} />
          Put them in a new group
        </label>
      )}
      {grouped && !existing && (
        <>
          <div className="compose-name">
            <button
              type="button"
              className="compose-dot"
              style={{ '--compose-c': `var(--tag-${shownColor})` } as CSSProperties}
              aria-label={`Group colour: ${shownColor}. Click for the next one.`}
              title="Change the colour"
              onClick={() => setColor(GROUP_COLORS[(GROUP_COLORS.indexOf(shownColor) + 1) % GROUP_COLORS.length])}
            />
            <input data-autofocus autoFocus className="compose-name-input" value={name} maxLength={24} aria-label="Group name" placeholder="Group name" onChange={(e) => setName(e.target.value)} />
          </div>
          <div className="chip-row" role="radiogroup" aria-label="Role" title="The role names and tints the group, and picks each slot’s default agent (Settings › Agents). It does not change what agents may do.">
            {ROLES.map((r) => (
              <button
                key={r}
                type="button"
                role="radio"
                aria-checked={role === r}
                className={`chip-btn compose-role ${role === r ? 'on' : ''}`}
                onClick={() => {
                  setRole(r);
                  setColor(null);
                  setPicks({});
                  setModelPicks({});
                }}
              >
                <span className="group-dot" style={{ background: `var(--tag-${ROLE_COLOR[r]})` } as CSSProperties} aria-hidden="true" />
                {ROLE_LABEL[r]}
              </button>
            ))}
          </div>
        </>
      )}
      <div className="compose-slots" role="list" aria-label="Slots">
        {Array.from({ length: count }, (_, i) => {
          const a = agentOf(slotAgent(i));
          return (
            <button
              key={i}
              type="button"
              role="listitem"
              className="compose-slot"
              style={{ '--agent-c': a?.color } as CSSProperties}
              title="Click to change the agent · right-click for all agents"
              aria-label={`Slot ${i + 1}: ${a?.name ?? slotAgent(i)}. Click to change.`}
              onClick={() => cycle(i)}
              onContextMenu={(e: ReactMouseEvent) => {
                e.preventDefault();
                setMenu({ x: e.clientX, y: e.clientY, slot: i });
              }}
            >
              <AgentBadge agent={a} />
              <span className="compose-slot-name truncate">{shortAgentName(a?.name ?? slotAgent(i))}</span>
            </button>
          );
        })}
      </div>
      <div className="compose-fill" role="group" aria-label="Fill every slot with">
        <span className="compose-fill-label">Fill all</span>
        {installed.slice(0, FILL_ROW).map((a) => (
          <button key={a.id} type="button" className="compose-fill-btn" title={`Every slot runs ${a.name}`} aria-label={`Every slot runs ${a.name}`} onClick={() => fillAll(a.id)}>
            <AgentBadge agent={a} small />
          </button>
        ))}
        {installed.length > FILL_ROW && (
          <button
            type="button"
            className="compose-fill-btn"
            title="All installed agents"
            aria-label="All installed agents"
            onClick={(e) => setMenu({ x: e.clientX, y: e.clientY, slot: null })}
          >
            <Icon name="plus" />
          </button>
        )}
      </div>
      {slotAgents.length > 0 && (
        <div className="compose-models" role="group" aria-label="Model and effort">
          {slotAgents.map((a) => (
            <div key={a.id} className="compose-model">
              <AgentBadge agent={a} small />
              <ModelPicker agent={a} value={modelOf(a.id)} onChange={(c) => setModelPicks((m) => ({ ...m, [a.id]: c }))} />
            </div>
          ))}
        </div>
      )}
      {machines.length > 1 && (
        <SelectField label="Machine" value={machineId} onChange={(e) => setMachineId(e.target.value)}>
          {machines.map((m) => (
            <option key={m.id} value={m.id} disabled={!machineReady(m)}>
              {m.name}
              {machineReady(m) ? '' : ' (offline)'}
            </option>
          ))}
        </SelectField>
      )}
      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
      <footer className="compose-foot">
        <Button size="sm" variant="ghost" iconOnly icon="minus" aria-label="One console fewer" disabled={count <= 1} onClick={() => setCount((c) => Math.max(1, c - 1))} />
        <span className="tabular compose-n" aria-live="polite" aria-label={`${count} consoles`}>
          {count}
        </span>
        <Button size="sm" variant="ghost" iconOnly icon="plus" aria-label="One console more" disabled={count >= maxSlots} onClick={() => setCount((c) => Math.min(maxSlots, c + 1))} />
        <span className="spacer" />
        <Button type="submit" variant="primary" icon="play" loading={busy} disabled={!count}>
          Spawn {count}
        </Button>
      </footer>
      {menu && <ContextMenu x={menu.x} y={menu.y} label={menu.slot === null ? 'Agent for every slot' : `Agent for slot ${menu.slot + 1}`} items={catalogItems(menu.slot)} onClose={() => setMenu(null)} />}
    </form>
  );
}
