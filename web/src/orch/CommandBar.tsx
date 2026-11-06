// Command bar (docs/API-orchestration.md §3, §18, D1–D3): the bottom of every board.
// Chips for quick actions, the agent chip (the old "New terminal" dock), an order input
// with @mentions and target chips, and the project thread in a drawer above it.
// Ctrl/⌘K focuses the input, Esc gives the keyboard back to the board.
// Space-hold is reserved for push-to-talk (Phase 7) and deliberately not bound here.

import { useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import { agentById } from '../agents/catalog';
import { patchSettings, useShortcuts } from '../agents/settings';
import { AgentChip, type NT } from '../board/NewTerminal';
import type { BoardController } from '../board/controller';
import type { UiStore } from '../board/ui';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon } from '../components/Icon';
import { toast } from '../components/Toast';
import { navigate } from '../lib/router';
import type { Project } from '../lib/types';
import { useMediaQuery } from '../lib/useMediaQuery';
import { decide, isAgentSession, killSessions, pauseAll, resumeAll, sendCommand, UNSUPPORTED, orchError } from './actions';
import { useVoice } from '../voice/useVoice';
import { VoiceCaption, VoiceStatus } from '../voice/VoiceBar';
import { noteOrderSent } from '../voice/tts';
import { useStoreSessions } from './hooks';
import { localMentions, mentionedTargets, removeMention } from './mentions';
import { AttachChips, useAttachments } from './Attachments';
import { MentionInput } from './MentionInput';
import { orchApi } from './api';
import { is404, orchStore, useOrch, useThread } from './store';
import type { Mention } from './types';
import { replyDraftOf, replyText, useLeadName, type ReplyDraft } from './lead';
import { LeadAvatar, ReplyChip } from './OrderBits';
import { ConfirmationCard, ThreadList } from './ThreadView';

interface CommandBarProps {
  ctl: BoardController;
  ui: UiStore;
  project: Project;
  nt: NT;
  loopsRunning: number;
  onArrange: () => void;
  /** A phone's Lead tab: the thread fills the screen and stays open. */
  lead?: boolean;
}

interface Chip {
  id: string;
  label: string;
  onClick: () => void;
  title?: string;
  danger?: boolean;
  pressed?: boolean;
  badge?: number;
}

export function CommandBar({ ctl, ui, project, nt, loopsRunning, onArrange, lead = false }: CommandBarProps) {
  const compact = useMediaQuery('(max-width: 899px)');
  const sessions = useStoreSessions();
  const shortcuts = useShortcuts();
  const doc = useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);
  const threadId = project.id;
  const thread = useThread(threadId);
  const supported = useOrch((s) => s.supported);
  const planner = useOrch((s) => s.planner);
  const confirmations = useOrch((s) => s.confirmations);
  const [text, setText] = useState('');
  const [sending, setSending] = useState(false);
  const att = useAttachments(project.id);
  const [open, setOpenState] = useState(false);
  // the drawer opens by itself after an order until the user closes it once
  const userClosed = useRef(false);
  const setOpen = (v: boolean | ((o: boolean) => boolean)) =>
    setOpenState((o) => {
      const next = typeof v === 'function' ? v(o) : v;
      if (o && !next) userClosed.current = true;
      return next;
    });
  const [seen, setSeen] = useState(0);
  const [killArm, setKillArm] = useState(false);
  const [killConfirm, setKillConfirm] = useState(false);
  const [reply, setReply] = useState<ReplyDraft | null>(null);
  const leadName = useLeadName();
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const barRef = useRef<HTMLDivElement>(null);
  // How tall the bar really is (chips that wrap, a taller field, touch sizes): what sits above
  // it on the board reads --cmdbar-h instead of a fixed guess.
  useEffect(() => {
    const el = barRef.current;
    if (!el) return;
    const root = document.documentElement;
    const ro = new ResizeObserver(() => root.style.setProperty('--cmdbar-h', `${Math.ceil(el.getBoundingClientRect().height)}px`));
    ro.observe(el);
    return () => {
      ro.disconnect();
      root.style.removeProperty('--cmdbar-h');
    };
  }, []);
  const historyKey = `p.${project.id}`;

  useEffect(() => {
    void orchStore.loadThread(threadId);
    if (!orchStore.get().planner) void orchStore.loadPlanner();
  }, [threadId]);

  // unread = messages that arrived while the drawer was closed
  const count = thread.messages.length;
  useEffect(() => {
    if (open) setSeen(count);
  }, [open, count]);
  useEffect(() => {
    if (thread.status === 'ready' && seen === 0) setSeen(count);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [thread.status]);
  const unread = open ? 0 : Math.max(0, count - seen);

  // Ctrl/⌘K focuses the bar (not while a console has the keyboard: there it belongs to the program)
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey) || e.altKey || e.key.toLowerCase() !== 'k') return;
      const t = e.target as HTMLElement | null;
      if (t?.closest?.('.xterm') || document.querySelector('.modal-layer')) return;
      e.preventDefault();
      inputRef.current?.focus();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, []);

  // The bar controls every board of the project, not just the open one.
  const projectSessions = useMemo(() => Object.values(sessions).filter((s) => s.projectId === project.id), [sessions, project.id]);
  const boardIds = (project.workspaces ?? []).map((w) => w.id);
  const agentsRunning = projectSessions.filter((s) => isAgentSession(s) && s.status === 'running');
  const anyPaused = agentsRunning.some((s) => s.paused);
  const liveConsoles = projectSessions.filter((s) => s.status !== 'exited');
  const liveBoards = new Set(liveConsoles.map((s) => s.workspaceId)).size;

  const local = () => localMentions(ctl.nodes, ctl.sync.state.edges, sessions, (a) => agentById(a)?.name ?? a);
  // Other boards' groups are only known to the hub: target chips ask it once an @ appears.
  const hasAt = text.includes('@');
  const [hubMentions, setHubMentions] = useState<Mention[]>([]);
  useEffect(() => {
    if (!hasAt) return;
    let live = true;
    orchApi
      .mentions(project.id, { workspaceId: ctl.workspaceId, q: '' })
      .then((l) => {
        if (live) setHubMentions(l ?? []);
      })
      .catch(() => undefined);
    return () => {
      live = false;
    };
  }, [hasAt, project.id, ctl.workspaceId]);
  const targets = useMemo(() => mentionedTargets(text, [...local(), ...hubMentions]), [text, doc, sessions, hubMentions]); // eslint-disable-line react-hooks/exhaustive-deps

  const pending = useMemo(
    () =>
      Object.values(confirmations)
        .filter((c) => c.threadId === threadId && c.state === 'pending' && Date.parse(c.expiresAt) > Date.now())
        .sort((a, b) => b.expiresAt.localeCompare(a.expiresAt))[0],
    [confirmations, threadId],
  );

  const send = async (t: string, source: 'typed' | 'voice' = 'typed') => {
    setSending(true);
    try {
      noteOrderSent();
      await sendCommand(project.id, replyText(att.compose(t), reply), { workspaceId: ctl.workspaceId, selection: [...ctl.selection], source, replyTo: reply?.id });
      setText('');
      att.clear();
      setReply(null);
      if (!compact && !userClosed.current) setOpenState(true);
    } catch (err) {
      toast.error(is404(err) ? UNSUPPORTED : `The order was not sent: ${orchError(err)}`);
    } finally {
      setSending(false);
    }
  };

  // voice: push-to-talk sends; "yes" / "cancel" answer a pending confirmation
  const voice = useVoice({
    ctl,
    onOrder: async (t) => {
      const words = t.trim().toLowerCase().replace(/[.!?]+$/, '');
      if (pending && /^(yes|yeah|confirm|ok|okay|do it|go ahead)$/.test(words)) return void decide(pending, 'confirm');
      if (pending && /^(no|cancel|stop|never ?mind)$/.test(words)) return void decide(pending, 'cancel');
      await send(t, 'voice');
    },
    onBarText: (t) => {
      setText((v) => (v.trim() ? `${v.replace(/\s+$/, '')} ${t}` : t));
      // the input comes back with the next render
      requestAnimationFrame(() => inputRef.current?.focus());
    },
  });
  const micDown = useRef(0);
  const live = voice.mode !== 'idle';

  const togglePlanner = async () => {
    const off = planner?.reason !== 'disabled';
    try {
      await patchSettings({ commands: { planner: off ? 'off' : 'auto' } });
      await orchStore.loadPlanner();
      toast.info(off ? 'Planner off: orders use the built-in parser only.' : 'Planner on: sentences go to Claude Code on This PC when available.');
    } catch (err) {
      toast.error(`Could not change the planner: ${orchError(err)}`);
    }
  };

  const toBoard = () => {
    inputRef.current?.blur();
    document.querySelector<HTMLElement>('.board-canvas')?.focus({ preventScroll: true });
  };

  const chips: Chip[] = [
    { id: 'new', label: 'New console', title: `Start ${nt.agent?.name ?? 'a console'} next to the others (or press ${shortcuts.spawn.toUpperCase()} and click)`, onClick: () => void nt.create() },
    { id: 'spawn4', label: 'Spawn 4', title: 'Start four consoles at once', onClick: () => ui.set({ composer: { slots: 4, group: false } }) },
    { id: 'group', label: 'Group', title: 'Compose a team with a name and a role', onClick: () => ui.set({ composer: { slots: 6, group: true } }) },
    { id: 'layouts', label: 'Layouts', onClick: () => ui.set({ panel: ui.get().panel === 'layouts' ? null : 'layouts' }) },
    { id: 'arrange', label: 'Auto arrange', title: 'Tidy this board so that nothing overlaps. Ctrl+Z undoes it.', onClick: onArrange },
    { id: 'pause', label: 'Pause all', title: 'Stop every agent on every board until you resume', onClick: () => void pauseAll(boardIds) },
    { id: 'resume', label: 'Resume all', pressed: anyPaused, title: 'Resume paused agents on every board', onClick: () => void resumeAll(boardIds) },
    { id: 'memory', label: 'Memory', title: `Project memory (${shortcuts.memory.toUpperCase()})`, onClick: () => ui.set({ panel: ui.get().panel === 'memory' ? null : 'memory' }) },
  ];
  if (open) {
    chips.push(
      { id: 'loops', label: 'Loops', badge: loopsRunning, title: `Loops (${shortcuts.loops.toUpperCase()})`, onClick: () => ui.set({ panel: ui.get().panel === 'loops' ? null : 'loops' }) },
      { id: 'mission', label: 'Mission Map', title: 'Mission Map (X)', onClick: () => ui.set({ mission: true }) },
      {
        id: 'kill',
        label: killArm ? 'Click again to kill all' : 'Kill all',
        danger: true,
        title: 'Two clicks, then a confirmation',
        onClick: () => {
          if (killArm) {
            setKillArm(false);
            setKillConfirm(true);
          } else {
            setKillArm(true);
            window.setTimeout(() => setKillArm(false), 4000);
          }
        },
      },
    );
  }

  const empty = doc.nodes.length === 0;
  const placeholder =
    supported === false
      ? 'Orders need a newer hub'
      : empty
        ? 'Ask what the bar can do — try “help”'
        : reply?.handle
          ? `Reply to @${reply.handle}…`
          : compact
            ? `Talk to ${leadName}…`
            : `Talk to ${leadName}… @ to reach an agent · Ctrl+K`;

  const plannerLabel = !planner ? '' : planner.available ? 'Planner on' : planner.reason === 'disabled' ? 'Parser only' : planner.reason === 'unsupported' ? '' : 'Parser only';
  const plannerTitle = !planner
    ? ''
    : planner.available
      ? `Sentences are planned by Claude Code on This PC${planner.lastLatencyMs ? ` (last ${Math.round(planner.lastLatencyMs / 100) / 10}s)` : ''}`
      : ({
          claude_missing: 'Claude Code is not installed on This PC, so orders use the built-in parser.',
          not_logged_in: 'Claude Code on This PC is not logged in, so orders use the built-in parser.',
          disabled: 'The planner is turned off in settings.',
          cooldown: 'The planner failed twice; it is paused for a minute.',
        }[planner.reason ?? ''] ?? 'The planner is not available.');

  return (
    <div className={`cmdbar-wrap ${open || lead ? 'has-thread' : ''} ${lead ? 'is-lead' : ''}`}>
      {(open || lead) && (
        <section className="thread-drawer" aria-label="Thread">
          <header className="thread-head">
            <LeadAvatar name={leadName} />
            <h2>{leadName}</h2>
            <span className="text-3 fs-xs truncate">{project.name}</span>
            {plannerLabel && (
              <button
                type="button"
                className={`badge ${planner?.available ? 'badge-success' : ''} thread-planner`}
                title={`${plannerTitle} Click to turn the planner ${planner?.reason === 'disabled' ? 'on' : 'off'}.`}
                aria-label={`${plannerLabel}. Turn the planner ${planner?.reason === 'disabled' ? 'on' : 'off'}`}
                onClick={() => void togglePlanner()}
              >
                <Icon name={planner?.available ? 'sparkle' : 'keyboard'} />
                {plannerLabel}
              </button>
            )}
            <span className="spacer" />
            <Button
              size="sm"
              variant="ghost"
              icon="broom"
              disabled={!thread.messages.length}
              onClick={() =>
                void orchApi
                  .clearThread(threadId)
                  .then(() => orchStore.apply({ type: 'threadCleared', threadId }))
                  .catch((err) => toast.error(`Could not clear the thread: ${orchError(err)}`))
              }
            >
              Clear
            </Button>
            <Button size="sm" variant="ghost" iconOnly icon="sliders" aria-label="Lead agent settings" title="Name and personality" onClick={() => navigate('/settings/lead')} />
            {!lead && <Button size="sm" variant="ghost" iconOnly icon="chevronDown" aria-label="Close the thread" onClick={() => setOpen(false)} />}
          </header>
          <ThreadList
            threadId={threadId}
            lead={leadName}
            actions={{
              reply: (m, toLead) => {
                setReply(replyDraftOf(m, leadName, toLead));
                inputRef.current?.focus();
              },
              reuse: (t) => {
                setText(att.take(t));
                inputRef.current?.focus();
              },
            }}
            empty="Orders, plans and reports from your agents appear here. Try “@Arkan run the tests” or “spawn two reviewers”." />
        </section>
      )}
      {!open && !lead && pending && (
        <div className="confirm-strip" role="alert">
          <ConfirmationCard c={pending} compact />
        </div>
      )}
      <div className={`cmdbar ${live ? 'is-live' : ''}`} role="region" aria-label="Command bar" ref={barRef}>
        <div
          className="cmd-chips"
          role="toolbar"
          aria-label="Quick actions"
          onKeyDown={(e) => {
            if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft' && e.key !== 'Home' && e.key !== 'End') return;
            const items = Array.from(e.currentTarget.querySelectorAll<HTMLButtonElement>('.cmd-chip'));
            const i = items.indexOf(document.activeElement as HTMLButtonElement);
            if (i < 0) return;
            e.preventDefault();
            const n = e.key === 'Home' ? 0 : e.key === 'End' ? items.length - 1 : (i + (e.key === 'ArrowRight' ? 1 : items.length - 1)) % items.length;
            items[n]?.focus();
          }}
        >
          {chips.map((c) => (
            <button
              key={c.id}
              type="button"
              className={`cmd-chip ${c.danger ? 'is-danger' : ''} ${c.id === 'kill' && killArm ? 'is-armed' : ''}`}
              title={c.title ?? c.label}
              aria-pressed={c.pressed}
              onClick={c.onClick}
            >
              <span>{c.label}</span>
              {!!c.badge && <span className="chip-count tabular">{c.badge}</span>}
            </button>
          ))}
        </div>
        <div className="cmd-panel">
          <div className={`cmd-row ${live ? 'is-voice' : ''}`}>
            {live && <VoiceStatus label={voice.mode === 'talk' ? 'listening' : 'dictating'} text={voice.text} />}
            <div className="cmd-input">
              {(targets.length > 0 || reply || att.items.length > 0) && (
                <div className="cmd-top">
                  {targets.length > 0 && (
                    <div className="target-chips" aria-label="Targets">
                      {targets.map((t) => (
                        <span key={t.token} className={`tchip ${t.mention ? `kind-${t.mention.kind}` : 'is-unknown'}`} title={t.mention ? `${t.mention.kind}: ${t.mention.label}` : 'Unknown name: the hub will ask what you meant'}>
                          <Icon name={!t.mention ? 'alertCircle' : t.mention.kind === 'group' ? 'group' : t.mention.kind === 'everyone' ? 'users' : t.mention.kind === 'agent' ? 'bot' : t.mention.kind === 'role' ? 'flag' : 'at'} />
                          {t.mention?.kind === 'console' ? `@${t.mention.handle ?? t.mention.label}` : t.mention ? t.mention.label : `@${t.token}?`}
                          <button type="button" className="tchip-x" aria-label={`Remove @${t.token}`} onClick={() => setText((v) => removeMention(v, t.token))}>
                            <Icon name="close" />
                          </button>
                        </span>
                      ))}
                    </div>
                  )}
                  {reply && <ReplyChip reply={reply} onClear={() => setReply(null)} />}
                  <AttachChips att={att} inputRef={inputRef} />
                </div>
              )}
              <MentionInput
                projectId={project.id}
                workspaceId={ctl.workspaceId}
                value={text}
                onChange={setText}
                onSubmit={(t) => void send(t)}
                local={local}
                placeholder={placeholder}
                label="Order for your agents"
                historyKey={historyKey}
                disabled={sending}
                inputRef={inputRef}
                onEscape={() => (reply ? setReply(null) : toBoard())}
                attachments={att}
              />
            </div>
            <button
              type="button"
              className={`thread-toggle ${open ? 'is-open' : ''}`}
              aria-expanded={open}
              aria-label={open ? 'Close the thread' : `Open the thread${unread ? ` (${unread} new)` : ''}`}
              title={open ? 'Close the thread' : 'Open the thread'}
              onClick={() => setOpen((o) => !o)}
            >
              <Icon name={open ? 'chevronDown' : 'chevronUp'} />
              {unread > 0 && <span className="toggle-dot tabular">{unread > 9 ? '9+' : unread}</span>}
            </button>
            <AgentChip nt={nt} anchor={barRef} />
            {live && <Button size="sm" variant="ghost" iconOnly icon="close" className="voice-cancel" aria-label="Cancel" title="Cancel (Esc)" onClick={voice.cancel} />}
            {voice.active && (
              <button
                type="button"
                // the same button stays through the switch: a held press must still see its release
                className={`btn btn-ghost btn-sm btn-icon cmd-mic ${live ? 'is-live' : ''}`}
                aria-label={live ? 'Stop and send' : 'Talk: hold, or tap to start and tap again to send'}
                aria-pressed={live}
                title={live ? 'Stop and send' : voice.holdSpace ? 'Talk (hold Space on the board)' : 'Talk'}
                onPointerDown={(e) => {
                  e.preventDefault();
                  if (voice.mode === 'talk') return;
                  e.currentTarget.setPointerCapture(e.pointerId);
                  micDown.current = Date.now();
                  voice.startTalk();
                }}
                onPointerUp={() => {
                  if (!live) return;
                  // held: release sends; a quick tap keeps listening until the next tap
                  if (micDown.current && Date.now() - micDown.current > 350) voice.stop();
                  else if (!micDown.current) voice.stop();
                  micDown.current = 0;
                }}
                onKeyDown={(e) => {
                  if (e.key !== 'Enter' && e.key !== ' ') return;
                  e.preventDefault();
                  if (live) voice.stop();
                  else {
                    micDown.current = 0;
                    voice.startTalk();
                  }
                }}
              >
                <Icon name={live ? 'stop' : 'mic'} />
              </button>
            )}
            {voice.supported && !voice.active && <Button size="sm" variant="ghost" iconOnly icon="mic" className="cmd-mic is-off" aria-label="Turn on voice in Settings" title="Voice is off. Turn it on in Settings" onClick={() => navigate('/settings/voice')} />}
            <Button variant="primary" size="sm" iconOnly icon="send" aria-label="Send order" className="cmd-send" loading={sending} disabled={!att.canSend(text) || sending} onClick={() => void send(text.trim())} />
          </div>
          {live && (
            <VoiceCaption
              text={voice.text}
              hint={voice.mode === 'talk' ? 'Speak your order…' : `Speak… typing into ${voice.target || 'the command bar'}`}
            />
          )}
        </div>
      </div>
      {killConfirm && (
        <ConfirmDialog
          title={liveBoards > 1 ? `Kill every console on ${liveBoards} boards?` : 'Kill every console in this project?'}
          confirmLabel={`Kill ${liveConsoles.length === 1 ? '1 console' : `${liveConsoles.length} consoles`}`}
          danger
          onClose={() => setKillConfirm(false)}
          onConfirm={() => killSessions(projectSessions)}
        >
          <p>All running processes on every board of this project stop. The tiles stay, marked DONE; restart them from their menus.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}
