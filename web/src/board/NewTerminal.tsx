// "New terminal" controls (DESIGN.md §3.5). Phase 6: the dock lives inside the command
// bar as the agent chip (armed agent + machine); the chip opens the same form as a
// popover (below 900px: the modal sheet). NewTerminal (the old dock) is kept for reuse.
// The dock's agent is the "armed" agent of the rail's Spawn tool (T), per client
// (docs/API-agents.md §1.2); availability and the default agent come per machine.

import { useEffect, useRef, useState, type CSSProperties, type FormEvent, type RefObject } from 'react';
import { errorMessage } from '../api';
import { agentProfiles, agentTagline, byPriority, hasYolo, isChatAgent, setArmed, useArmed, useMachineAgents } from '../agents/catalog';
import { ModelPicker } from '../agents/ModelPicker';
import { defaultChoice } from '../agents/models';
import { useEffectiveSettings } from '../agents/settings';
import { onMachine, YoloConfirm } from '../agents/YoloConfirm';
import { Button } from '../components/Button';
import { FolderBrowser } from '../components/FolderBrowser';
import { Checkbox, Field, SegmentedControl, SelectField, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { Popover } from '../components/Popover';
import { toast } from '../components/Toast';
import { isRoot, partNamesOn, partOn, type Placement } from '../lib/parts';
import { defaultPlacement, isMultiPart, lastPlacement, machineOfPlace, partOfPlace, placeFromValue, placeShort, placeValue, rememberPlacement, ROOT_VALUE } from '../lib/placement';
import type { Agent, Machine, Project, ModelChoice } from '../lib/types';
import { useMediaQuery } from '../lib/useMediaQuery';
import { agentChoiceName, foldEngines, isApiEngine } from '../nextcode/engines';
import { EngineSwitch } from '../nextcode/EngineSwitch';
import { nextcodeProvider } from '../settings/nextcodeSettings';
import { useMachines, useProfiles } from '../store';
import { AccountOptions, accountFor, isProviderId, NeedsProviderHint, needsProvider } from '../providers/AccountOptions';
import { compatible, useProviders } from '../providers/providers';
import { OTHER_VALUE, PlacePicker } from './PlacePicker';
import { machineReady, machineStatus } from './sessionStatus';
import { ResumeChatList } from '../chats/ResumeChatList';
import type { ChatView } from '../chats/types';

/** What the dock / Spawn tool would start. */
export interface TerminalChoice {
  /**
   * Its place in a project of several parts (v0.9.0-beta.2): the root, or a part (then machineId
   * is that part's and cwd an optional folder inside it). Absent: machineId and cwd say where, as
   * in a single-part project or "Another folder…".
   */
  place?: Placement;
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

/** Model and effort of the armed agent, in the forms (on an API provider: the provider's models). */
function ModelField({ nt }: { nt: ReturnType<typeof useNewTerminal> }) {
  if (!nt.agent || (!nt.agent.models && !nt.providerId)) return null;
  return (
    <div className="field">
      <span className="field-label">Model and effort</span>
      <ModelPicker agent={nt.agent} value={nt.modelChoice} onChange={nt.setModelChoice} profileId={nt.profileId} />
    </div>
  );
}

/** What the agent field says about the agent: NextCode's line, else the command it runs. */
function agentHint(a: Agent | undefined): string | undefined {
  if (!a) return undefined;
  return agentTagline(a) || `Runs ${a.command}`;
}

/** Pick the agent to show: armed (if installed here), else the machine's default. */
function resolveAgent(agents: Agent[], armed: string, defaultAgent: string): Agent | undefined {
  const ok = (a: Agent | undefined) => (a && a.available !== false ? a : undefined);
  return ok(agents.find((a) => a.id === armed)) ?? ok(agents.find((a) => a.id === defaultAgent)) ?? agents.find((a) => a.available !== false);
}

/**
 * State + actions shared by the dock, the modal form and the Spawn tool.
 * `spawn` places and starts the terminal (the board decides where); resolves true on success.
 * `focused` gives the placement of the board's focused tile: in a project of several parts the
 * new console goes there by default, else where the last one went (lib/placement.ts).
 */
export function useNewTerminal(project: Project, spawn: (c: TerminalChoice) => Promise<boolean>, focused?: () => Placement | null) {
  const machines = useMachines();
  const settings = useEffectiveSettings();
  const profiles = useProfiles();
  const providers = useProviders();
  const [chosenMachine, setMachineId] = useState(project.machineId || 'local');
  // "Place in" (several parts only): picked here, else the focused tile's, else the last used
  const multi = isMultiPart(project);
  const focusedPlace = multi && focused ? focused() : null;
  const focusKey = focusedPlace ? placeValue(focusedPlace) : '';
  const [pick, setPick] = useState<string | null>(null);
  // another focused tile: its placement again, until something is picked for it
  useEffect(() => setPick(null), [focusKey]);
  const picked = pick === ROOT_VALUE || pick === OTHER_VALUE || (pick && partOfPlace(project, { partId: pick })) ? pick : null;
  const placeVal = multi ? (picked ?? placeValue(defaultPlacement(project, focusedPlace, lastPlacement(project)))) : '';
  const place: Placement | null = multi && placeVal !== OTHER_VALUE ? placeFromValue(placeVal) : null;
  // the machine of the work: the part's, This PC for the root, else the one chosen
  const machineId = place ? machineOfPlace(project, place) : chosenMachine;
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
    if (machines.length > 0 && !machine && !place) setMachineId(machines[0].id);
  }, [machines, machine, place]);

  // Another project → its machine (the cwd defaults to the project folder).
  useEffect(() => {
    setMachineId(project.machineId || 'local');
    setCwd('');
    setPick(null);
  }, [project.id, project.machineId]);

  const agent = resolveAgent(ma.agents, armed, ma.defaultAgent);
  const agentId = agent?.id ?? '';
  // NextCode has permission modes (Settings › NextCode) instead of Yolo
  const yoloable = hasYolo(agent);
  const yolo = yoloable ? (yoloChoice ?? settings.agents.yoloDefault) : false;
  // its own logins only: NextCode never gets Claude Code's, nor Claude Code NextCode's
  const profileOptions = agentProfiles(profiles, machineId, agent);
  // agents run on This PC, whatever machine they work on, so any provider that can run this one
  const providerOptions = agent ? providers.filter((p) => compatible(agent, p)) : [];
  // NextCode's API engine has no login: a provider always, Settings › NextCode's default first
  const providersOnly = isApiEngine(agent);
  const defaultProvider = nextcodeProvider(settings);
  const account = accountFor(agent, profileId, { profiles: profileOptions, providers: providerOptions }, defaultProvider);
  const noProvider = needsProvider(agent, providerOptions);
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

  const setPlace = (v: string) => {
    setPick(v);
    setCwd(''); // paths are per part
  };

  /**
   * The current settings. With a placement, cwd is a folder inside the part (empty: the part's
   * folder; the root takes none); without, it falls back to the folder of the project's part on
   * that machine.
   */
  const choice = (): TerminalChoice => ({
    ...(place ? { place } : {}),
    machineId,
    preset: agentId,
    name: name.trim(),
    cwd: place ? (isRoot(place) ? '' : cwd.trim()) : cwd.trim() || (partOn(project, machineId)?.path ?? ''),
    // NextCode asks in the permission mode of its settings, never in the hub's Yolo default
    yolo: isChatAgent(agent) ? false : yoloable && yoloChoice !== undefined ? yoloChoice : undefined,
    profileId: account && !isProviderId(account) ? account : undefined,
    providerId: isProviderId(account) ? account : undefined,
    model: modelChoices[agentId]?.model,
    effort: modelChoices[agentId]?.effort,
  });

  /** Returns true on success. */
  const create = async (): Promise<boolean> => {
    if (!machineId || !agentId || creating || noProvider) return false;
    setCreating(true);
    try {
      const ok = await spawn(choice());
      if (ok) {
        if (agentId !== armed) setArmed(agentId);
        if (place) rememberPlacement(project, place);
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
    /** A project of several parts: "Place in" shows. */
    multi,
    /** The placement the next console gets (null: a single-part project, or "Another folder…"). */
    place,
    /** The picker's value: placeValue(place), or OTHER_VALUE. */
    placeValue: placeVal,
    setPlace,
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
    /** NextCode's API engine: the account is an API provider, never a login. */
    providersOnly,
    /** Settings › NextCode's provider for the API engine ('' = none). */
    defaultProvider,
    /** The API engine with no API provider at all: nothing can start it. */
    noProvider,
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
    canCreate: !!machineId && !!agentId && !creating && !noProvider,
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
      {nt.machines.map((m) => {
        // a project on several machines: which of its parts each one holds
        const parts = partNamesOn(nt.project, m.id);
        return (
          <option key={m.id} value={m.id}>
            {machineOptionLabel(m)}
            {parts ? ` · ${parts}` : ''}
          </option>
        );
      })}
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

/** Agent options grouped like the pickers: Detected · Your agents · Shell · Not installed. NextCode once (EngineField picks its engine). */
function AgentOptions({ nt }: { nt: NT }) {
  if (nt.agents.length === 0) return <option value="">{nt.agentsError ? 'Agents unavailable' : 'Loading…'}</option>;
  const agents = foldEngines(nt.agents, nt.agentId);
  const detected = agents.filter((a) => a.source !== 'custom' && a.kind === 'agent' && a.available !== false).sort(byPriority);
  const mine = agents.filter((a) => a.source === 'custom').sort((a, b) => a.name.localeCompare(b.name));
  const shells = agents.filter((a) => a.source !== 'custom' && a.kind === 'shell');
  const missing = agents.filter((a) => a.source !== 'custom' && a.kind === 'agent' && a.available === false).sort(byPriority);
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

/** NextCode's engine, under the agent field while NextCode is chosen. */
function EngineField({ nt }: { nt: NT }) {
  return <EngineSwitch agents={nt.agents} value={nt.agentId} onChange={nt.setAgentId} where={nt.machine?.name} field />;
}

/**
 * The login profile or API provider of the forms. NextCode's API engine lists its API providers
 * only, and says where to add one while there is none.
 */
function AccountField({ nt, className }: { nt: NT; className?: string }) {
  if (nt.noProvider) return <NeedsProviderHint />;
  if (!nt.hasAccounts) return null;
  return (
    <SelectField label={nt.providersOnly ? 'API provider' : 'Login profile'} value={nt.profileId} onChange={(e) => nt.setProfileId(e.target.value)} className={className}>
      <AccountOptions profiles={nt.profileOptions} defaultProfile={nt.defaultProfile} providers={nt.providerOptions} providersOnly={nt.providersOnly} defaultProvider={nt.defaultProvider} />
    </SelectField>
  );
}

/** Placeholder for the cwd field: the folder of the placed part, else of the project's part on that machine, if any. */
function cwdPlaceholder(nt: NT): string {
  return partOfPlace(nt.project, nt.place)?.path ?? partOn(nt.project, nt.machineId)?.path ?? (nt.machine?.probe?.home || '~');
}

/** Where Browse starts: the folder typed, else the placed part's, else the project's part on that machine. */
function browseFrom(nt: NT): string {
  return nt.cwd.trim() || (partOfPlace(nt.project, nt.place)?.path ?? partOn(nt.project, nt.machineId)?.path ?? '');
}

/** The working-directory field shows unless the console goes to the root (the hub picks its folder). */
const askCwd = (nt: NT) => !nt.place || !isRoot(nt.place);

/** In a project of several parts the machine follows the placement; it is asked only for "Another folder…". */
const askMachine = (nt: NT) => !nt.multi || nt.placeValue === OTHER_VALUE;

/** "Place in" for the forms (renders nothing in a single-part project). */
function PlaceField({ nt, size = 'sm' }: { nt: NT; size?: 'sm' | 'md' }) {
  return <PlacePicker project={nt.project} value={nt.placeValue} onChange={nt.setPlace} other size={size} />;
}

/** The board's side of "Resume a chat": its id for the dialogs, and the Resume dialog to open. */
export interface ResumeFrom {
  workspaceId: string;
  onResume: (c: ChatView) => void;
}

type NtTab = 'fresh' | 'resume';

/** "Start fresh / Resume a chat" at the top of the new console form (mockup 4). */
function NtTabs({ tab, onTab }: { tab: NtTab; onTab: (t: NtTab) => void }) {
  return (
    <SegmentedControl
      label="New console"
      block
      size="sm"
      className="nt-tabs"
      value={tab}
      onChange={onTab}
      options={[
        { value: 'fresh', label: 'Start fresh' },
        { value: 'resume', label: 'Resume a chat', title: 'A conversation an agent had in this project, where it started' },
      ]}
    />
  );
}

/** The chats of the place picked in the form, the armed agent's first. */
function ResumeTab({ nt, from, onDone }: { nt: NT; from: ResumeFrom; onDone: () => void }) {
  return (
    <div className="nt-resume">
      <ResumeChatList
        project={nt.project}
        workspaceId={from.workspaceId}
        place={nt.multi ? (nt.placeValue === OTHER_VALUE ? ROOT_VALUE : nt.placeValue) : ''}
        onPlace={nt.setPlace}
        agent={nt.agentId}
        onResume={(c) => {
          onDone();
          from.onResume(c);
        }}
        onShown={onDone}
      />
      <p className="fs-xs text-3">Chats started in another place are under that place.</p>
    </div>
  );
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
      initialPath={browseFrom(nt)}
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
      {nt.multi && <PlacePicker project={nt.project} value={nt.placeValue} onChange={nt.setPlace} other label={null} hint={false} block={false} className="dock-place" />}
      {askMachine(nt) && (
        <div className="input-group has-lead dock-machine" title={mstatus ? `Machine status: ${mstatus.label}` : 'Machine'}>
          <span className="ig-lead">
            <span className={`dot dot-${mstatus?.color ?? 'grey'}`} aria-hidden="true" />
          </span>
          <select className="control control-sm" value={nt.machineId} onChange={(e) => nt.changeMachine(e.target.value)} aria-label="Machine">
            <MachineOptions nt={nt} />
          </select>
        </div>
      )}

      <div className="input-group has-lead dock-preset" title={nt.agentsError || agentHint(nt.agent) || 'Agent'}>
        <span className="ig-lead">
          <span className="swatch" style={{ '--swatch': nt.agent?.color } as CSSProperties} aria-hidden="true" />
        </span>
        <select className="control control-sm" value={nt.agentId} onChange={(e) => nt.setAgentId(e.target.value)} aria-label="Agent">
          <AgentOptions nt={nt} />
        </select>
      </div>
      <EngineSwitch agents={nt.agents} value={nt.agentId} onChange={nt.setAgentId} where={nt.machine?.name} className="dock-engine" />

      {nt.hasAccounts && (
        <div className="input-group has-lead dock-profile" title={nt.providersOnly ? 'API provider' : 'Login profile or API provider'}>
          <span className="ig-lead">
            <Icon name="user" />
          </span>
          <select className="control control-sm" value={nt.profileId} onChange={(e) => nt.setProfileId(e.target.value)} aria-label={nt.providersOnly ? 'API provider' : 'Login profile'}>
            <AccountOptions profiles={nt.profileOptions} defaultProfile={nt.defaultProfile} providers={nt.providerOptions} providersOnly={nt.providersOnly} defaultProvider={nt.defaultProvider} />
          </select>
        </div>
      )}

      {askCwd(nt) && (
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
            title={nt.place ? 'Working directory (empty = the part’s folder)' : 'Working directory (empty = project folder)'}
            spellCheck={false}
          />
          <Button size="sm" className="ig-addon" onClick={onBrowse} disabled={!nt.machine}>
            Browse
          </Button>
        </div>
      )}

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

      <Button type="submit" variant="primary" size="sm" icon="plus" loading={nt.creating} disabled={!nt.canCreate && !nt.creating} title={nt.noProvider ? 'Add an API provider in Settings › API providers first' : undefined}>
        New terminal
      </Button>
      {yolo.dialog}
    </form>
  );
}

/** Same form as a modal for narrow screens. */
function NewTerminalSheet({ nt, onClose, onBrowse, resume }: { nt: NT; onClose: () => void; onBrowse: () => void; resume?: ResumeFrom }) {
  const yolo = useYoloToggle(nt);
  const [tab, setTab] = useState<NtTab>('fresh');
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
        tab === 'resume' ? (
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
        ) : (
          <>
            <Button variant="secondary" onClick={onClose} disabled={nt.creating}>
              Cancel
            </Button>
            <Button type="submit" form="new-terminal-form" variant="primary" icon="plus" loading={nt.creating} disabled={!nt.canCreate && !nt.creating}>
              Start terminal
            </Button>
          </>
        )
      }
    >
      {resume && <NtTabs tab={tab} onTab={setTab} />}
      {resume && tab === 'resume' ? (
        <ResumeTab nt={nt} from={resume} onDone={onClose} />
      ) : (
        <form id="new-terminal-form" className="form" onSubmit={submit}>
          <fieldset disabled={nt.creating}>
            <PlaceField nt={nt} size="md" />
            {askMachine(nt) && (
              <SelectField label="Machine" value={nt.machineId} onChange={(e) => nt.changeMachine(e.target.value)} hint={serverHint(nt)}>
                <MachineOptions nt={nt} />
              </SelectField>
            )}
            <SelectField
              label="Agent"
              value={nt.agentId}
              onChange={(e) => nt.setAgentId(e.target.value)}
              hint={agentHint(nt.agent)}
              error={nt.agentsError || undefined}
            >
              <AgentOptions nt={nt} />
            </SelectField>
            <EngineField nt={nt} />
            <ModelField nt={nt} />
            <AccountField nt={nt} />
            {askCwd(nt) && (
              <Field label="Working directory" optional hint={nt.place ? 'Leave empty to start in the part’s folder.' : 'Leave empty to start in the project folder.'}>
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
            )}
            <TextField label="Name" optional placeholder={nt.agent?.name ?? 'Claude Code'} value={nt.name} onChange={(e) => nt.setName(e.target.value)} maxLength={80} />
            {nt.yoloable && <Checkbox checked={nt.yolo} onChange={() => yolo.toggle()} label="Yolo: run without permission prompts" />}
          </fieldset>
        </form>
      )}
      {yolo.dialog}
    </Modal>
  );
}

/**
 * Agent chip of the command bar: the armed agent (and machine when not the project's).
 * Opens the new-console form as a popover (desktop) or the sheet (narrow screens).
 */
export function AgentChip({ nt, anchor, resume }: { nt: NT; anchor?: RefObject<HTMLElement | null>; resume?: ResumeFrom }) {
  const compact = useMediaQuery('(max-width: 899px)');
  const [open, setOpen] = useState(false);
  const [tab, setTab] = useState<NtTab>('fresh');
  const [browsing, setBrowsing] = useState(false);
  const ref = useRef<HTMLButtonElement>(null);
  const remote = nt.machine && nt.machine.id !== nt.project.machineId;
  // several parts: where the next console goes, always ("Claude Code · Root", "Claude Code · admin")
  const where = nt.place ? placeShort(nt.project, nt.place) : nt.multi ? (nt.machine?.name ?? '') : remote ? (nt.machine?.name ?? '') : '';
  const label = nt.agent ? `${agentChoiceName(nt.agent, nt.agents)}${where ? ` · ${where}` : ''}` : 'Pick an agent';
  const folderBrowser = browsing && nt.machine && (
    <FolderBrowser
      machineId={nt.machine.id}
      machineName={nt.machine.name}
      initialPath={browseFrom(nt)}
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
      {open && compact && <NewTerminalSheet nt={nt} onClose={() => setOpen(false)} onBrowse={() => setBrowsing(true)} resume={resume} />}
      {open && !compact && (
        <Popover anchor={anchor ?? ref} onClose={() => !browsing && setOpen(false)} label="New console" placement="above" align="start" className="nt-pop" width={resume && tab === 'resume' ? 460 : 340}>
          {resume && (
            <div className="nt-panel-tabs">
              <div className="pop-head">
                <Icon name="terminal" />
                <strong>New console</strong>
              </div>
              <NtTabs tab={tab} onTab={setTab} />
            </div>
          )}
          {resume && tab === 'resume' ? (
            <ResumeTab nt={nt} from={resume} onDone={() => setOpen(false)} />
          ) : (
            <NewTerminalPanel
              nt={nt}
              onBrowse={() => setBrowsing(true)}
              head={!resume}
              onDone={() => {
                setOpen(false);
                ref.current?.focus();
              }}
            />
          )}
        </Popover>
      )}
      {folderBrowser}
    </>
  );
}

/** The dock's controls stacked in a popover. */
function NewTerminalPanel({ nt, onBrowse, onDone, head = true }: { nt: NT; onBrowse: () => void; onDone: () => void; head?: boolean }) {
  const yolo = useYoloToggle(nt);
  const mstatus = nt.machine ? machineStatus(nt.machine) : null;
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (await nt.create()) onDone();
  };
  return (
    <form className="nt-panel form" onSubmit={submit} aria-label="New console">
      {head && (
        <div className="pop-head">
          <Icon name="terminal" />
          <strong>New console</strong>
        </div>
      )}
      <PlaceField nt={nt} />
      {askMachine(nt) && (
        <SelectField label="Machine" value={nt.machineId} onChange={(e) => nt.changeMachine(e.target.value)} className="control-sm" hint={serverHint(nt) ?? (mstatus ? `Status: ${mstatus.label}` : undefined)}>
          <MachineOptions nt={nt} />
        </SelectField>
      )}
      <SelectField label="Agent" value={nt.agentId} onChange={(e) => nt.setAgentId(e.target.value)} className="control-sm" error={nt.agentsError || undefined} hint={agentTagline(nt.agent) || undefined}>
        <AgentOptions nt={nt} />
      </SelectField>
      <EngineField nt={nt} />
      <ModelField nt={nt} />
      <AccountField nt={nt} className="control-sm" />
      {askCwd(nt) && (
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
      )}
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
