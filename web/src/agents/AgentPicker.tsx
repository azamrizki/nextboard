// Agent pickers (docs/API-agents.md §17, C2): the Spawn tool flyout next to the rail,
// the "All agents" dialog (Detected / Your agents / Not installed) and "Switch agent".

import { useEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent } from 'react';
import { Button } from '../components/Button';
import { Checkbox } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { timeAgo } from '../lib/format';
import type { Agent, Machine, ModelChoice, Profile, Session } from '../lib/types';
import { useProfiles } from '../store';
import { AccountOptions } from '../providers/AccountOptions';
import { compatible, useProviders } from '../providers/providers';
import { AgentLogo } from './AgentLogo';
import { agentLogo } from './logos';
import { byPriority, canResumeExact, canContinue, hasLiveStatus, hasYolo, supportsProfiles, useMachineAgents } from './catalog';
import { ModelPicker } from './ModelPicker';
import { defaultChoice, modelSummary } from './models';
import { defaultProfileOf, useEffectiveSettings } from './settings';

/** An agent in a list: its logo, or its colour when it has none. */
export function AgentSwatch({ agent }: { agent: Agent }) {
  const logo = agentLogo(agent.id, agent.base);
  if (logo) return <AgentLogo logo={logo} />;
  return <span className="swatch" style={{ '--swatch': agent.color || 'var(--c-grey)' } as CSSProperties} aria-hidden="true" />;
}

/** Capability chips (text, not colour alone). */
export function AgentCaps({ agent }: { agent: Agent }) {
  if (agent.kind === 'shell')
    return (
      <span className="cap-row">
        <span className="cap-chip">Shell</span>
      </span>
    );
  const caps: string[] = [];
  if (hasLiveStatus(agent)) caps.push('Live status');
  if (canResumeExact(agent)) caps.push('Resume');
  else if (canContinue(agent)) caps.push('Continue');
  if (hasYolo(agent)) caps.push('Yolo');
  if (supportsProfiles(agent)) caps.push('Profiles');
  return (
    <span className="cap-row">
      {caps.map((c) => (
        <span key={c} className="cap-chip">
          {c}
        </span>
      ))}
    </span>
  );
}

/** Rows of the spawn flyout: armed, ≤ 3 custom, installed built-ins (5 at most), Shell. */
export function flyoutRows(agents: Agent[], armedId: string): Agent[] {
  const avail = (a: Agent) => a.available !== false;
  const rows: Agent[] = [];
  const add = (a: Agent | undefined) => {
    if (a && !rows.some((r) => r.id === a.id) && a.kind === 'agent' && rows.length < 5) rows.push(a);
  };
  add(agents.find((a) => a.id === armedId));
  agents.filter((a) => a.source === 'custom' && avail(a)).slice(0, 3).forEach(add);
  agents.filter((a) => a.source === 'builtin' && avail(a)).sort(byPriority).forEach(add);
  return rows;
}

interface FlyoutProps {
  machineId: string;
  machineName: string;
  armed: string;
  onPick: (a: Agent) => void;
  /** Model and effort of the armed agent. */
  choice: ModelChoice;
  onChoice: (c: ModelChoice) => void;
  onAll: () => void;
  onClose: () => void;
}

export function AgentFlyout({ machineId, machineName, armed, onPick, choice, onChoice, onAll, onClose }: FlyoutProps) {
  const ma = useMachineAgents(machineId);
  const ref = useRef<HTMLDivElement>(null);
  const rows = flyoutRows(ma.agents, armed);
  const shell = ma.agents.find((a) => a.kind === 'shell');
  const armedAgent = ma.agents.find((a) => a.id === armed);

  useEffect(() => {
    if (!ma.ready) return;
    const items = ref.current?.querySelectorAll<HTMLButtonElement>('.menu-item:not(:disabled)');
    const checked = ref.current?.querySelector<HTMLButtonElement>('.menu-item[aria-checked="true"]');
    (checked ?? items?.[0])?.focus();
  }, [ma.ready]);

  const onKey = (e: KeyboardEvent<HTMLDivElement>) => {
    const list = Array.from(ref.current?.querySelectorAll<HTMLButtonElement>('.menu-item:not(:disabled)') ?? []);
    const i = list.indexOf(document.activeElement as HTMLButtonElement);
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      list[(i + (e.key === 'ArrowDown' ? 1 : -1) + list.length) % list.length]?.focus();
    } else if (e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      onClose();
    }
  };

  return (
    <div ref={ref} className="agent-flyout" role="menu" aria-label="Spawn agent" onKeyDown={onKey}>
      <div className="menu-heading" role="presentation">
        Spawn on {machineName}
      </div>
      {!ma.ready && (
        <div className="list-msg" role="status">
          <span className="spinner" aria-hidden="true" /> Loading agents…
        </div>
      )}
      {ma.error && !ma.ready && <div className="list-msg text-danger">{ma.error}</div>}
      {armedAgent && armedAgent.available === false && (
        <div className="flyout-note">
          {armedAgent.name} is not installed on {machineName}.
        </div>
      )}
      {rows.map((a) => (
        <button
          key={a.id}
          type="button"
          role="menuitemradio"
          aria-checked={a.id === armed}
          className="menu-item"
          tabIndex={-1}
          disabled={a.available === false}
          title={`${a.command}${a.available === false ? ' — not installed' : ''}`}
          onClick={() => onPick(a)}
        >
          <AgentSwatch agent={a} />
          <span className="menu-label">{a.name}</span>
          {a.id === armed && <Icon name="check" className="menu-check" />}
        </button>
      ))}
      {shell && (
        <button type="button" role="menuitemradio" aria-checked={armed === shell.id} className="menu-item" tabIndex={-1} onClick={() => onPick(shell)}>
          <Icon name="terminal" />
          <span className="menu-label">Shell</span>
          {armed === shell.id && <Icon name="check" className="menu-check" />}
        </button>
      )}
      <div className="menu-sep" role="separator" />
      <button type="button" role="menuitem" className="menu-item" tabIndex={-1} onClick={onAll}>
        <Icon name="grid" />
        <span className="menu-label">All agents…</span>
      </button>
      {armedAgent?.models && armedAgent.available !== false && (
        <div className="flyout-model" onMouseDown={(e) => e.stopPropagation()}>
          <ModelPicker agent={armedAgent} value={choice} onChange={onChoice} />
        </div>
      )}
      <p className="flyout-hint">Pick an agent, then click on the board to place it.</p>
    </div>
  );
}

// ---- grouped list (dialogs) ----

interface Group {
  title: string;
  list: Agent[];
}

function groupAgents(agents: Agent[], q: string): Group[] {
  const query = q.trim().toLowerCase();
  const match = (a: Agent) => !query || a.name.toLowerCase().includes(query) || a.command.toLowerCase().includes(query) || a.id.includes(query);
  const list = agents.filter(match);
  const detected = list.filter((a) => a.source !== 'custom' && a.available !== false).sort(byPriority);
  const mine = list.filter((a) => a.source === 'custom').sort((a, b) => a.name.localeCompare(b.name));
  const missing = list.filter((a) => a.source !== 'custom' && a.available === false).sort(byPriority);
  return [
    { title: 'Detected', list: detected },
    { title: 'Your agents', list: mine },
    { title: 'Not installed', list: missing },
  ].filter((g) => g.list.length > 0);
}

interface AgentListProps {
  agents: Agent[];
  value: string;
  onChange: (id: string) => void;
  onActivate?: (a: Agent) => void;
  machineName: string;
  /** Allow picking agents that aren't installed (switch dialog: no). */
  label: string;
}

function AgentList({ agents, value, onChange, onActivate, machineName, label }: AgentListProps) {
  const [q, setQ] = useState('');
  const groups = useMemo(() => groupAgents(agents, q), [agents, q]);
  const flat = groups.flatMap((g) => g.list).filter((a) => a.available !== false);
  const listRef = useRef<HTMLDivElement>(null);

  const onKey = (e: KeyboardEvent) => {
    if (!flat.length) return;
    const i = flat.findIndex((a) => a.id === value);
    let j = i;
    if (e.key === 'ArrowDown') j = Math.min(flat.length - 1, i + 1);
    else if (e.key === 'ArrowUp') j = Math.max(0, i - 1);
    else if (e.key === 'Enter') {
      e.preventDefault();
      const a = flat[i];
      if (a) onActivate?.(a);
      return;
    } else return;
    e.preventDefault();
    onChange(flat[j].id);
    listRef.current?.querySelector<HTMLElement>(`[data-id="${CSS.escape(flat[j].id)}"]`)?.scrollIntoView({ block: 'nearest' });
  };

  return (
    <div className="stack agent-list-wrap">
      <div className="input-group has-lead">
        <span className="ig-lead">
          <Icon name="search" />
        </span>
        <input className="control" placeholder="Filter agents" aria-label="Filter agents" value={q} onChange={(e) => setQ(e.target.value)} onKeyDown={onKey} />
      </div>
      <div ref={listRef} className="agent-list" role="listbox" aria-label={label} tabIndex={0} aria-activedescendant={value ? `agent-opt-${value}` : undefined} onKeyDown={onKey}>
        {groups.length === 0 && <div className="list-msg">No agent matches “{q}”.</div>}
        {groups.map((g) => (
          <div key={g.title} role="group" aria-label={g.title}>
            <div className="conv-group" role="presentation">
              {g.title}
            </div>
            {g.list.map((a) => {
              const missing = a.available === false;
              return (
                <div
                  key={a.id}
                  id={`agent-opt-${a.id}`}
                  data-id={a.id}
                  role="option"
                  aria-selected={a.id === value}
                  aria-disabled={missing || undefined}
                  className={`agent-opt ${missing ? 'is-missing' : ''}`}
                  onClick={() => !missing && onChange(a.id)}
                  onDoubleClick={() => !missing && onActivate?.(a)}
                >
                  <AgentSwatch agent={a} />
                  <span className="agent-opt-main">
                    <span className="agent-opt-name">{a.name}</span>
                    <span className="agent-opt-cmd mono truncate" title={[a.command, ...(a.args ?? [])].join(' ')}>
                      {missing ? `Not found on ${machineName}: ` : ''}
                      {[a.command, ...(a.args ?? [])].join(' ')}
                    </span>
                    <AgentCaps agent={a} />
                  </span>
                </div>
              );
            })}
          </div>
        ))}
      </div>
    </div>
  );
}

interface AllAgentsDialogProps {
  machine: Machine | undefined;
  machineId: string;
  armed: string;
  onPick: (a: Agent) => void;
  onClose: () => void;
}

/** Every agent for the dock's machine; picking one arms the Spawn tool. */
export function AllAgentsDialog({ machine, machineId, armed, onPick, onClose }: AllAgentsDialogProps) {
  const ma = useMachineAgents(machineId);
  const [value, setValue] = useState(armed);
  const sel = ma.agents.find((a) => a.id === value && a.available !== false);
  const name = machine?.name ?? 'This PC';
  return (
    <Modal
      title="All agents"
      icon="bot"
      size="md"
      onClose={onClose}
      help={<span>Set paths and add your own agents in Settings › Agents.</span>}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" icon="terminal" disabled={!sel} onClick={() => sel && (onPick(sel), onClose())}>
            {sel ? `Arm ${sel.name}` : 'Arm agent'}
          </Button>
        </>
      }
    >
      {!ma.ready ? (
        <div className="list-msg" role="status">
          <span className="spinner" aria-hidden="true" /> Loading agents…
        </div>
      ) : (
        <AgentList agents={ma.agents} value={value} onChange={setValue} onActivate={(a) => (onPick(a), onClose())} machineName={name} label={`Agents on ${name}`} />
      )}
    </Modal>
  );
}

interface SwitchAgentDialogProps {
  session: Session;
  /** The agent to start on, when coming back to an earlier one. */
  initial?: string;
  onClose: () => void;
  /** account: "" the default login, a profile (pf_…) or an API provider (ap_…). */
  onSwitch: (agentId: string, account: string, opts: { brief: boolean; note: string; fresh: boolean; model?: string; effort?: string }) => Promise<void>;
  /** The same agent and login with another model: the conversation goes on. */
  onModel: (c: ModelChoice) => Promise<void>;
}

/** Another agent (and/or login) in this same tile, told what happened here. */
export function SwitchAgentDialog({ session, initial, onClose, onSwitch, onModel }: SwitchAgentDialogProps) {
  const ma = useMachineAgents(session.machineId);
  const profiles = useProfiles();
  const providers = useProviders();
  const current = session.agent || session.preset;
  const [value, setValue] = useState(initial ?? current);
  const [profileId, setProfileId] = useState(session.providerId || session.profileId || '');
  const [brief, setBrief] = useState(true);
  const [note, setNote] = useState('');
  const [goBack, setGoBack] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const sel = ma.agents.find((a) => a.id === value && a.available !== false);
  const options: Profile[] = sel && supportsProfiles(sel) ? profiles.filter((p) => p.machineId === session.machineId && p.agent === sel.id) : [];
  const provOptions = providers.filter((p) => compatible(sel, p));
  const settings = useEffectiveSettings();
  const defaultId = sel ? defaultProfileOf(settings, session.machineId, sel.id) : '';
  // the default account is the empty choice, whether it was picked or came with the session
  const chosen = profileId === defaultId ? '' : profileId;
  const sameAgent = sel?.id === current && (chosen || defaultId) === (session.providerId || session.profileId || defaultId);
  const [picked, setPicked] = useState<Record<string, ModelChoice>>({});
  const startsWith: ModelChoice = sel?.id === current ? { model: session.model ?? '', effort: session.effort ?? '' } : defaultChoice(settings, undefined, sel?.id ?? '');
  const modelNow = (sel && picked[sel.id]) || startsWith;
  const sameModel = (modelNow.model ?? '') === (session.model ?? '') && (modelNow.effort ?? '') === (session.effort ?? '');
  const same = sameAgent && sameModel;
  const running = session.status === 'running' && session.agentState !== 'shell';
  const past = session.pastRuns?.find((r) => r.agent === value && r.agentSessionId);
  const left = ma.agents.find((a) => a.id === current);
  // What was left can be picked up only by a CLI that can name its conversations.
  const leaving = canResumeExact(left) || canContinue(left);

  // A run left under one login cannot be continued under another.
  useEffect(() => {
    if (initial && past?.profileId) setProfileId(past.profileId);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const go = async () => {
    if (!sel || same) return;
    setBusy(true);
    setError('');
    try {
      if (sameAgent) await onModel(modelNow);
      else {
        const own = picked[sel.id];
        await onSwitch(sel.id, options.some((p) => p.id === chosen) || provOptions.some((p) => p.id === chosen) ? chosen : '', { brief, note: note.trim(), fresh: !!past && !goBack, model: own?.model, effort: own?.effort });
      }
      onClose();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  };

  return (
    <Modal
      title={`Agent and model · ${session.handle ? `@${session.handle}` : session.name}`}
      icon="bot"
      size="md"
      onClose={onClose}
      dismissable={!busy}
      help={
        sameAgent ? (
          <span>The console restarts and goes on with the same conversation.</span>
        ) : (
          <span className={running ? 'text-warning' : undefined}>
            {running ? 'The running agent stops; this tile stays where it is.' : 'This tile stays where it is.'}
            {leaving ? ' You can come back to its conversation later.' : ''}
          </span>
        )
      }
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" icon="restart" loading={busy} disabled={!sel || same} onClick={() => void go()}>
            {sel ? (sameAgent ? `Restart with ${modelSummary(modelNow.model, modelNow.effort) || 'the default'}` : past && goBack ? `Back to ${sel.name}` : `Switch to ${sel.name}`) : 'Switch'}
          </Button>
        </>
      }
    >
      <div className="stack">
        {!ma.ready ? (
          <div className="list-msg" role="status">
            <span className="spinner" aria-hidden="true" /> Loading agents…
          </div>
        ) : (
          <AgentList agents={ma.agents} value={value} onChange={setValue} onActivate={() => void go()} machineName={session.machineName} label="Agent" />
        )}
        {options.length + provOptions.length > 0 && (
          <div className="field">
            <label className="field-label" htmlFor="switch-profile">
              Login profile
            </label>
            <select id="switch-profile" className="control" value={chosen} onChange={(e) => setProfileId(e.target.value)}>
              <AccountOptions profiles={options} defaultProfile={defaultId} providers={provOptions} />
            </select>
          </div>
        )}
        {sel?.models && (
          <div className="field">
            <span className="field-label">Model and effort</span>
            <ModelPicker agent={sel} value={modelNow} onChange={(c) => setPicked((p) => ({ ...p, [sel.id]: c }))} profileId={chosen} />
          </div>
        )}
        {past && !sameAgent && (
          <Checkbox checked={goBack} onChange={setGoBack} label={`Continue its earlier conversation here, from ${timeAgo(past.at)}`} />
        )}
        {!sameAgent && <Checkbox checked={brief} onChange={setBrief} label="Brief it on what happened in this console" />}
        {!sameAgent && brief && (
          <>
            <p className="setting-hint">A short hidden run writes the summary, using your CLI’s plan. The agent stands still until it is done. An agent with no conversation yet needs none.</p>
            <input className="control" value={note} maxLength={600} placeholder="Anything to add for the next agent (optional)" aria-label="Note for the next agent" onChange={(e) => setNote(e.target.value)} />
          </>
        )}
        <p className="setting-hint">Project memory, critical notes and MCP servers come along, whichever agent you choose.</p>
        {error && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error}</span>
          </div>
        )}
      </div>
    </Modal>
  );
}
