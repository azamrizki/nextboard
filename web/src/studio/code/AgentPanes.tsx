// Agents inside the editor (docs/API-studio.md §7.8): a console or the orchestrator as a pane,
// in the right side bar or as an editor tab. A console here is a normal session without a tile
// on the board, so its status, its needs-you and the link to the editor work as for any other.

import { createContext, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import { api, errorMessage } from '../../api';
import { restartConsole } from '../../agents/actions';
import { agentById, byPriority, useMachineAgents } from '../../agents/catalog';
import { agentPill } from '../../agents/state';
import type { BoardController } from '../../board/controller';
import { directedTerminals } from '../../board/groups';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { Menu, type MenuEntry } from '../../components/Menu';
import { toast } from '../../components/Toast';
import { prefs } from '../../lib/format';
import type { Agent, Project, Session } from '../../lib/types';
import { orchError, sendCommand } from '../../orch/actions';
import { useStoreSessions } from '../../orch/hooks';
import { localMentions } from '../../orch/mentions';
import { AttachChips, useAttachments } from '../../orch/Attachments';
import { MentionInput } from '../../orch/MentionInput';
import { replyDraftOf, replyText, useLeadName, useTalkFirst, type ReplyDraft } from '../../orch/lead';
import { AgentChip, GENERAL_IDEAS, LeadAvatar, OrderWelcome, ReplyChip } from '../../orch/OrderBits';
import { orchStore, useOrch } from '../../orch/store';
import { ConfirmationCard, ThreadList } from '../../orch/ThreadView';
import { store, useSession, useSessions, useStoreReady } from '../../store';
import { TerminalView } from './BottomPanel';
import { orderText, type AgentContext } from './SidePanels';
import { useWs } from './useWs';
import { nameOf, ORCHESTRATOR_PANE, type Workspace } from './workspace';

/** What a pane needs from the board around the card. */
export const PaneEnv = createContext<{ project: Project; ctl: BoardController } | null>(null);

type Where = 'side' | 'tab';

/** Start an agent for this editor: a console with no tile on the board. */
export async function startAgentPane(ws: Workspace, workspaceId: string, agent: Agent, where: Where) {
  try {
    const s = await api.createSession({ workspaceId, preset: agent.id, agent: agent.id, panel: true, cols: 100, rows: 30 });
    store.upsertSession(s);
    ws.placePane(s.id, where);
  } catch (err) {
    toast.error(`Could not start ${agent.name}: ${errorMessage(err)}`);
  }
}

export interface PaneChoices {
  /** What can be started: the catalog is long, and most of it is not installed. */
  agents: Agent[];
  /** Agents of this board's editor with no pane here, as when started from another browser. */
  unplaced: Session[];
}

export function usePaneChoices(ws: Workspace, workspaceId: string): PaneChoices {
  const { agents } = useMachineAgents(ws.mid);
  const sessions = useSessions();
  const sorted = useMemo(() => agents.filter((a) => a.kind === 'agent' && a.available !== false).sort(byPriority), [agents]);
  const placed = ws.paneConsoles;
  const unplaced = sessions.filter((s) => s.panel && s.agentKind === 'agent' && s.workspaceId === workspaceId && s.status !== 'exited' && !placed.includes(s.id));
  return { agents: sorted, unplaced };
}

export function agentMenu(ws: Workspace, workspaceId: string, { agents, unplaced }: PaneChoices, where: Where): MenuEntry[] {
  return [
    ...unplaced.map<MenuEntry>((s) => ({ label: `Show ${s.handle ? `@${s.handle}` : s.name}`, icon: 'terminal', title: 'Already running in this editor', onSelect: () => ws.placePane(s.id, where) })),
    ...(unplaced.length ? (['sep'] as MenuEntry[]) : []),
    ...agents.map<MenuEntry>((a) => ({
      label: a.name,
      icon: 'bot',
      title: `Start ${a.name} in the project folder`,
      onSelect: () => void startAgentPane(ws, workspaceId, a, where),
    })),
    ...(agents.length ? (['sep'] as MenuEntry[]) : []),
    { label: 'Orchestrator', icon: 'orchestrator', title: 'Give orders to the agents of this board', onSelect: () => ws.placePane(ORCHESTRATOR_PANE, where) },
  ];
}

function paneLabel(id: string, s: ReturnType<typeof useSession>) {
  if (id === ORCHESTRATOR_PANE) return 'Lead agent';
  if (!s) return 'Console';
  return s.handle ? `@${s.handle}` : s.name;
}

/** Icon and name of a pane, for the tabs of the side bar and of the editor. */
export function PaneTitle({ ws, id }: { ws: Workspace; id: string }) {
  const s = useSession(id);
  const lead = useLeadName();
  const ready = useStoreReady();
  // the hub no longer has it (it was closed elsewhere)
  useEffect(() => {
    if (id !== ORCHESTRATOR_PANE && ready && !s) ws.forgetPane(id);
  }, [ready, s, ws, id]);
  if (id === ORCHESTRATOR_PANE) {
    return (
      <>
        <Icon name="orchestrator" className="wb-icon" />
        <span className="wb-tab-name truncate">{lead}</span>
      </>
    );
  }
  const pill = s && agentPill(s);
  const agentName = s ? (agentById(s.agent || s.preset)?.name ?? s.name) : '';
  return (
    <>
      <span className={`dot dot-${pill?.dot ?? 'grey'}`} aria-hidden="true" />
      <span className="wb-tab-name truncate" title={pill ? `${agentName} · ${pill.label}` : agentName}>
        {paneLabel(id, s)}
      </span>
      <span className="wb-tab-dir truncate">{agentName}</span>
    </>
  );
}

function ConsolePane({ ws, id }: { ws: Workspace; id: string }) {
  useWs(ws);
  const s = useSession(id);
  const host = useRef<HTMLDivElement>(null);

  // xterm appears a moment after the view does, so the keyboard waits for it
  const focus = ws.paneFocus;
  useEffect(() => {
    if (focus.id !== id) return;
    let tries = 0;
    const t = window.setInterval(() => {
      const box = host.current?.querySelector<HTMLTextAreaElement>('.xterm-helper-textarea');
      box?.focus();
      if (box || ++tries > 30) window.clearInterval(t);
    }, 100);
    return () => window.clearInterval(t);
  }, [focus.tick, focus.id, id]);

  return (
    <div className="wb-pane-console" ref={host}>
      {s?.status === 'exited' && (
        <div className="wb-pane-ended">
          <span className="truncate">This console has ended.</span>
          <span className="spacer" />
          <Button size="sm" icon="restart" onClick={() => void restartConsole(s, s.agentSessionId ? { mode: 'resume' } : {})}>
            Restart
          </Button>
        </div>
      )}
      <div className="wb-pane-term">
        <TerminalView key={id} sessionId={id} />
      </div>
    </div>
  );
}

/** The text selected in an editor now, or the file it shows. */
function currentContext(ws: Workspace): AgentContext | null {
  const ed = ws.activeEditor() ?? [...ws.editors.values()][0];
  const model = ed?.getModel();
  if (!ed || !model || model.uri.scheme !== 'file') return null;
  const rel = ws.rel(model.uri.path);
  const name = nameOf(model.uri.path);
  const language = model.getLanguageId();
  const sel = ed.getSelection();
  if (!sel || sel.isEmpty()) return { label: name, rel, language, code: '', startLine: 0, endLine: 0 };
  const end = sel.endColumn === 1 && sel.endLineNumber > sel.startLineNumber ? sel.endLineNumber - 1 : sel.endLineNumber;
  const lines = sel.startLineNumber === end ? `line ${end}` : `lines ${sel.startLineNumber}-${end}`;
  return { label: `${name}, ${lines}`, rel, language, code: model.getValueInRange(sel), startLine: sel.startLineNumber, endLine: end };
}

const TEAM_CHIPS = 8;

/** Ideas for an empty thread, about what is open when something is. */
function orderIdeas(context: AgentContext | null): string[] {
  return context ? [`Review ${context.label}`, `Write tests for ${context.label}`, `Explain ${context.label}`] : GENERAL_IDEAS;
}

function OrchestratorPane({ ws }: { ws: Workspace }) {
  useWs(ws);
  useSyncExternalStore(ws.subscribeCursor, ws.getCursorVersion);
  const env = useContext(PaneEnv);
  const project = env!.project;
  const ctl = env!.ctl;
  const sessions = useStoreSessions();
  const doc = useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);
  const confirmations = useOrch((s) => s.confirmations);
  // the pane closes with the board, and this choice should not
  const threadPref = `code.orchThread.${ctl.workspaceId}`;
  const [cardId, setCardId] = useState(() => prefs.get(threadPref) ?? '');
  const [text, setText] = useState('');
  const [sending, setSending] = useState(false);
  const att = useAttachments(project.id);
  const [dropped, setDropped] = useState('');
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const [reply, setReply] = useState<ReplyDraft | null>(null);

  const cards = doc.nodes.filter((n) => n.kind === 'orchestrator');
  const card = cards.some((n) => n.id === cardId) ? cardId : '';
  const cardTitle = doc.nodes.find((n) => n.id === card)?.data.title;
  const name = useLeadName(typeof cardTitle === 'string' ? cardTitle : undefined);
  const talkFirst = useTalkFirst();
  const threadId = card || project.id;
  useEffect(() => {
    void orchStore.loadThread(threadId);
  }, [threadId]);

  const pending = useMemo(
    () =>
      Object.values(confirmations)
        .filter((c) => c.threadId === threadId && c.state === 'pending' && Date.parse(c.expiresAt) > Date.now())
        .sort((a, b) => b.expiresAt.localeCompare(a.expiresAt))[0],
    [confirmations, threadId],
  );

  const context = currentContext(ws);
  const attached = context && context.label !== dropped ? context : null;

  // who hears this thread: the consoles wired to the card, or the whole project
  const team = useMemo(() => {
    const running = Object.values(sessions).filter((s) => s.agentKind === 'agent' && s.status !== 'exited');
    if (!card) return running.filter((s) => s.projectId === project.id);
    const wired = new Set(directedTerminals(doc.nodes, doc.edges, card).map((id) => doc.nodes.find((n) => n.id === id)?.data.sessionId));
    return running.filter((s) => wired.has(s.id));
  }, [sessions, card, project.id, doc]);
  const shownTeam = team.filter((s) => s.handle).slice(0, TEAM_CHIPS);
  const consoles = `${team.length} ${team.length === 1 ? 'console' : 'consoles'}`;
  const summary = !team.length ? (card ? 'No console is wired to this card yet' : 'No agent is running yet') : card ? `Directs ${consoles}` : `${consoles} on the team`;

  const fillIn = (t: string) => {
    setText(t);
    inputRef.current?.focus();
  };
  const mention = (handle: string) => fillIn(`${text}${text && !text.endsWith(' ') ? ' ' : ''}@${handle} `);

  const send = async (t: string) => {
    setSending(true);
    try {
      await sendCommand(project.id, orderText(replyText(att.compose(t), reply), attached), { workspaceId: ctl.workspaceId, cardId: card || null, selection: [], replyTo: reply?.id });
      setText('');
      att.clear();
      setReply(null);
    } catch (err) {
      toast.error(`The order was not sent: ${orchError(err)}`);
    } finally {
      setSending(false);
    }
  };

  return (
    <div className="wb-pane-orch wb-orch">
      <header className="wb-orch-head">
        <LeadAvatar name={name} size="md" />
        <div className="wb-orch-who">
          <h2>{name}</h2>
          <p className="truncate" title={summary}>
            {summary}
          </p>
        </div>
        {cards.length > 0 && (
          <select className="control control-sm wb-orch-thread" aria-label="Thread" value={card} onChange={(e) => { setCardId(e.target.value); prefs.set(threadPref, e.target.value); }}>
            <option value="">Whole project</option>
            {cards.map((n) => (
              <option key={n.id} value={n.id}>
                {typeof n.data.title === 'string' && n.data.title ? n.data.title : 'Orchestrator'}
              </option>
            ))}
          </select>
        )}
      </header>
      <div className="wb-agents-thread nowheel">
        <ThreadList
          key={threadId}
          threadId={threadId}
          compact
          lead={name}
          actions={{
            reply: (m, toLead) => {
              setReply(replyDraftOf(m, name, toLead));
              inputRef.current?.focus();
            },
            reuse: (t) => fillIn(att.take(t)),
          }}
          empty={
            <OrderWelcome title={`Hi, I'm ${name}`} lead={name} ideas={orderIdeas(attached)} onIdea={fillIn}>
              <p>{talkFirst ? 'I lead your agents. Tell me what you need and I will hand it out; I only stop to ask when something could destroy work or data. Mention one with @ to reach them directly.' : 'I lead your agents. Tell me what you need, or mention one with @.'}</p>
            </OrderWelcome>
          }
        />
      </div>
      <div className="wb-orch-foot">
        {pending && pending.kind !== 'proposal' && <ConfirmationCard c={pending} compact />}
        {reply && <ReplyChip reply={reply} onClear={() => setReply(null)} />}
        {(attached || shownTeam.length > 0) && (
          <div className="wb-orch-chips">
            {attached && (
              <span className="wb-context-chip wb-pane-chip">
                <Icon name="fileCode" />
                <span className="truncate" title={attached.rel}>
                  {attached.label}
                </span>
                <button type="button" aria-label="Leave this out of the order" title="Leave this out of the order" onClick={() => setDropped(attached.label)}>
                  <Icon name="close" />
                </button>
              </span>
            )}
            {shownTeam.map((s) => (
              <AgentChip key={s.id} session={s} onMention={() => mention(s.handle!)} />
            ))}
            {team.length > TEAM_CHIPS && <span className="wb-orch-more">+{team.length - TEAM_CHIPS}</span>}
          </div>
        )}
        <div className="orch-box wb-pane-input">
          <AttachChips att={att} inputRef={inputRef} />
          <MentionInput
            projectId={project.id}
            workspaceId={ctl.workspaceId}
            cardId={card || undefined}
            value={text}
            onChange={setText}
            onSubmit={(t) => void send(t)}
            local={() => localMentions(ctl.nodes, ctl.sync.state.edges, sessions, (a) => agentById(a)?.name ?? a, card || undefined)}
            placeholder={reply?.handle ? `Reply to @${reply.handle}…` : `Talk to ${name}…`}
            label="Order for the agents"
            onEscape={() => setReply(null)}
            historyKey={`pane.${threadId}`}
            disabled={sending}
            inputRef={inputRef}
            maxRows={6}
            menuPlacement="up"
            attachments={att}
          />
          <Button size="sm" variant="primary" iconOnly icon="send" aria-label="Send order" disabled={!att.canSend(text) || sending} loading={sending} onClick={() => void send(text.trim())} />
        </div>
        <p className="wb-orch-hint">Enter to send · Shift+Enter for a new line · @ to mention</p>
      </div>
    </div>
  );
}

/** What a pane shows, in the side bar and in a tab alike. */
export function PaneBody({ ws, id }: { ws: Workspace; id: string }) {
  return id === ORCHESTRATOR_PANE ? <OrchestratorPane ws={ws} /> : <ConsolePane ws={ws} id={id} />;
}

/** The right side bar. */
export function AgentSideBar({ ws, workspaceId }: { ws: Workspace; workspaceId: string }) {
  useWs(ws);
  const choices = usePaneChoices(ws, workspaceId);
  const active = ws.rightPanes.includes(ws.rightActive) ? ws.rightActive : ws.rightPanes[0];
  return (
    <aside className="wb-right" style={{ width: ws.rightW }} aria-label="Agents">
      <div className="wb-tabs wb-right-tabs" role="tablist" aria-label="Open agents">
        {ws.rightPanes.map((id) => (
          <div
            key={id}
            role="tab"
            tabIndex={0}
            aria-selected={id === active}
            className={`wb-tab ${id === active ? 'is-active' : ''}`}
            onClick={() => ws.revealPane(id)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault();
                ws.revealPane(id);
              }
            }}
          >
            <PaneTitle ws={ws} id={id} />
            <button
              type="button"
              className="wb-tab-close"
              aria-label={id === ORCHESTRATOR_PANE ? 'Close the orchestrator' : 'Close this console'}
              title="Close"
              onClick={(e) => {
                e.stopPropagation();
                ws.askClosePane(id);
              }}
            >
              <Icon name="close" />
            </button>
          </div>
        ))}
        <span className="spacer" />
        <div className="wb-tabs-tools">
          <Menu label="Open an agent here" icon="plus" items={agentMenu(ws, workspaceId, choices, 'side')} />
          {active && <Button size="sm" variant="ghost" iconOnly icon="external" aria-label="Move to an editor tab" title="Move to an editor tab" onClick={() => ws.placePane(active, 'tab')} />}
          <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Hide the agents side bar" title="Hide · Ctrl+Alt+B (agents keep running)" onClick={() => ((ws.rightOpen = false), ws.changed())} />
        </div>
      </div>
      {active ? (
        <div className="wb-right-body">
          <PaneBody key={active} ws={ws} id={active} />
        </div>
      ) : (
        <div className="wb-right-empty">
          <h2>Open an agent</h2>
          <p className="text-3">It works in this project and sees what you select in the editor where the CLI supports it. No tile is added to the board.</p>
          <div className="wb-right-list">
            {choices.unplaced.map((s) => (
              <Button key={s.id} variant="ghost" icon="terminal" onClick={() => ws.placePane(s.id, 'side')}>
                Show {s.handle ? `@${s.handle}` : s.name}
              </Button>
            ))}
            {choices.agents.map((a) => (
              <Button key={a.id} variant="ghost" icon="bot" onClick={() => void startAgentPane(ws, workspaceId, a, 'side')}>
                {a.name}
              </Button>
            ))}
            <Button variant="ghost" icon="orchestrator" onClick={() => ws.placePane(ORCHESTRATOR_PANE, 'side')}>
              Orchestrator
            </Button>
          </div>
        </div>
      )}
    </aside>
  );
}

/** "Stop this agent?" for a console's pane. */
export function ClosePaneDialog({ ws }: { ws: Workspace }) {
  useWs(ws);
  const id = ws.closingPane;
  const s = useSession(id);
  if (!id) return null;
  const close = () => {
    ws.closingPane = '';
    ws.changed();
  };
  const name = paneLabel(id, s);
  return (
    <ConfirmDialog
      title={`Stop ${name}?`}
      confirmLabel="Stop and close"
      danger
      onClose={close}
      onConfirm={() => {
        ws.forgetPane(id);
        void api.deleteSession(id).catch((err) => toast.error(`Could not stop ${name}: ${errorMessage(err)}`));
      }}
    >
      <p>The agent stops and this console closes. Its conversation stays saved by the CLI, so it can be resumed later from a new console.</p>
    </ConfirmDialog>
  );
}
