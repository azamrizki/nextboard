// Terminal node header (DESIGN.md §3.5, docs/API-agents.md §2/§17): state dot, handle
// (double-click to rename), title "@ machine", YOLO / profile badges, "Output", the
// agent-state pill (+ activity), remote badge and the actions menu.
// useConsoleActions() holds the agent actions and their dialogs so the header menu and
// the exit bar share them.

import { useRef, useState, useSyncExternalStore, type KeyboardEvent, type ReactNode } from 'react';
import { api, errorMessage, isCode } from '../api';
import { launchAgent, restartConsole, resumeMode } from '../agents/actions';
import { modelSummary } from '../agents/models';
import { SwitchAgentDialog } from '../agents/AgentPicker';
import { canContinue, canPickInCli, canResumeExact, hasTranscript, hasYolo, useAgent } from '../agents/catalog';
import { OutputViewer } from '../agents/OutputViewer';
import { ResumePicker } from '../agents/ResumePicker';
import { agentPill, handleOf, handleProblem, hasOutput, initials, isAgentConsole, type Pill } from '../agents/state';
import { stepScale } from '../agents/textSize';
import { onMachine, YoloConfirm } from '../agents/YoloConfirm';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon } from '../components/Icon';
import { Menu, type MenuEntry } from '../components/Menu';
import { toast } from '../components/Toast';
import type { SocketState } from '../lib/socket';
import type { Agent, Conversation, LaunchMode, Machine, Session } from '../lib/types';
import { store, useProfile } from '../store';
import { rectOf } from './controller';
import { DisplayButton } from './DisplayButton';
import { placeNear } from './layout';
import { machineStatus } from './sessionStatus';
import { useBoardEnv } from './ui';
import { QueueBadge } from '../orch/DeliveryQueue';
import { isAgentSession, orchError, pauseSessions, resumeSessions } from '../orch/actions';
import { orchApi } from '../orch/api';
import { orchStore, useOrch } from '../orch/store';
import { groupOf, frameOps } from './groups';
import { readGroup } from './kinds';

/** Remote badge tint follows the machine's connection state. */
const REMOTE_BADGE: Record<string, string> = {
  success: 'badge-info',
  busy: 'badge-info',
  warning: 'badge-warning',
  danger: 'badge-danger',
  grey: '',
};

/** CSS class used as React Flow's dragHandle selector. */
export const DRAG_HANDLE_CLASS = 'nb-node-header';

type Dialog =
  | { t: 'close' }
  | { t: 'restart'; mode: Exclude<LaunchMode, 'shell'>; agentSessionId?: string }
  | { t: 'wrap'; mode: Exclude<LaunchMode, 'shell'>; agentSessionId?: string }
  | { t: 'yoloOn' }
  | { t: 'yoloOff' }
  | { t: 'switch'; agent?: string }
  | { t: 'picker' }
  | { t: 'output' };

const who = (s: Session) => (s.handle ? `@${s.handle}` : `“${s.name}”`);

const HANDOFF_OVER = ['done', 'failed', 'manual'];

function switchStep(state: string, to: string): string {
  switch (state) {
    case 'collecting':
    case 'summarizing':
      return `Writing a brief for ${to}…`;
    case 'delivering':
      return `Briefing ${to}…`;
    default:
      return `Starting ${to}…`;
  }
}

/** Agent actions of one console + the dialogs they open. */
export function useConsoleActions(nodeId: string, session: Session, machine: Machine | undefined) {
  const { ctl } = useBoardEnv();
  const agent = useAgent(session.agent || session.preset);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const close = () => setDialog(null);
  const agentRunning = session.status === 'running' && session.agentState !== 'shell' && session.agentState !== undefined;
  const busyTurn = session.agentState === 'working' || session.agentState === 'needsYou';

  /** Start / restart the agent; asks first when that would interrupt a turn. */
  const launch = async (mode: Exclude<LaunchMode, 'shell'>, agentSessionId?: string, confirmed = false) => {
    if (!confirmed && agentRunning && busyTurn) {
      setDialog({ t: 'restart', mode, agentSessionId });
      return;
    }
    const r = await launchAgent(session, mode, agentSessionId);
    if (r === 'wrap_missing') setDialog({ t: 'wrap', mode, agentSessionId });
  };

  const restartAgent = () => void launch('fresh');
  const resume = () => {
    const r = canResumeExact(agent) ? resumeMode(session) : { mode: 'continue' as const };
    void launch(r.mode, r.agentSessionId);
  };
  const pick = () => {
    if (hasTranscript(agent)) setDialog({ t: 'picker' });
    else if (canPickInCli(agent)) void launch('pick');
  };

  const resumeNew = async (c: Conversation) => {
    const n = ctl.node(nodeId);
    const base = n ? rectOf(n) : { x: 0, y: 0, w: 720, h: 440 };
    const rect = placeNear(ctl.nodes.map(rectOf), { x: base.x + base.w * 1.5 + 40, y: base.y + base.h / 2 }, base.w, base.h);
    const s = await ctl.spawn({
      machineId: session.machineId,
      preset: session.agent || session.preset,
      cwd: session.cwd,
      name: '',
      rect,
      profileId: session.profileId || undefined,
      launch: { mode: 'resume', agentSessionId: c.agentSessionId },
    });
    if (!s) throw new Error('The new console could not be started.');
  };

  const resumeHere = async (c: Conversation) => {
    // a running agent switches conversations with one restart (Phase 5 agentSessionId)
    if (session.status === 'running' && session.agentState !== 'shell' && session.foreground !== 'shell') {
      if (!(await restartConsole(session, { mode: 'resume', agentSessionId: c.agentSessionId }, 'Resume'))) throw new Error('The conversation could not be resumed here.');
      return;
    }
    const r = await launchAgent(session, 'resume', c.agentSessionId);
    if (r === 'wrap_missing') setDialog({ t: 'wrap', mode: 'resume', agentSessionId: c.agentSessionId });
    else if (r !== 'ok') throw new Error('The conversation could not be resumed here.');
  };

  const dialogs: ReactNode = (() => {
    if (!dialog) return null;
    switch (dialog.t) {
      case 'close':
        return (
          <ConfirmDialog title="Close this terminal?" confirmLabel="Close terminal" danger onClose={close} onConfirm={() => ctl.deleteNodes([nodeId])}>
            <p>
              <strong>{handleOf(session)}</strong> on {session.machineName} will be killed and removed from the board. Its scrollback is
              discarded. Undo (Ctrl+Z) starts a fresh terminal in its place.
            </p>
          </ConfirmDialog>
        );
      case 'restart':
        return (
          <ConfirmDialog
            title={`Restart ${agent?.name ?? 'the agent'}?`}
            confirmLabel="Restart agent"
            icon="restart"
            onClose={close}
            onConfirm={() => launch(dialog.mode, dialog.agentSessionId, true)}
          >
            <p>
              {who(session)} is {session.agentState === 'needsYou' ? 'waiting for your answer' : 'in the middle of a turn'}. Restarting stops it; the
              console and its scrollback stay.
            </p>
          </ConfirmDialog>
        );
      case 'wrap':
        return (
          <ConfirmDialog
            title="Restart the whole console?"
            confirmLabel="Restart console"
            icon="restart"
            onClose={close}
            onConfirm={async () => {
              if (!(await restartConsole(session, { mode: dialog.mode }))) throw new Error('Restart failed.');
            }}
          >
            <p>
              The agent did not start from the shell in {who(session)} (the shell may have changed its environment). A full restart launches
              the console again in the same folder; the scrollback stays.
            </p>
          </ConfirmDialog>
        );
      case 'yoloOn':
        return (
          <YoloConfirm
            subject={
              <>
                {who(session)} ({agent?.name ?? session.name}) {onMachine(machine, session.machineName)}
              </>
            }
            extra="NextBoard restarts the agent now and resumes this conversation."
            onClose={close}
            onConfirm={async () => {
              if (!(await restartConsole(session, { ...resumeBody(session, agent), yolo: true }, 'Yolo restart'))) throw new Error('Restart failed.');
            }}
          />
        );
      case 'yoloOff':
        return (
          <ConfirmDialog
            title="Turn off Yolo?"
            confirmLabel="Restart with prompts"
            icon="shield"
            onClose={close}
            onConfirm={async () => {
              if (!(await restartConsole(session, { ...resumeBody(session, agent), yolo: false }, 'Restart'))) throw new Error('Restart failed.');
            }}
          >
            <p>{who(session)} restarts and resumes this conversation, asking for permission again before it edits files or runs commands.</p>
          </ConfirmDialog>
        );
      case 'switch':
        return (
          <SwitchAgentDialog
            session={session}
            initial={dialog.agent}
            onClose={close}
            onModel={async (c) => {
              const ok = await restartConsole(session, { ...resumeBody(session, agent), model: c.model ?? '', effort: c.effort ?? '' }, 'Model');
              if (!ok) throw new Error('The console did not restart.');
            }}
            onSwitch={async (agentId, profileId, { brief, note, fresh, ...opts }) => {
              try {
                orchStore.upsertHandoff(await orchApi.handoff(session.id, { model: opts.model, effort: opts.effort, agent: agentId, profileId, inPlace: true, noBrief: !brief, note: note || undefined, fresh }));
              } catch (err) {
                throw new Error(orchError(err));
              }
            }}
          />
        );
      case 'picker':
        return <ResumePicker session={session} agent={agent} onClose={close} onResumeHere={resumeHere} onResumeNew={resumeNew} />;
      case 'output':
        return <OutputViewer sessionId={session.id} onClose={close} />;
    }
  })();

  return { agent, dialog, setDialog, restartAgent, resume, pick, dialogs, agentRunning };
}

/** Restart body that keeps the conversation (exact when possible). */
function resumeBody(s: Session, a: Agent | undefined) {
  if (canResumeExact(a) && s.agentSessionId) return { mode: 'resume' as const };
  if (canContinue(a)) return { mode: 'continue' as const };
  return { mode: 'fresh' as const };
}

export function AgentPillView({ pill }: { pill: Pill }) {
  return (
    <span className="node-pill-wrap">
      <span className={`agent-pill tone-${pill.tone}`} title={pill.detail} aria-label={`Status: ${pill.label}${pill.activity ? ` — ${pill.activity}` : ''}`}>
        {pill.tone === 'needs' && <Icon name="bell" />}
        <span className="agent-pill-label">{pill.label}</span>
      </span>
      {pill.activity && (
        <span className="node-activity truncate" title={pill.activity} aria-hidden="true">
          {pill.activity}
        </span>
      )}
    </span>
  );
}

interface NodeHeaderProps {
  /** Board node id (for undo when the terminal is closed). */
  nodeId: string;
  session: Session;
  machine: Machine | undefined;
  socket: SocketState;
  actions: ReturnType<typeof useConsoleActions>;
  /** Per-console text size override (null = global) and the effective scale. */
  textOverride: number | null;
  textScale: number;
}

export function NodeHeader({ nodeId, session, machine, socket, actions, textOverride, textScale }: NodeHeaderProps) {
  const { ctl, ui } = useBoardEnv();
  const profile = useProfile(session.profileId || undefined);
  const linked = useSyncExternalStore(ctl.sync.subscribe, () => {
    const g = groupOf(ctl.nodes, nodeId);
    return g ? readGroup(g).syncInput : false;
  });
  const [renaming, setRenaming] = useState<'handle' | 'name' | null>(null);
  const [draft, setDraft] = useState('');
  const [draftError, setDraftError] = useState('');
  // Guards against Enter + blur both committing the same rename.
  const renamingRef = useRef(false);

  const { agent, setDialog } = actions;
  const switching = useOrch((st) => Object.values(st.handoffs).find((h) => h.inPlace && h.sourceSessionId === session.id && !HANDOFF_OVER.includes(h.state)));
  const switchingTo = useAgent(switching?.agent ?? '');
  const backTo = session.pastRuns?.find((r) => r.agent !== session.agent && r.agentSessionId);
  const backToAgent = useAgent(backTo?.agent);
  const statePill = agentPill(session, socket);
  // While it changes hands the tile says so: the agent's own state is not the story.
  const pill: Pill = switching
    ? { ...statePill, state: 'working', label: 'Switching', dot: 'busy', tone: 'info', activity: switchStep(switching.state, switchingTo?.name ?? switching.agent) }
    : statePill;
  const exited = session.status === 'exited';
  const isRemote = machine?.kind === 'ssh';
  const agentConsole = isAgentConsole(session) && session.agentState !== undefined;
  const yoloable = hasYolo(agent);
  const atShell = session.agentState === 'shell';

  const startRename = (what: 'handle' | 'name') => {
    if (what === 'handle' && !session.handle) what = 'name';
    setDraft(what === 'handle' ? (session.handle ?? '') : session.name);
    setDraftError('');
    renamingRef.current = true;
    setRenaming(what);
  };

  const commitRename = () => {
    if (!renamingRef.current || !renaming) return;
    const what = renaming;
    const value = draft.trim();
    if (what === 'handle' && value && value !== session.handle) {
      const problem = handleProblem(value);
      if (problem) {
        setDraftError(problem);
        return;
      }
    }
    renamingRef.current = false;
    setRenaming(null);
    if (!value) return;
    if (what === 'handle') {
      if (value === session.handle) return;
      api
        .setHandle(session.id, value)
        .then((s) => store.upsertSession(s))
        .catch((err) =>
          toast.error(isCode(err, 'handle_taken') ? `@${value} is already used by another console. Pick another name.` : `Could not rename ${who(session)}: ${errorMessage(err)}`),
        );
    } else if (value !== session.name) {
      api
        .renameSession(session.id, value)
        .then((s) => store.upsertSession(s))
        .catch((err) => toast.error(`Rename failed for ${who(session)}: ${errorMessage(err)}`));
    }
  };

  const onRenameKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === 'Enter') commitRename();
    else if (e.key === 'Escape') {
      e.stopPropagation();
      renamingRef.current = false;
      setRenaming(null);
    }
  };

  const setText = (v: number | null) => ctl.updateData(nodeId, { textScale: v });
  const dec = stepScale(textScale, -1);
  const inc = stepScale(textScale, 1);

  const items: MenuEntry[] = [
    { label: session.handle ? 'Rename handle' : 'Rename', icon: 'pencil', onSelect: () => startRename('handle') },
  ];
  if (session.handle) items.push({ label: 'Rename title', icon: 'type', onSelect: () => startRename('name') });
  if (hasOutput(session) || session.reply) items.push({ label: 'See output', icon: 'fileText', onSelect: () => setDialog({ t: 'output' }) });
  if (agentConsole) {
    items.push('sep', { type: 'heading', label: agent?.name ?? 'Agent' });
    items.push({ label: atShell ? 'Start agent' : 'Restart agent', icon: 'restart', disabled: exited, onSelect: actions.restartAgent });
    if (canResumeExact(agent) || canContinue(agent))
      items.push({
        label: canResumeExact(agent) && session.agentSessionId ? 'Resume this conversation' : 'Continue last conversation',
        icon: 'resume',
        disabled: exited,
        onSelect: actions.resume,
      });
    if (hasTranscript(agent) || canPickInCli(agent)) items.push({ label: 'Pick conversation…', icon: 'history', disabled: exited, onSelect: actions.pick });
    if (yoloable)
      items.push(
        session.yolo
          ? { label: 'Turn off Yolo…', icon: 'shield', disabled: exited, onSelect: () => setDialog({ t: 'yoloOff' }) }
          : { label: 'Run with Yolo…', icon: 'bolt', disabled: exited, onSelect: () => setDialog({ t: 'yoloOn' }) },
      );
  }
  if (!agentConsole && (session.resumeArgs?.length ?? 0) > 0)
    items.push({ label: 'Restart and resume', icon: 'play', disabled: exited, title: `Restart with ${session.resumeArgs?.join(' ')}`, onSelect: () => void restartConsole(session, { resume: true }) });
  // A console whose agent has ended is exactly one you may want to give to another.
  items.push({ label: 'Agent and model…', icon: 'bot', disabled: session.agentState === undefined || !!switching, onSelect: () => setDialog({ t: 'switch' }) });
  if (backTo) {
    items.push({ label: `Back to ${backToAgent?.name ?? backTo.agent}…`, icon: 'history', disabled: !!switching, onSelect: () => setDialog({ t: 'switch', agent: backTo.agent }) });
  }
  // Phase 6: pause / resume, hand off, forward, group membership
  if (agentConsole && isAgentSession(session)) {
    items.push('sep', { type: 'heading', label: 'Orchestration' });
    items.push(
      session.paused
        ? { label: 'Resume', icon: 'play', onSelect: () => void resumeSessions([session]) }
        : { label: 'Pause', icon: 'pause', disabled: exited || atShell, title: 'Stop the process until you resume it (state is kept)', onSelect: () => void pauseSessions([session]) },
      { label: 'Hand off…', icon: 'handoff', disabled: exited || atShell, title: 'Continue this task in a new console of another agent', onSelect: () => ui.set({ handoff: session.id }) },
    );
  }
  items.push({ label: 'Forward results to…', icon: 'forward', title: 'Then click the console or group that should receive them', onSelect: () => ui.set({ connectFrom: nodeId }) });
  const group = groupOf(ctl.nodes, nodeId);
  if (group) {
    items.push({
      label: `Remove from ${readGroup(group).name}`,
      icon: 'ungroup',
      onSelect: () => {
        const rest = readGroup(group).members.filter((m) => m !== nodeId);
        ctl.apply([{ op: 'patch', id: group.id, set: { data: { members: rest } } }, ...frameOps(ctl.nodes, [group.id], undefined, new Map([[group.id, rest]]))]);
      },
    });
  }
  items.push('sep', {
    type: 'stepper',
    label: 'Text',
    value: textOverride === null ? `${textScale}%` : `${textScale}%*`,
    decLabel: 'Smaller text',
    incLabel: 'Larger text',
    resetLabel: 'Global',
    onDec: dec ? () => setText(dec) : undefined,
    onInc: inc ? () => setText(inc) : undefined,
    onReset: textOverride !== null ? () => setText(null) : undefined,
  });
  items.push(
    'sep',
    {
      label: 'Restart console',
      icon: 'restart',
      title: 'Kill the console and launch it again (scrollback stays)',
      onSelect: () => void restartConsole(session, agentConsole ? resumeBody(session, agent) : {}),
    },
    {
      label: 'Kill process',
      icon: 'power',
      disabled: exited,
      onSelect: () =>
        void api
          .killSession(session.id)
          .then((s) => store.upsertSession(s))
          .catch((err) => toast.error(`Kill failed for ${who(session)}: ${errorMessage(err)}`)),
    },
    'sep',
    { label: 'Close terminal…', icon: 'trash', danger: true, onSelect: () => setDialog({ t: 'close' }) },
  );

  const mstatus = machine ? machineStatus(machine) : null;
  const showHandle = !!session.handle;

  return (
    <div className={DRAG_HANDLE_CLASS} title={session.cwd ? `${handleOf(session)} — ${session.remoteCwd || session.cwd}` : handleOf(session)}>
      <span className="tile-mark" aria-hidden="true">
        <span className={`dot dot-${pill.dot}`} />
      </span>

      {renaming ? (
        <span className="node-rename-wrap nodrag">
          <input
            className="control control-sm node-rename"
            value={draft}
            autoFocus
            aria-label={renaming === 'handle' ? 'Console handle' : 'Terminal title'}
            aria-invalid={draftError ? true : undefined}
            title={draftError || undefined}
            onFocus={(e) => e.currentTarget.select()}
            onChange={(e) => {
              setDraft(e.target.value);
              setDraftError('');
            }}
            onKeyDown={onRenameKey}
            onBlur={() => {
              if (draftError) {
                renamingRef.current = false;
                setRenaming(null);
                toast.error(draftError);
                return;
              }
              commitRename();
            }}
            maxLength={renaming === 'handle' ? 20 : 80}
          />
          {draftError && (
            <span className="node-rename-error" role="alert">
              {draftError}
            </span>
          )}
        </span>
      ) : (
        <span className="node-title">
          <span
            className={showHandle ? 'node-handle' : 'node-name'}
            onDoubleClick={(e) => {
              e.stopPropagation();
              startRename('handle');
            }}
          >
            {showHandle ? `@${session.handle}` : session.name}
          </span>
          {showHandle && (
            <span
              className="node-name-sub"
              onDoubleClick={(e) => {
                e.stopPropagation();
                startRename('name');
              }}
            >
              · {session.name}
            </span>
          )}
          {modelSummary(session.model, session.effort) && (
            <span className="node-model truncate" title={`Model and effort of this console. They stay when it restarts. Change them under Agent and model…`}>
              {modelSummary(session.model, session.effort)}
            </span>
          )}
          <span className="node-at" title={session.remoteMachineId ? 'The agent runs on This PC and works on this server' : undefined}>
            @ {machine?.name ?? session.machineName}
          </span>
        </span>
      )}

      {session.yolo && (
        <span className="badge badge-warning node-badge" title="Yolo: this agent runs without permission prompts">
          <Icon name="bolt" />
          YOLO
        </span>
      )}
      {session.profileId && (
        <span className="badge badge-info node-badge" title={`Login profile: ${profile?.name ?? session.profileId}`} aria-label={`Login profile ${profile?.name ?? ''}`}>
          <Icon name="user" />
          {profile ? initials(profile.name) : '…'}
        </span>
      )}

      {linked && (
        <span className="node-linked" title="Input linked: what you type here also goes to the rest of the group">
          <Icon name="link" />
          <span className="sr-only">Input linked</span>
        </span>
      )}
      <span className="spacer" />

      {hasOutput(session) && (
        <Button size="sm" variant="ghost" icon="fileText" className="nodrag node-output" onClick={() => setDialog({ t: 'output' })} aria-label={`See output of ${handleOf(session)}`} title="See the last reply">
          <span className="node-output-label">Output</span>
        </Button>
      )}

      <QueueBadge session={session} />

      <AgentPillView pill={pill} />

      {isRemote && (
        <span className={`badge ${REMOTE_BADGE[mstatus?.color ?? 'success']} node-remote`} title={machine ? `${machine.name}: ${mstatus?.label}` : undefined}>
          {session.tmuxName ? 'ssh · tmux' : 'ssh'}
        </span>
      )}

      <DisplayButton id={nodeId} />
      <Menu className="nodrag" label={`Actions for ${handleOf(session)}`} items={items} />
    </div>
  );
}
