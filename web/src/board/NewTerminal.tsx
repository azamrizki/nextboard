// "New terminal" controls (DESIGN.md §3.5). Phase 6: the dock lives inside the command
// bar as the agent chip (armed agent + machine); the chip opens the same form as a
// popover (below 900px: the modal sheet). NewTerminal (the old dock) is kept for reuse.
// The dock's agent is the "armed" agent of the rail's Spawn tool (T), per client
// (docs/API-agents.md §1.2); availability and the default agent come per machine.

import { useEffect, useRef, useState, type CSSProperties, type FormEvent, type RefObject } from 'react';
import { errorMessage } from '../api';
import { byPriority, hasYolo, setArmed, supportsProfiles, useArmed, useMachineAgents } from '../agents/catalog';
import { ModelPicker } from '../agents/ModelPicker';
import { defaultChoice } from '../agents/models';
import { useEffectiveSettings } from '../agents/settings';
import { onMachine, YoloConfirm } from '../agents/YoloConfirm';
import { Button } from '../components/Button';
import { FolderBrowser } from '../components/FolderBrowser';
import { Checkbox, Field, SelectField, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { Popover } from '../components/Popover';
import { toast } from '../components/Toast';
import type { Agent, Machine, Project, ModelChoice } from '../lib/types';
import { useMediaQuery } from '../lib/useMediaQuery';
import { useMachines, useProfiles } from '../store';
import { AccountOptions, isProviderId } from '../providers/AccountOptions';
import { compatible, useProviders } from '../providers/providers';
import { machineReady, machineStatus } from './sessionStatus';

/** What the dock / Spawn tool would start. */
export interface TerminalChoice {
  machineId: string;
  /** Agent (catalog) id. */
  preset: string;
  name: string;
  cwd: string;
  /** undefined = hub default (settings.agents.yoloDefault). */
  yolo?: boolean;
  /** undefined = default profile from settings. */
  profileId?: string;
  /** An API provider instead of a login (docs/API-agents.md §10.2). */
  providerId?: string;
  /** undefined = the default in settings. */
  model?: string;
  effort?: string;
}

/** Model and effort of the armed agent, in the forms. */
function ModelField({ nt }: { nt: ReturnType<typeof useNewTerminal> }) {
  if (!nt.agent?.models) return null;
  return (
    <div className="field">
      <span className="field-label">Model and effort</span>
      <ModelPicker agent={nt.agent} value={nt.modelChoice} onChange={nt.setModelChoice} profileId={nt.profileId} />
    </div>
  );
}

/** Pick the agent to show: armed (if installed here), else the machine's default. */
function resolveAgent(agents: Agent[], armed: string, defaultAgent: string): Agent | undefined {
  const ok = (a: Agent | undefined) => (a && a.available !== false ? a : undefined);
  return ok(agents.find((a) => a.id === armed)) ?? ok(agents.find((a) => a.id === defaultAgent)) ?? agents.find((a) => a.available !== false);
}

/**
 * State + actions shared by the dock, the modal form and the Spawn tool.
 * `spawn` places and starts the terminal (the board decides where); resolves true on success.
 */
export function useNewTerminal(project: Project, spawn: (c: TerminalChoice) => Promise<boolean>) {
  const machines = useMachines();
  const settings = useEffectiveSettings();
  const profiles = useProfiles();
  const providers = useProviders();
  const [machineId, setMachineId] = useState(project.machineId || 'local');
  const armed = useArmed();
  const ma = useMachineAgents(machineId);
  const [name, setName] = useState('');
  const [cwd, setCwd] = useState('');
  const [creating, setCreating] = useState(false);
  const [yoloChoice, setYoloChoice] = useState<boolean | undefined>(undefined);
  const [profileId, setProfileId] = useState('');
  /** Chosen per agent, for as long as this board is open. */
  const [modelChoices, setModelChoices] = useState<Record<string, ModelChoice>>({});

  const machine = machines.find((m) => m.id === machineId);

  // Fall back to the first machine if the remembered one no longer exists.
  useEffect(() => {
    if (machines.length > 0 && !machine) setMachineId(machines[0].id);
  }, [machines, machine]);

  // Another project → its machine (the cwd defaults to the project folder).
  useEffect(() => {
    setMachineId(project.machineId || 'local');
    setCwd('');
  }, [project.id, project.machineId]);

  const agent = resolveAgent(ma.agents, armed, ma.defaultAgent);
  const agentId = agent?.id ?? '';
  const yoloable = hasYolo(agent);
  const yolo = yoloable ? (yoloChoice ?? settings.agents.yoloDefault) : false;
  const profileOptions = agent && supportsProfiles(agent) ? profiles.filter((p) => p.machineId === machineId && p.agent === agent.id) : [];
  // agents run on This PC, whatever machine they work on, so any provider that can run this one
  const providerOptions = agent ? providers.filter((p) => compatible(agent, p)) : [];
  const account = profileOptions.some((p) => p.id === profileId) || providerOptions.some((p) => p.id === profileId) ? profileId : '';
  const defaultProfile = settings.agents.profiles?.[machineId]?.[agentId] ?? '';
  const modelDefault = defaultChoice(settings, undefined, agentId);
  const modelChoice = modelChoices[agentId] ?? modelDefault;

  // a new agent / machine starts from the defaults again
  useEffect(() => {
    setYoloChoice(undefined);
    setProfileId('');
  }, [agentId, machineId]);

  const changeMachine = (id: string) => {
    setMachineId(id);
    setCwd(''); // paths are per machine
  };

  /** The current settings; cwd falls back to the project folder on the project's machine. */
  const choice = (): TerminalChoice => ({
    machineId,
    preset: agentId,
    name: name.trim(),
    cwd: cwd.trim() || (machineId === project.machineId ? project.path : ''),
    yolo: yoloable && yoloChoice !== undefined ? yoloChoice : undefined,
    profileId: account && !isProviderId(account) ? account : undefined,
    providerId: isProviderId(account) ? account : undefined,
    model: modelChoices[agentId]?.model,
    effort: modelChoices[agentId]?.effort,
  });

  /** Returns true on success. */
  const create = async (): Promise<boolean> => {
    if (!machineId || !agentId || creating) return false;
    setCreating(true);
    try {
      const ok = await spawn(choice());
      if (ok) {
        if (agentId !== armed) setArmed(agentId);
        setName('');
        setYoloChoice(undefined);
      }
      return ok;
    } catch (err) {
      toast.error(`Could not start the terminal: ${errorMessage(err)}`);
      return false;
    } finally {
      setCreating(false);
    }
  };

  return {
    machines,
    machine,
    machineId,
    changeMachine,
    agents: ma.agents,
    agentsError: ma.error,
    agentsReady: ma.ready,
    agentId,
    agent,
    armed,
    /** Picking an agent in the dock arms the Spawn tool. */
    setAgentId: (id: string) => setArmed(id),
    yoloable,
    yolo,
    setYolo: (v: boolean) => setYoloChoice(v),
    profileOptions,
    providerOptions,
    /** The account chosen: "" (the default login), a profile (pf_…) or an API provider (ap_…). */
    profileId: account,
    providerId: isProviderId(account) ? account : '',
    /** Whether there is any choice of account to show. */
    hasAccounts: profileOptions.length > 0 || providerOptions.length > 0,
    defaultProfile,
    /** Another account: a model of the old one would not exist on it. */
    setProfileId: (v: string) => {
      if (isProviderId(v) !== isProviderId(account) || (isProviderId(v) && v !== account)) setModelChoices((m) => ({ ...m, [agentId]: {} }));
      setProfileId(v);
    },
    modelChoice,
    setModelChoice: (c: ModelChoice) => setModelChoices((m) => ({ ...m, [agentId]: c })),
    name,
    setName,
    cwd,
    setCwd,
    creating,
    create,
    choice,
    project,
    canCreate: !!machineId && !!agentId && !creating,
  };
}

export type NT = ReturnType<typeof useNewTerminal>;

function machineOptionLabel(m: Machine) {
  return machineReady(m) ? m.name : `${m.name} (${machineStatus(m).label.toLowerCase()})`;
}

function MachineOptions({ nt }: { nt: NT }) {
  return (
    <>
      {nt.machines.length === 0 && <option value="local">This PC</option>}
      {nt.machines.map((m) => (
        <option key={m.id} value={m.id}>
          {machineOptionLabel(m)}
        </option>
      ))}
    </>
  );
}

/** What a server offers: agents always run on This PC. */
function serverHint(nt: NT): string | undefined {
  if (nt.machine?.kind !== 'ssh') return undefined;
  return nt.agents.some((a) => a.kind === 'agent')
    ? 'The agent runs on This PC, with its login and memory there, and works on this server.'
    : 'Agents run on This PC. On a server you get a plain shell.';
}

/** Agent options grouped like the pickers: Detected · Your agents · Shell · Not installed. */
function AgentOptions({ nt }: { nt: NT }) {
  if (nt.agents.length === 0) return <option value="">{nt.agentsError ? 'Agents unavailable' : 'Loading…'}</option>;
  const detected = nt.agents.filter((a) => a.source !== 'custom' && a.kind === 'agent' && a.available !== false).sort(byPriority);
  const mine = nt.agents.filter((a) => a.source === 'custom').sort((a, b) => a.name.localeCompare(b.name));
  const shells = nt.agents.filter((a) => a.source !== 'custom' && a.kind === 'shell');
  const missing = nt.agents.filter((a) => a.source !== 'custom' && a.kind === 'agent' && a.available === false).sort(byPriority);
  const opt = (a: Agent) => (
    <option key={a.id} value={a.id} disabled={a.available === false}>
      {a.name}
      {a.available === false ? ' (not installed)' : ''}
    </option>
  );
  return (
    <>
      {detected.length > 0 && <optgroup label="Detected">{detected.map(opt)}</optgroup>}
      {mine.length > 0 && <optgroup label="Your agents">{mine.map(opt)}</optgroup>}
      {shells.map(opt)}
      {missing.length > 0 && <optgroup label="Not installed">{missing.map(opt)}</optgroup>}
    </>
  );
}

/** Placeholder for the cwd field: the project folder when on the project's machine. */
function cwdPlaceholder(nt: NT): string {
  return nt.machineId === nt.project.machineId ? nt.project.path : nt.machine?.probe?.home || '~';
}

/** Yolo needs a confirmation every time it is switched on for a spawn. */
function useYoloToggle(nt: NT) {
  const [confirming, setConfirming] = useState(false);
  const toggle = () => {
    if (nt.yolo) nt.setYolo(false);
    else setConfirming(true);
  };
  const dialog = confirming && (
    <YoloConfirm
      subject={
        <>
          The new {nt.agent?.name ?? 'agent'} console {onMachine(nt.machine)}
        </>
      }
      onClose={() => setConfirming(false)}
      onConfirm={() => nt.setYolo(true)}
    />
  );
  return { toggle, dialog };
}

export function NewTerminal({ nt }: { nt: NT }) {
  const compact = useMediaQuery('(max-width: 899px)');
  const [sheetOpen, setSheetOpen] = useState(false);
  const [browsing, setBrowsing] = useState(false);

  const folderBrowser = browsing && nt.machine && (
    <FolderBrowser
      machineId={nt.machine.id}
      machineName={nt.machine.name}
      initialPath={nt.cwd.trim() || (nt.machineId === nt.project.machineId ? nt.project.path : '')}
      onClose={() => setBrowsing(false)}
      onChoose={(p) => {
        nt.setCwd(p);
        setBrowsing(false);
      }}
    />
  );

  if (compact) {
    return (
      <>
        <button type="button" className="fab" aria-label="New terminal" title="New terminal" onClick={() => setSheetOpen(true)}>
          <Icon name="plus" />
        </button>
        {sheetOpen && <NewTerminalSheet nt={nt} onClose={() => setSheetOpen(false)} onBrowse={() => setBrowsing(true)} />}
        {folderBrowser}
      </>
    );
  }

  return (
    <>
      <NewTerminalDock nt={nt} onBrowse={() => setBrowsing(true)} />
      {folderBrowser}
    </>
  );
}

/** Floating glass dock with small (32px) controls. */
function NewTerminalDock({ nt, onBrowse }: { nt: NT; onBrowse: () => void }) {
  const mstatus = nt.machine ? machineStatus(nt.machine) : null;
  const yolo = useYoloToggle(nt);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void nt.create();
  };
  return (
    <form className="dock" onSubmit={submit} aria-label="New terminal">
      <div className="input-group has-lead dock-machine" title={mstatus ? `Machine status: ${mstatus.label}` : 'Machine'}>
        <span className="ig-lead">
          <span className={`dot dot-${mstatus?.color ?? 'grey'}`} aria-hidden="true" />
        </span>
        <select className="control control-sm" value={nt.machineId} onChange={(e) => nt.changeMachine(e.target.value)} aria-label="Machine">
          <MachineOptions nt={nt} />
        </select>
      </div>

      <div className="input-group has-lead dock-preset" title={nt.agentsError || (nt.agent ? `Runs ${nt.agent.command}` : 'Agent')}>
        <span className="ig-lead">
          <span className="swatch" style={{ '--swatch': nt.agent?.color } as CSSProperties} aria-hidden="true" />
        </span>
        <select className="control control-sm" value={nt.agentId} onChange={(e) => nt.setAgentId(e.target.value)} aria-label="Agent">
          <AgentOptions nt={nt} />
        </select>
      </div>

      {nt.hasAccounts && (
        <div className="input-group has-lead dock-profile" title="Login profile or API provider">
          <span className="ig-lead">
            <Icon name="user" />
          </span>
          <select className="control control-sm" value={nt.profileId} onChange={(e) => nt.setProfileId(e.target.value)} aria-label="Login profile">
            <AccountOptions profiles={nt.profileOptions} defaultProfile={nt.defaultProfile} providers={nt.providerOptions} />
          </select>
        </div>
      )}

      <div className="input-group has-lead has-addon dock-cwd">
        <span className="ig-lead">
          <Icon name="folder" />
        </span>
        <input
          className="control control-sm mono"
          placeholder={cwdPlaceholder(nt)}
          value={nt.cwd}
          onChange={(e) => nt.setCwd(e.target.value)}
          aria-label="Working directory (optional)"
          title="Working directory (empty = project folder)"
          spellCheck={false}
        />
        <Button size="sm" className="ig-addon" onClick={onBrowse} disabled={!nt.machine}>
          Browse
        </Button>
      </div>

      <input
        className="control control-sm dock-name"
        placeholder="Name (optional)"
        value={nt.name}
        onChange={(e) => nt.setName(e.target.value)}
        aria-label="Terminal name (optional)"
        maxLength={80}
      />

      {nt.yoloable && (
        <Button
          size="sm"
          variant={nt.yolo ? 'secondary' : 'ghost'}
          iconOnly
          icon="bolt"
          className={`dock-yolo ${nt.yolo ? 'is-on' : ''}`}
          aria-pressed={nt.yolo}
          aria-label={nt.yolo ? 'Yolo is on for the next console — turn off' : 'Run the next console with Yolo…'}
          title={nt.yolo ? 'Yolo on: no permission prompts' : 'Yolo off'}
          onClick={yolo.toggle}
        />
      )}

      <span className="dock-sep" aria-hidden="true" />

      <Button type="submit" variant="primary" size="sm" icon="plus" loading={nt.creating} disabled={!nt.canCreate && !nt.creating}>
        New terminal
      </Button>
      {yolo.dialog}
    </form>
  );
}

/** Same form as a modal for narrow screens. */
function NewTerminalSheet({ nt, onClose, onBrowse }: { nt: NT; onClose: () => void; onBrowse: () => void }) {
  const yolo = useYoloToggle(nt);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (await nt.create()) onClose();
  };
  return (
    <Modal
      title="New terminal"
      icon="terminal"
      onClose={onClose}
      size="sm"
      dismissable={!nt.creating}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={nt.creating}>
            Cancel
          </Button>
          <Button type="submit" form="new-terminal-form" variant="primary" icon="plus" loading={nt.creating} disabled={!nt.canCreate && !nt.creating}>
            Start terminal
          </Button>
        </>
      }
    >
      <form id="new-terminal-form" className="form" onSubmit={submit}>
        <fieldset disabled={nt.creating}>
          <SelectField label="Machine" value={nt.machineId} onChange={(e) => nt.changeMachine(e.target.value)} hint={serverHint(nt)}>
            <MachineOptions nt={nt} />
          </SelectField>
          <SelectField
            label="Agent"
            value={nt.agentId}
            onChange={(e) => nt.setAgentId(e.target.value)}
            hint={nt.agent ? `Runs ${nt.agent.command}` : undefined}
            error={nt.agentsError || undefined}
          >
            <AgentOptions nt={nt} />
          </SelectField>
          <ModelField nt={nt} />
          {nt.hasAccounts && (
            <SelectField label="Login profile" value={nt.profileId} onChange={(e) => nt.setProfileId(e.target.value)}>
              <AccountOptions profiles={nt.profileOptions} defaultProfile={nt.defaultProfile} providers={nt.providerOptions} />
            </SelectField>
          )}
          <Field label="Working directory" optional hint="Leave empty to start in the project folder.">
            {({ labelId: _l, ...a11y }) => (
              <div className="input-group has-lead has-addon">
                <span className="ig-lead">
                  <Icon name="folder" />
                </span>
                <input
                  className="control mono"
                  placeholder={cwdPlaceholder(nt)}
                  value={nt.cwd}
                  onChange={(e) => nt.setCwd(e.target.value)}
                  spellCheck={false}
                  {...a11y}
                />
                <Button className="ig-addon" onClick={onBrowse} disabled={!nt.machine}>
                  Browse
                </Button>
              </div>
            )}
          </Field>
          <TextField label="Name" optional placeholder={nt.agent?.name ?? 'Claude Code'} value={nt.name} onChange={(e) => nt.setName(e.target.value)} maxLength={80} />
          {nt.yoloable && <Checkbox checked={nt.yolo} onChange={() => yolo.toggle()} label="Yolo: run without permission prompts" />}
        </fieldset>
      </form>
      {yolo.dialog}
    </Modal>
  );
}

/**
 * Agent chip of the command bar: the armed agent (and machine when not the project's).
 * Opens the new-console form as a popover (desktop) or the sheet (narrow screens).
 */
export function AgentChip({ nt, anchor }: { nt: NT; anchor?: RefObject<HTMLElement | null> }) {
  const compact = useMediaQuery('(max-width: 899px)');
  const [open, setOpen] = useState(false);
  const [browsing, setBrowsing] = useState(false);
  const ref = useRef<HTMLButtonElement>(null);
  const remote = nt.machine && nt.machine.id !== nt.project.machineId;
  const label = nt.agent ? `${nt.agent.name}${remote ? ` · ${nt.machine?.name}` : ''}` : 'Pick an agent';
  const folderBrowser = browsing && nt.machine && (
    <FolderBrowser
      machineId={nt.machine.id}
      machineName={nt.machine.name}
      initialPath={nt.cwd.trim() || (nt.machineId === nt.project.machineId ? nt.project.path : '')}
      onClose={() => setBrowsing(false)}
      onChoose={(p) => {
        nt.setCwd(p);
        setBrowsing(false);
      }}
    />
  );
  return (
    <>
      <button
        ref={ref}
        type="button"
        className={`agent-chip ${open ? 'is-open' : ''}`}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-label={`New console settings: ${label}${nt.yolo ? ', Yolo on' : ''}`}
        title={`New consoles start ${label}${nt.yolo ? ' with Yolo' : ''}. Click to change.`}
        onClick={() => setOpen((o) => !o)}
      >
        <span className="swatch" style={{ '--swatch': nt.agent?.color } as CSSProperties} aria-hidden="true" />
        <span className="agent-chip-label truncate">{label}</span>
        {nt.yolo && <Icon name="bolt" className="agent-chip-yolo" />}
        <Icon name="chevronUp" className="agent-chip-chev" />
      </button>
      {open && compact && <NewTerminalSheet nt={nt} onClose={() => setOpen(false)} onBrowse={() => setBrowsing(true)} />}
      {open && !compact && (
        <Popover anchor={anchor ?? ref} onClose={() => !browsing && setOpen(false)} label="New console" placement="above" align="start" className="nt-pop" width={340}>
          <NewTerminalPanel
            nt={nt}
            onBrowse={() => setBrowsing(true)}
            onDone={() => {
              setOpen(false);
              ref.current?.focus();
            }}
          />
        </Popover>
      )}
      {folderBrowser}
    </>
  );
}

/** The dock's controls stacked in a popover. */
function NewTerminalPanel({ nt, onBrowse, onDone }: { nt: NT; onBrowse: () => void; onDone: () => void }) {
  const yolo = useYoloToggle(nt);
  const mstatus = nt.machine ? machineStatus(nt.machine) : null;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (await nt.create()) onDone();
  };
  return (
    <form className="nt-panel form" onSubmit={submit} aria-label="New console">
      <div className="pop-head">
        <Icon name="terminal" />
        <strong>New console</strong>
      </div>
      <SelectField label="Machine" value={nt.machineId} onChange={(e) => nt.changeMachine(e.target.value)} className="control-sm" hint={serverHint(nt) ?? (mstatus ? `Status: ${mstatus.label}` : undefined)}>
        <MachineOptions nt={nt} />
      </SelectField>
      <SelectField label="Agent" value={nt.agentId} onChange={(e) => nt.setAgentId(e.target.value)} className="control-sm" error={nt.agentsError || undefined}>
        <AgentOptions nt={nt} />
      </SelectField>
      <ModelField nt={nt} />
      {nt.hasAccounts && (
        <SelectField label="Login profile" value={nt.profileId} onChange={(e) => nt.setProfileId(e.target.value)} className="control-sm">
          <AccountOptions profiles={nt.profileOptions} defaultProfile={nt.defaultProfile} providers={nt.providerOptions} />
        </SelectField>
      )}
      <Field label="Working directory" optional>
        {({ labelId: _l, ...a11y }) => (
          <div className="input-group has-lead has-addon">
            <span className="ig-lead">
              <Icon name="folder" />
            </span>
            <input className="control control-sm mono" placeholder={cwdPlaceholder(nt)} value={nt.cwd} onChange={(e) => nt.setCwd(e.target.value)} spellCheck={false} {...a11y} />
            <Button size="sm" className="ig-addon" onClick={onBrowse} disabled={!nt.machine}>
              Browse
            </Button>
          </div>
        )}
      </Field>
      <TextField label="Name" optional className="control-sm" placeholder={nt.agent?.name ?? 'Claude Code'} value={nt.name} onChange={(e) => nt.setName(e.target.value)} maxLength={80} />
      {nt.yoloable && <Checkbox checked={nt.yolo} onChange={() => yolo.toggle()} label="Yolo: run without permission prompts" />}
      <div className="row-end">
        <span className="fs-xs text-3 grow">Also used by the New console chip and the Spawn tool (T).</span>
        <Button type="submit" variant="primary" size="sm" icon="plus" loading={nt.creating} disabled={!nt.canCreate && !nt.creating}>
          New console
        </Button>
      </div>
      {yolo.dialog}
    </form>
  );
}
