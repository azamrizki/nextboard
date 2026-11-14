// Settings › Agents (docs/API-agents.md §1, §9, §17): default agent per machine,
// behaviour (yolo default, live status, resume after a hub restart), the catalog with
// detection per machine (+ path overrides), and custom agents.

import { useMemo, useState, type ReactNode } from 'react';
import { api, errorMessage } from '../api';
import { AgentCaps, AgentSwatch } from '../agents/AgentPicker';
import { byPriority, refreshAgents, SOURCE_LABEL, useAgentCatalog, useMachinesAgents } from '../agents/catalog';
import { patchSettings, useEffectiveSettings } from '../agents/settings';
import { YoloConfirm } from '../agents/YoloConfirm';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { SegmentedControl } from '../components/form';
import { Icon } from '../components/Icon';
import { SettingRow, SwitchRow } from '../components/Switch';
import { toast } from '../components/Toast';
import type { Agent, Machine } from '../lib/types';
import { machineReady } from '../board/sessionStatus';
import { useMachines } from '../store';
import { AgentDetailsDialog } from './AgentDetailsDialog';
import { CustomAgentDialog } from './CustomAgentDialog';
import { ModelDefaults } from './ModelDefaults';

async function save(patch: Record<string, unknown>, what: string) {
  try {
    await patchSettings(patch);
  } catch (err) {
    toast.error(`Could not save ${what}: ${errorMessage(err)}`);
    throw err;
  }
}

export function AgentsPane() {
  const settings = useEffectiveSettings();
  const machines = useMachines();
  const catalog = useAgentCatalog();
  const ids = useMemo(() => machines.map((m) => m.id), [machines]);
  const perMachine = useMachinesAgents(ids);
  const [yoloAsk, setYoloAsk] = useState(false);
  const [details, setDetails] = useState<Agent | null>(null);
  const [editing, setEditing] = useState<Agent | 'new' | null>(null);
  const [deleting, setDeleting] = useState<Agent | null>(null);
  const [filter, setFilter] = useState('');
  const [onlyFound, setOnlyFound] = useState<'all' | 'found' | 'off'>('all');

  const ag = settings.agents;
  const found = (a: Agent) =>
    machines.some((m) => {
      const x = perMachine[m.id]?.agents.find((y) => y.id === a.id);
      return !!x && x.available !== false;
    });
  const list = catalog.agents
    .filter((a) => a.source !== 'custom')
    .filter((a) => !filter.trim() || a.name.toLowerCase().includes(filter.trim().toLowerCase()) || a.command.includes(filter.trim()))
    .filter((a) => onlyFound === 'all' || (onlyFound === 'found' ? found(a) : !!ag.disabled?.[a.id]))
    .sort((a, b) => (a.source === b.source ? byPriority(a, b) : a.source === 'builtin' ? -1 : 1));
  const custom = catalog.agents.filter((a) => a.source === 'custom').sort((a, b) => a.name.localeCompare(b.name));

  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="ag-default">
        <h3 id="ag-default" className="section-title">
          Default agent per machine
        </h3>
        <p className="setting-hint">Used when nothing is armed for the Spawn tool, and as the first choice in new consoles.</p>
        {machines.map((m) => (
          <DefaultAgentRow key={m.id} machine={m} data={perMachine[m.id]} value={ag.defaults?.[m.id] ?? ''} />
        ))}
      </section>

      <ModelDefaults />

      <section className="card settings-card" aria-labelledby="ag-behaviour">
        <h3 id="ag-behaviour" className="section-title">
          Behaviour
        </h3>
        <SwitchRow
          title="Yolo by default"
          hint="New consoles of agents that have a skip-permissions flag start without permission prompts. Off is safer."
          checked={ag.yoloDefault}
          onChange={(v) => (v ? setYoloAsk(true) : void save({ agents: { yoloDefault: false } }, 'the Yolo default').catch(() => undefined))}
        />
        <SwitchRow
          title="Agents on servers"
          hint="Claude Code, Codex and OpenCode stay on This PC, with their login there, and work on a server through tools the hub provides over SSH. Their local tools are turned off, and the server needs nothing installed."
          checked={!!ag.remoteTools}
          onChange={(v) => void save({ agents: { remoteTools: v } }, 'agents on servers').then(() => refreshAgents()).catch(() => undefined)}
        />
        <SwitchRow
          title="Agents sign their commits"
          hint="Off: commits made in agent consoles carry no Co-authored-by or “Generated with” line for an AI tool, in any project and with any CLI. Co-authors who are people stay. Applies to consoles started after the change."
          checked={!!ag.coAuthor}
          onChange={(v) => void save({ agents: { coAuthor: v } }, 'commit signing').catch(() => undefined)}
        />
        <SwitchRow
          title="Live status from the agent"
          hint="Claude Code hooks and Codex notify are passed per launch as flags — no config file is written on any machine. Off: status comes from the screen only."
          checked={ag.liveStatus}
          onChange={(v) => void save({ agents: { liveStatus: v } }, 'live status').catch(() => undefined)}
        />
        <SwitchRow
          title="Link Claude Code to the editor"
          hint="New Claude Code consoles use the editor card as their IDE: they see your selection and its problems, and show proposed changes as a diff there. Only for projects on This PC. Consoles that are running follow after a restart."
          checked={ag.ideLink}
          onChange={(v) => void save({ agents: { ideLink: v } }, 'the editor link').catch(() => undefined)}
        />
        <SettingRow title="After a hub restart" hint="Local consoles end when the hub restarts. Remote tmux consoles keep running either way.">
          <SegmentedControl
            label="Resume after a hub restart"
            size="sm"
            value={ag.resumeOnRestart}
            onChange={(v) => void save({ agents: { resumeOnRestart: v } }, 'the resume setting').catch(() => undefined)}
            options={[
              { value: 'auto', label: 'Resume', title: 'Resume the conversation that was running' },
              { value: 'ask', label: 'Ask', title: 'Open the shell and offer to resume' },
              { value: 'never', label: 'Shell only', title: 'Open the shell' },
            ]}
          />
        </SettingRow>
        <SwitchRow
          title="Start agents when they are needed"
          hint="After a hub restart, agents wait asleep until you open their board, type into them or the lead sends them work, instead of all starting at once. Each agent takes 120–250 MB while it runs. Applies with Resume."
          checked={ag.lazyStart ?? true}
          onChange={(v) => void save({ agents: { lazyStart: v } }, 'starting agents when needed').catch(() => undefined)}
        />
        <SettingRow
          title="Idle agents sleep after"
          hint="An agent at its prompt with its terminal closed, no work from the lead and no command running is stopped to free memory. It resumes its conversation when you open it, type into it or send it work, and what you had typed but not sent is put back."
        >
          <SegmentedControl
            label="Idle agents sleep after"
            size="sm"
            value={String(ag.sleepAfterMin ?? 30)}
            onChange={(v) => void save({ agents: { sleepAfterMin: Number(v) } }, 'the sleep setting').catch(() => undefined)}
            options={[
              { value: '0', label: 'Never', title: 'Agents keep running until you stop them' },
              { value: '15', label: '15 min' },
              { value: '30', label: '30 min' },
              { value: '60', label: '1 h' },
              { value: '240', label: '4 h' },
            ]}
          />
        </SettingRow>
        <SettingRow
          title="Keep recent projects awake"
          hint="The agents of the projects you opened last do not sleep when idle, and start right after a hub restart, so going back to one is instant. Each awake agent takes 120–250 MB."
        >
          <SegmentedControl
            label="Keep recent projects awake"
            size="sm"
            value={String(ag.keepRecentProjects ?? 2)}
            onChange={(v) => void save({ agents: { keepRecentProjects: Number(v) } }, 'the recent projects setting').catch(() => undefined)}
            options={[
              { value: '0', label: 'None', title: 'Every project\'s idle agents sleep' },
              { value: '1', label: 'Last 1' },
              { value: '2', label: 'Last 2' },
              { value: '3', label: 'Last 3' },
            ]}
          />
        </SettingRow>
        <SettingRow title="Status channel" hint="How hooks reach the hub. Auto uses the terminal and falls back to a loopback HTTP port.">
          <SegmentedControl
            label="Status channel"
            size="sm"
            value={ag.hookChannel}
            onChange={(v) => void save({ agents: { hookChannel: v } }, 'the status channel').catch(() => undefined)}
            options={[
              { value: 'auto', label: 'Auto' },
              { value: 'tty', label: 'Terminal' },
              { value: 'http', label: 'HTTP' },
            ]}
          />
        </SettingRow>
      </section>

      <section className="card settings-card" aria-labelledby="ag-mine">
        <div className="settings-card-head">
          <h3 id="ag-mine" className="section-title">
            Your agents
          </h3>
          <Button size="sm" variant="primary" icon="plus" onClick={() => setEditing('new')}>
            Add agent
          </Button>
        </div>
        {custom.length === 0 ? (
          <p className="setting-hint">No custom agents yet. Add any CLI by its command — for example a wrapper script or an agent missing from the catalog.</p>
        ) : (
          <ul className="plain agent-rows">
            {custom.map((a) => (
              <AgentRow key={a.id} agent={a} machines={machines} perMachine={perMachine} off={!!ag.disabled?.[a.id]}>
                <Button size="sm" variant="ghost" icon="pencil" onClick={() => setEditing(a)} aria-label={`Edit ${a.name}`}>
                  Edit
                </Button>
                <Button size="sm" variant="danger-ghost" iconOnly icon="trash" onClick={() => setDeleting(a)} aria-label={`Delete ${a.name}`} />
              </AgentRow>
            ))}
          </ul>
        )}
      </section>

      <section className="card settings-card" aria-labelledby="ag-catalog">
        <div className="settings-card-head">
          <h3 id="ag-catalog" className="section-title">
            Catalog
          </h3>
          <SegmentedControl
            label="Show"
            size="sm"
            value={onlyFound}
            onChange={setOnlyFound}
            options={[
              { value: 'all', label: 'All' },
              { value: 'found', label: 'Detected' },
              { value: 'off', label: 'Turned off' },
            ]}
          />
        </div>
        <p className="setting-hint">Turn off what you have installed but don’t use: it disappears from every list. Consoles already running it keep running.</p>
        <div className="input-group has-lead">
          <span className="ig-lead">
            <Icon name="search" />
          </span>
          <input className="control control-sm" placeholder="Filter by name or command" aria-label="Filter the catalog" value={filter} onChange={(e) => setFilter(e.target.value)} />
        </div>
        {catalog.error && !catalog.ready && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>Could not load the catalog: {catalog.error}</span>
          </div>
        )}
        {!catalog.ready && !catalog.error && (
          <div className="list-msg" role="status">
            <span className="spinner" aria-hidden="true" /> Loading agents…
          </div>
        )}
        <ul className="plain agent-rows">
          {list.map((a) => (
            <AgentRow key={a.id} agent={a} machines={machines} perMachine={perMachine} off={!!ag.disabled?.[a.id]}>
              <Button size="sm" variant="ghost" onClick={() => setDetails(a)} aria-label={`Details and paths for ${a.name}`}>
                Details
              </Button>
            </AgentRow>
          ))}
        </ul>
        {catalog.ready && list.length === 0 && <div className="list-msg">{onlyFound === 'off' ? 'Nothing is turned off.' : 'No agent matches.'}</div>}
      </section>

      {yoloAsk && (
        <YoloConfirm
          subject="Every new console of an agent that supports it, on every machine,"
          onClose={() => setYoloAsk(false)}
          onConfirm={() => save({ agents: { yoloDefault: true } }, 'the Yolo default')}
        />
      )}
      {details && <AgentDetailsDialog agent={details} onClose={() => setDetails(null)} />}
      {editing && <CustomAgentDialog agent={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.name}?`}
          confirmLabel="Delete agent"
          danger
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await api.deleteAgent(deleting.id);
            refreshAgents();
            toast.success(`${deleting.name} deleted.`);
          }}
        >
          <p>It disappears from the pickers. Consoles already running it keep running.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}

function DefaultAgentRow({ machine, data, value }: { machine: Machine; data: { defaultAgent: string; agents: Agent[] } | undefined; value: string }) {
  const [busy, setBusy] = useState(false);
  const id = `def-${machine.id}`;
  const avail = (data?.agents ?? []).filter((a) => a.available !== false && !a.disabled).sort(byPriority);
  const auto = data ? data.agents.find((a) => a.id === data.defaultAgent)?.name ?? data.defaultAgent : '';
  const change = async (v: string) => {
    setBusy(true);
    try {
      await save({ agents: { defaults: { [machine.id]: v || null } } }, 'the default agent');
    } catch {
      /* toast shown */
    } finally {
      setBusy(false);
    }
  };
  return (
    <SettingRow
      htmlFor={id}
      title={
        <span className="row">
          <span className={`dot dot-${machineReady(machine) ? 'success' : 'grey'}`} aria-hidden="true" />
          {machine.name}
        </span>
      }
      hint={!machineReady(machine) && !data ? 'Connect the machine to see its agents.' : value ? undefined : auto ? `Automatic: ${auto}` : undefined}
    >
      <select id={id} className="control control-sm default-select" value={avail.some((a) => a.id === value) ? value : ''} disabled={busy || !data} onChange={(e) => void change(e.target.value)}>
        <option value="">Automatic{auto ? ` (${auto})` : ''}</option>
        {avail.map((a) => (
          <option key={a.id} value={a.id}>
            {a.name}
          </option>
        ))}
      </select>
    </SettingRow>
  );
}

interface AgentRowProps {
  agent: Agent;
  machines: Machine[];
  perMachine: Record<string, { defaultAgent: string; agents: Agent[] } | undefined>;
  off: boolean;
  children: ReactNode;
}

/** On or off for every list: a shell is no agent and stays. */
function AgentSwitch({ agent, off }: { agent: Agent; off: boolean }) {
  const [busy, setBusy] = useState(false);
  if (agent.kind !== 'agent') return null;
  const change = async () => {
    setBusy(true);
    try {
      // null removes the key: what is on is not listed at all
      await save({ agents: { disabled: { [agent.id]: off ? null : true } } }, `${agent.name}`);
    } catch {
      /* toast shown */
    } finally {
      setBusy(false);
    }
  };
  return (
    <button
      type="button"
      role="switch"
      className="switch"
      aria-checked={!off}
      aria-label={`${agent.name} in the agent lists`}
      title={off ? `${agent.name} is turned off` : `${agent.name} is on`}
      aria-busy={busy || undefined}
      onClick={() => !busy && void change()}
    >
      <span className="switch-knob" aria-hidden="true" />
    </button>
  );
}

/** Catalog row: name, command, source, detection per machine, capabilities. */
function AgentRow({ agent, machines, perMachine, off, children }: AgentRowProps) {
  return (
    <li className={`agent-row ${off ? 'is-off' : ''}`}>
      <AgentSwatch agent={agent} />
      <div className="agent-row-main">
        <div className="agent-row-title">
          <span className="agent-row-name">{agent.name}</span>
          <span className="cap-chip">{SOURCE_LABEL[agent.source] ?? agent.source}</span>
          {off && <span className="cap-chip">Turned off</span>}
          {agent.base && <span className="cap-chip">Like {agent.base === 'claude' ? 'Claude Code' : 'Codex'}</span>}
        </div>
        <span className="agent-row-cmd mono truncate" title={[agent.command, ...(agent.args ?? [])].join(' ')}>
          {[agent.command, ...(agent.args ?? [])].join(' ')}
        </span>
        <div className="agent-row-meta">
          <span className="detect-row" aria-label="Detection per machine">
            {machines.map((m) => {
              const d = perMachine[m.id];
              const a = d?.agents.find((x) => x.id === agent.id);
              const st = !d ? 'unknown' : a?.available === false ? 'missing' : 'found';
              return (
                <span key={m.id} className={`detect-chip is-${st}`} title={st === 'found' ? `Found on ${m.name}` : st === 'missing' ? `Not found on ${m.name}` : `${m.name}: unknown`}>
                  <Icon name={st === 'found' ? 'check' : st === 'missing' ? 'minus' : 'dot'} />
                  {m.name}
                  <span className="sr-only">{st === 'found' ? ': found' : st === 'missing' ? ': not found' : ': unknown'}</span>
                </span>
              );
            })}
          </span>
          <AgentCaps agent={agent} />
        </div>
      </div>
      <div className="agent-row-actions">
        {children}
        <AgentSwitch agent={agent} off={off} />
      </div>
    </li>
  );
}
