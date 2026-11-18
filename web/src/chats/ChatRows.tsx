// A chat as a row (mockups 1 and 4): the agent's mark, the title, the place chip, the status and
// size, and what can be done with it now: Show a live one, Wake an asleep one, Resume a closed
// one, read the transcript of an orphan or continue it elsewhere. Rename and the rest are in the
// row's menu (… , right-click, long-press). Shared by the Chats panel, the New console dialog's
// "Resume a chat" tab and the editor panes' "Resume a chat…".

import { useEffect, useState, type MouseEvent as ReactMouseEvent } from 'react';
import { api, errorMessage } from '../api';
import { AgentBadge } from '../agents/AgentBadge';
import { useAgent } from '../agents/catalog';
import { jumpToSession } from '../agents/jump';
import { Button } from '../components/Button';
import { TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { ContextMenu, Menu, type MenuEntry } from '../components/Menu';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { formatDateTime, timeAgo } from '../lib/format';
import type { Project, Session } from '../lib/types';
import { orchStore, useOrch } from '../orch/store';
import { PlacePicker } from '../board/PlacePicker';
import { isMultiPart, placeFromValue, ROOT_VALUE } from '../lib/placement';
import { isRoot, projectParts } from '../lib/parts';
import { store, useMachineNameOf } from '../store';
import { chatsApi } from './api';
import { chatAction, groupByDay, msgsLabel, placeChip, sessionOfResult, statusText } from './helpers';
import { chatsStore } from './store';
import type { ChatTranscript, ChatView } from './types';
import '../styles/chats.css';

const DOT: Record<string, string> = { live: 'success', asleep: 'info', closed: 'grey', orphan: 'warning' };

/** The machine names a chip shows, as named now. */
function useMachineLabel(): (id: string) => string {
  const nameOf = useMachineNameOf();
  return (id: string) => (id === 'local' || !id ? 'This PC' : nameOf({ machineId: id, machineName: '' }) || id);
}

export interface ChatRowHandlers {
  /** Closed: open the Resume dialog. */
  resume: (c: ChatView) => void;
  transcript: (c: ChatView) => void;
  continueElsewhere: (c: ChatView) => void;
  rename: (c: ChatView) => void;
  /** After Show / Wake took the user to the console (a panel closes). */
  shown?: () => void;
}

/** Show a live chat's console; wake an asleep one first. */
export async function showChat(c: ChatView, wake: boolean): Promise<boolean> {
  if (!c.sessionId) return false;
  if (wake) {
    try {
      store.upsertSession(await api.wakeSession(c.sessionId));
    } catch (err) {
      toast.error(`Could not wake @${c.handle}: ${errorMessage(err)}`);
      return false;
    }
  }
  if (!jumpToSession(c.sessionId)) {
    toast.error(`@${c.handle} is not on a board that can be opened.`);
    return false;
  }
  return true;
}

function rowMenu(c: ChatView, h: ChatRowHandlers): MenuEntry[] {
  const act = chatAction(c);
  return [
    ...(act === 'show' ? [{ label: 'Show the console', icon: 'target', onSelect: () => void showChat(c, false).then((ok) => ok && h.shown?.()) } as MenuEntry] : []),
    ...(act === 'wake' ? [{ label: 'Wake', icon: 'play', onSelect: () => void showChat(c, true).then((ok) => ok && h.shown?.()) } as MenuEntry] : []),
    ...(act === 'resume' ? [{ label: 'Resume…', icon: 'resume', onSelect: () => h.resume(c) } as MenuEntry] : []),
    { label: 'Rename…', icon: 'pencil', onSelect: () => h.rename(c) },
    { label: 'Transcript', icon: 'fileText', disabled: !c.agentSessionId && !c.transcriptPath, onSelect: () => h.transcript(c) },
    { label: 'Continue elsewhere…', icon: 'handoff', title: 'Summarise it and start a fresh console in another place', onSelect: () => h.continueElsewhere(c) },
    'sep',
    {
      label: 'Hide from the list',
      icon: 'archive',
      onSelect: () =>
        void chatsApi
          .update(c.id, { archived: true })
          .then((v) => chatsStore.upsert(v ?? { ...c, archived: true }))
          .catch((err) => toast.error(`Could not hide the chat: ${errorMessage(err)}`)),
    },
  ];
}

export function ChatRow({ chat: c, project, sessions, handlers, compact, showPlace = true }: { chat: ChatView; project: Project | undefined; sessions: Record<string, Session>; handlers: ChatRowHandlers; compact?: boolean; showPlace?: boolean }) {
  const machine = useMachineLabel();
  const mission = useOrch((s) => (c.missionId ? s.missions[c.missionId] : undefined));
  useEffect(() => {
    if (c.missionId && !mission) void orchStore.loadMission(c.missionId);
  }, [c.missionId, mission]);
  const [menuAt, setMenuAt] = useState<{ x: number; y: number } | null>(null);
  const [busy, setBusy] = useState(false);
  const chip = placeChip(c, project, machine);
  const act = chatAction(c);
  const agent = useAgent(c.agent);
  const title = c.title || 'Untitled chat';
  const status = statusText(c, sessions);
  const go = (wake: boolean) => {
    setBusy(true);
    void showChat(c, wake)
      .then((ok) => ok && handlers.shown?.())
      .finally(() => setBusy(false));
  };
  const onContext = (e: ReactMouseEvent) => {
    e.preventDefault();
    e.stopPropagation();
    setMenuAt({ x: e.clientX, y: e.clientY });
  };
  return (
    <li className={`chat-row ${act === 'orphan' ? 'is-orphan' : ''}`} data-chat={c.id} data-longpress onContextMenu={onContext}>
      <AgentBadge agent={agent} />
      <div className="chat-main">
        <span className="chat-title truncate" title={title}>
          {title}
        </span>
        <span className="chat-meta">
          {(showPlace || chip.kind === 'orphan') && (
            <span className={`chat-chip is-${chip.kind}`} title={chip.title}>
              {chip.kind === 'orphan' && <Icon name="alert" />}
              <span className="truncate">{chip.label}</span>
            </span>
          )}
          {mission && (
            <span className="chat-chip is-mission truncate" title={`Mission: ${mission.goal}`}>
              mission · {mission.goal}
            </span>
          )}
          {act !== 'orphan' && (
            <span className="chat-status">
              <span className={`dot dot-${DOT[c.status] ?? 'grey'}`} aria-hidden="true" />
              {status}
            </span>
          )}
          <span className="chat-size tabular" title={`Started ${formatDateTime(c.startedAt)} · last message ${formatDateTime(c.lastAt)}`}>
            {msgsLabel(c.messages)} · {timeAgo(c.lastAt || c.startedAt)}
          </span>
        </span>
      </div>
      <div className="chat-actions">
        {act === 'show' && (
          <Button size="sm" loading={busy} onClick={() => go(false)} aria-label={`Show @${c.handle}: ${title}`}>
            Show
          </Button>
        )}
        {act === 'wake' && (
          <Button size="sm" loading={busy} onClick={() => go(true)} aria-label={`Wake @${c.handle}: ${title}`}>
            Wake
          </Button>
        )}
        {act === 'resume' && (
          <Button size="sm" className="chat-resume" onClick={() => handlers.resume(c)} aria-label={`Resume: ${title}`}>
            Resume
          </Button>
        )}
        {act === 'orphan' && (
          <>
            {!compact && (
              <Button size="sm" variant="ghost" onClick={() => handlers.transcript(c)}>
                Transcript
              </Button>
            )}
            <Button size="sm" onClick={() => handlers.continueElsewhere(c)} aria-label={`Continue elsewhere: ${title}`}>
              Continue elsewhere…
            </Button>
          </>
        )}
        <Menu className="chat-row-menu" label={`More for ${title}`} items={() => rowMenu(c, handlers)} />
      </div>
      {menuAt && <ContextMenu x={menuAt.x} y={menuAt.y} label={`Actions for ${title}`} items={rowMenu(c, handlers)} onClose={() => setMenuAt(null)} />}
    </li>
  );
}

/** Chats grouped by day (Today, Yesterday, Earlier this week, Earlier), newest first. */
export function ChatGroups({ chats, project, sessions, handlers, compact, showPlace, label = 'Chats' }: { chats: ChatView[]; project: Project | undefined; sessions: Record<string, Session>; handlers: ChatRowHandlers; compact?: boolean; showPlace?: boolean; label?: string }) {
  const groups = groupByDay(chats, (c) => c.lastAt || c.startedAt);
  return (
    <div className="chat-groups" aria-label={label}>
      {groups.map((g) => (
        <section key={g.title} className="chat-group" aria-label={g.title}>
          <h3 className="chat-day">{g.title}</h3>
          <ul className="plain chat-list">
            {g.items.map((c) => (
              <ChatRow key={c.id} chat={c} project={project} sessions={sessions} handlers={handlers} compact={compact} showPlace={showPlace} />
            ))}
          </ul>
        </section>
      ))}
    </div>
  );
}

// ---- the dialogs a row opens ----------------------------------------------------------

/** The chat's conversation as the CLI kept it (read only). */
export function TranscriptDialog({ chat, onClose }: { chat: ChatView; onClose: () => void }) {
  const [t, setT] = useState<ChatTranscript | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    let live = true;
    chatsApi
      .transcript(chat.id)
      .then((r) => live && setT(r))
      .catch((err) => live && setError(errorMessage(err)));
    return () => {
      live = false;
    };
  }, [chat.id]);
  const agent = useAgent(chat.agent);
  const msgs = (t?.messages ?? []).filter((m) => !m.sidechain && m.text.trim());
  return (
    <Modal title={`Transcript · ${chat.title || 'Untitled chat'}`} icon="fileText" size="lg" onClose={onClose} footer={<Button onClick={onClose}>Close</Button>}>
      <div className="stack">
        <p className="picker-meta truncate" title={chat.remoteCwd || chat.cwd}>
          {agent?.name ?? chat.agent} in <span className="mono">{chat.remoteCwd || chat.cwd}</span>
        </p>
        {error && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error}</span>
          </div>
        )}
        {!t && !error && (
          <div className="list-msg" role="status">
            <span className="spinner" aria-hidden="true" /> Reading the transcript…
          </div>
        )}
        {t && msgs.length === 0 && <div className="list-msg">Nothing to show: the transcript holds no messages.</div>}
        {msgs.length > 0 && (
          <ol className="plain chat-transcript">
            {t?.truncated && <li className="fs-xs text-3">Only the last {msgs.length} messages are shown.</li>}
            {msgs.map((m) => (
              <li key={m.id} className={`chat-msg is-${m.role}`}>
                <span className="chat-msg-who">
                  {m.role === 'user' ? 'You' : m.role === 'tool' ? (m.tool ?? 'Tool') : (agent?.name ?? 'Agent')}
                  <time className="text-3 tabular" dateTime={m.at}>
                    {formatDateTime(m.at)}
                  </time>
                </span>
                <span className="chat-msg-text">{m.text}</span>
              </li>
            ))}
          </ol>
        )}
      </div>
    </Modal>
  );
}

export function RenameChatDialog({ chat, onClose }: { chat: ChatView; onClose: () => void }) {
  const [title, setTitle] = useState(chat.title);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const save = async () => {
    const v = title.trim();
    if (!v || v === chat.title) return onClose();
    setBusy(true);
    try {
      chatsStore.upsert((await chatsApi.update(chat.id, { title: v })) ?? { ...chat, title: v });
      onClose();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      title="Rename chat"
      icon="pencil"
      size="sm"
      onClose={onClose}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" form="rename-chat" loading={busy} disabled={!title.trim()}>
            Rename
          </Button>
        </>
      }
    >
      <form
        id="rename-chat"
        className="form"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <TextField label="Title" value={title} onChange={(e) => setTitle(e.target.value)} maxLength={120} error={error || undefined} autoFocus />
      </form>
    </Modal>
  );
}

const jumpToStarted = (sessionId: string) => void jumpToSession(sessionId);

/**
 * Continue elsewhere: the hub summarises the chat (like a handoff) and starts a fresh console at
 * the place picked, with the summary as its first order. For a chat whose place is gone, and any
 * other that should go on somewhere else.
 */
export function ContinueDialog({ chat, project, workspaceId, onClose, onStarted }: { chat: ChatView; project: Project | undefined; workspaceId: string; onClose: () => void; onStarted?: (sessionId: string) => void }) {
  const parts = project ? projectParts(project) : [];
  const first = chat.root ? ROOT_VALUE : parts.some((p) => p.id === chat.partId) ? chat.partId! : project && isMultiPart(project) ? ROOT_VALUE : (parts[0]?.id ?? ROOT_VALUE);
  const [place, setPlace] = useState(first);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const go = async () => {
    setBusy(true);
    setError('');
    try {
      const p = placeFromValue(place);
      const r = await chatsApi.continueElsewhere(chat.id, { workspaceId, place: isRoot(p) ? { root: true } : { partId: p.partId }, agent: chat.agent });
      // the store has the console before anything looks for it there (a jump to it)
      const s = sessionOfResult(r);
      if (s) store.upsertSession(s);
      toast.success('The chat is being summarised for a new console there.');
      if (s) onStarted?.(s.id);
      onClose();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };
  const agent = useAgent(chat.agent);
  return (
    <Modal
      title="Continue elsewhere"
      icon="handoff"
      size="sm"
      onClose={onClose}
      dismissable={!busy}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" icon="handoff" loading={busy} onClick={() => void go()}>
            Continue there
          </Button>
        </>
      }
    >
      <div className="stack">
        <p className="fs-sm">
          “{chat.title || 'Untitled chat'}” cannot resume where it started{chat.status === 'orphan' ? ': its place is gone or its folder moved' : ' from another place'}. NextBoard summarises it and starts a new {agent?.name ?? chat.agent} console at the place you pick, with the summary as its first order.
        </p>
        {project && isMultiPart(project) ? (
          <PlacePicker project={project} value={place} onChange={setPlace} label="Continue in" />
        ) : (
          <p className="fs-xs text-3">In the project folder.</p>
        )}
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

/**
 * The Transcript, Rename and Continue elsewhere dialogs, for any list of rows. started: where the
 * console a chat continued in shows (the Code view opens it as a tab); by default the jump to it.
 */
export function useChatDialogs(project: Project | undefined, workspaceId: string, resume: (c: ChatView) => void, shown?: () => void, started: (sessionId: string) => void = jumpToStarted) {
  const [open, setOpen] = useState<{ kind: 'transcript' | 'rename' | 'continue'; chat: ChatView } | null>(null);
  const handlers: ChatRowHandlers = {
    resume,
    transcript: (chat) => setOpen({ kind: 'transcript', chat }),
    rename: (chat) => setOpen({ kind: 'rename', chat }),
    continueElsewhere: (chat) => setOpen({ kind: 'continue', chat }),
    shown,
  };
  const close = () => setOpen(null);
  const node =
    open?.kind === 'transcript' ? (
      <TranscriptDialog chat={open.chat} onClose={close} />
    ) : open?.kind === 'rename' ? (
      <RenameChatDialog chat={open.chat} onClose={close} />
    ) : open?.kind === 'continue' ? (
      <ContinueDialog chat={open.chat} project={project} workspaceId={workspaceId} onClose={close} onStarted={started} />
    ) : null;
  return { handlers, node };
}
