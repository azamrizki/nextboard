// Chats panel on the board's right rail (mockup 1): every agent conversation of the project, from
// every part and the root, including those whose console was closed, grouped by day; and the
// lead's chats of the project thread and of each orchestrator card on this board. A chat resumes
// where it started (docs/DESIGN.md §3.5 Chats).

import { useEffect, useMemo, useState, useSyncExternalStore } from 'react';
import { agentById, useAgentCatalog } from '../agents/catalog';
import type { BoardController } from '../board/controller';
import { Button } from '../components/Button';
import { SegmentedControl, TextField } from '../components/form';
import { ConfirmDialog } from '../components/ConfirmDialog';
import type { MenuEntry } from '../components/Menu';
import { Modal } from '../components/Modal';
import { Icon } from '../components/Icon';
import { projectParts } from '../lib/parts';
import { isMultiPart, ROOT_VALUE } from '../lib/placement';
import type { Project } from '../lib/types';
import { useStoreSessions } from '../orch/hooks';
import { useLeadName } from '../orch/lead';
import { chatsApi } from './api';
import { ChatGroups, useChatDialogs } from './ChatRows';
import { filterChats, leadChatTitle, sortLeadChats, spoken } from './helpers';
import { leadChatError, resumeLeadChat, thinkingText, useLeadThinking } from './LeadChatSwitcher';
import { LeadChatRow } from './LeadChatRow';
import { chatsStore, useLeadChats, useProjectChats } from './store';
import type { ChatView, LeadChat } from './types';
import '../styles/chats.css';

interface Props {
  ctl: BoardController;
  project: Project;
  onClose: () => void;
  /** A closed chat: open the Resume dialog. */
  onResume: (c: ChatView) => void;
  /** Show a lead thread: the project's (command bar) or an orchestrator card's. */
  onOpenThread: (threadId: string) => void;
  /** A narrow screen: the panel covers the board, so it closes once a console is shown. */
  compact?: boolean;
  /** Where a chat continued elsewhere shows its new console; absent: the jump to it on its board. */
  onStarted?: (sessionId: string) => void;
}

type Tab = 'agents' | 'lead';

/** Hub search in the transcripts too, a moment after typing stops. */
function useHubSearch(projectId: string, q: string): ReadonlySet<string> | undefined {
  const [found, setFound] = useState<ReadonlySet<string> | undefined>(undefined);
  useEffect(() => {
    const t = q.trim();
    if (t.length < 2) {
      setFound(undefined);
      return;
    }
    const ac = new AbortController();
    const timer = window.setTimeout(() => {
      chatsApi
        .list(projectId, { q: t, limit: 200 }, ac.signal)
        .then((items) => setFound(new Set(items.map((c) => c.id))))
        .catch(() => undefined);
    }, 300);
    return () => {
      window.clearTimeout(timer);
      ac.abort();
    };
  }, [projectId, q]);
  return found;
}

/** The threads of the Lead tab: the project's, then each orchestrator card's on this board. */
function useLeadThreads(ctl: BoardController, project: Project): { id: string; card?: string }[] {
  const doc = useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);
  return useMemo(
    () => [
      { id: project.id },
      ...doc.nodes
        .filter((n) => n.kind === 'orchestrator')
        .map((n) => ({ id: n.id, card: typeof n.data.title === 'string' && n.data.title ? n.data.title : 'Orchestrator' })),
    ],
    [doc.nodes, project.id],
  );
}

export default function ChatsPanel({ ctl, project, onClose, onResume, onOpenThread, compact, onStarted }: Props) {
  const [tab, setTab] = useState<Tab>('agents');
  const [q, setQ] = useState('');
  const [agent, setAgent] = useState('');
  const [place, setPlace] = useState('');
  const sessions = useStoreSessions();
  const lead = useLeadName();
  const { items, status, error } = useProjectChats(project.id);
  const found = useHubSearch(project.id, q);
  const multi = isMultiPart(project);
  const parts = projectParts(project);
  const threads = useLeadThreads(ctl, project);
  const dialogs = useChatDialogs(project, ctl.workspaceId, onResume, compact ? onClose : undefined, onStarted);

  useEffect(() => {
    chatsStore.install();
    for (const t of threads) void chatsStore.loadLead(t.id);
  }, [threads]);
  const leadCount = useSyncExternalStore(chatsStore.subscribe, () => threads.reduce((n, t) => n + (chatsStore.get().lead[t.id]?.items.filter(spoken).length ?? 0), 0));

  useEffect(() => {
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('.modal-layer, .menu-list')) onClose();
    };
    document.addEventListener('keydown', esc);
    return () => document.removeEventListener('keydown', esc);
  }, [onClose]);

  const visible = items.filter((c) => !c.archived);
  // the agents' names and marks (the catalog loads once)
  const catalog = useAgentCatalog();
  const agents = useMemo(() => [...new Set(visible.map((c) => c.agent))].map((id) => ({ id, name: agentById(id)?.name ?? id })).sort((a, b) => a.name.localeCompare(b.name)), [visible, catalog.agents]); // eslint-disable-line react-hooks/exhaustive-deps
  const shown = filterChats(visible, { q, agent, place, found });
  const machines = new Set(parts.map((p) => p.machineId)).size;

  return (
    <aside className="side-panel chats-panel" aria-label="Chats">
      <div className="side-panel-head">
        <Icon name="message" />
        <h2>Chats</h2>
        <span className="text-3 fs-sm truncate">
          {project.name}
          {multi ? ` · ${parts.length} parts` : ''}
        </span>
        <span className="spacer" />
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close chats" onClick={onClose} />
      </div>
      <SegmentedControl
        label="Whose chats"
        block
        value={tab}
        onChange={setTab}
        options={[
          { value: 'agents', label: `Agents · ${visible.length}` },
          { value: 'lead', label: `Lead (${lead}) · ${leadCount}` },
        ]}
      />
      {tab === 'agents' ? (
        <>
          <div className="chats-filters">
            <div className="input-group has-lead grow">
              <span className="ig-lead">
                <Icon name="search" />
              </span>
              <input className="control control-sm" type="search" placeholder="Search titles and messages…" aria-label="Search chats" value={q} onChange={(e) => setQ(e.target.value)} />
            </div>
            <select className="control control-sm chats-agent" aria-label="Agent" value={agent} onChange={(e) => setAgent(e.target.value)}>
              <option value="">All agents</option>
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.name}
                </option>
              ))}
            </select>
          </div>
          {multi && (
            <div className="chat-place-chips" role="toolbar" aria-label="Place">
              {[{ v: '', l: 'All places', t: `Every part and the root${machines > 1 ? `, on ${machines} machines` : ''}` }, { v: ROOT_VALUE, l: 'Root', t: 'Started at the project root (every part)' }, ...parts.map((p) => ({ v: p.id, l: p.name, t: p.path }))].map((o) => (
                <button key={o.v || 'all'} type="button" className="chat-place-btn" aria-pressed={place === o.v} title={o.t} onClick={() => setPlace(o.v)}>
                  {o.l}
                </button>
              ))}
            </div>
          )}
          <div className="chats-scroll">
            {status === 'loading' && !items.length && (
              <div className="list-msg" role="status">
                <span className="spinner" aria-hidden="true" /> Loading chats…
              </div>
            )}
            {status === 'unsupported' && <div className="list-msg">This hub does not keep chats yet. Update NextBoard on the hub.</div>}
            {status === 'error' && (
              <div className="alert" role="alert">
                <Icon name="alertCircle" />
                <span>Could not load the chats: {error}</span>
              </div>
            )}
            {status === 'ready' && !visible.length && <div className="list-msg">No chats yet. Every conversation an agent has here shows up, and stays after its console closes.</div>}
            {status === 'ready' && visible.length > 0 && !shown.length && <div className="list-msg">No chat matches{q.trim() ? ` “${q.trim()}”` : ' these filters'}.</div>}
            {shown.length > 0 && <ChatGroups chats={shown} project={project} sessions={sessions} handlers={dialogs.handlers} />}
          </div>
          <p className="chats-foot">
            <Icon name="info" />
            <span>A chat resumes where it started: the same part and folder, agent and account — so its context and paths stay right.</span>
          </p>
        </>
      ) : (
        <div className="chats-scroll">
          {threads.map((t) => (
            <LeadThreadChats key={t.id} threadId={t.id} card={t.card} projectName={project.name} lead={lead} onOpen={() => (onOpenThread(t.id), compact && onClose())} />
          ))}
        </div>
      )}
      {dialogs.node}
    </aside>
  );
}

/** One thread's chats in the Lead tab: the current one marked, the others with Resume. */
function LeadThreadChats({ threadId, card, projectName, lead, onOpen }: { threadId: string; card?: string; projectName: string; lead: string; onOpen: () => void }) {
  const { items, active, status } = useLeadChats(threadId);
  const [busy, setBusy] = useState('');
  const [editing, setEditing] = useState<{ kind: 'rename' | 'delete'; chat: LeadChat } | null>(null);
  const thinking = useLeadThinking(threadId);
  if (status === 'unsupported') return threadId.startsWith('p_') ? <div className="list-msg">This hub keeps one chat per thread. Update NextBoard on the hub to keep several.</div> : null;
  if (status !== 'ready' && !items.length) return null;
  // a chat nothing was said in yet is no chat to go back to
  const sorted = sortLeadChats(items.filter(spoken), active);
  if (card && !sorted.length) return null;
  const menu = (c: LeadChat): MenuEntry[] => [
    { label: 'Rename…', icon: 'pencil', onSelect: () => setEditing({ kind: 'rename', chat: c }) },
    {
      label: 'Delete…',
      icon: 'trash',
      danger: true,
      disabled: c.id === active,
      title: c.id === active ? 'The chat in use: resume another one first' : undefined,
      onSelect: () => setEditing({ kind: 'delete', chat: c }),
    },
  ];
  const resume = async (c: LeadChat) => {
    setBusy(c.id);
    const ok = await resumeLeadChat(threadId, c.id, card ?? lead);
    setBusy('');
    if (ok) onOpen();
  };
  return (
    <section className="chat-group" aria-label={card ? `Orchestrator card ${card}` : `Chats with ${lead}`}>
      <h3 className="chat-day">{card ? `Card · ${card}` : `With ${lead} · ${projectName}`}</h3>
      {!sorted.length && <p className="fs-xs text-3 lead-chat-none">No chat with {lead} yet.</p>}
      <ul className="plain chat-list">
        {sorted.map((c) => (
          <LeadChatRow key={c.id} chat={c} current={c.id === active} card={card} busy={busy === c.id} blocked={thinking ? thinkingText(card ?? lead) : undefined} onResume={() => void resume(c)} onOpen={onOpen} menu={menu(c)} />
        ))}
      </ul>
      {editing?.kind === 'rename' && <RenameLeadChatDialog threadId={threadId} chat={editing.chat} lead={lead} onClose={() => setEditing(null)} />}
      {editing?.kind === 'delete' && (
        <ConfirmDialog
          title={`Delete “${leadChatTitle(editing.chat)}”?`}
          confirmLabel="Delete chat"
          danger
          onClose={() => setEditing(null)}
          onConfirm={async () => {
            try {
              await chatsApi.deleteLeadChat(threadId, editing.chat.id);
            } catch (err) {
              throw new Error(leadChatError(err, lead));
            }
            await chatsStore.loadLead(threadId, true);
          }}
        >
          <p>Its {editing.chat.messages} messages go. The other chats with {lead}, and the project memory, stay.</p>
        </ConfirmDialog>
      )}
    </section>
  );
}

/** A lead chat's title (the owner's first message until renamed). */
function RenameLeadChatDialog({ threadId, chat, lead, onClose }: { threadId: string; chat: LeadChat; lead: string; onClose: () => void }) {
  const [title, setTitle] = useState(leadChatTitle(chat));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const save = async () => {
    const v = title.trim();
    if (!v || v === chat.title) return onClose();
    setBusy(true);
    try {
      await chatsApi.renameLeadChat(threadId, chat.id, v);
      await chatsStore.loadLead(threadId, true);
      onClose();
    } catch (err) {
      setError(leadChatError(err, lead));
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
          <Button variant="primary" type="submit" form="rename-lead-chat" loading={busy} disabled={!title.trim()}>
            Rename
          </Button>
        </>
      }
    >
      <form
        id="rename-lead-chat"
        className="form"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <TextField label="Title" value={title} onChange={(e) => setTitle(e.target.value)} maxLength={80} error={error || undefined} autoFocus />
      </form>
    </Modal>
  );
}
