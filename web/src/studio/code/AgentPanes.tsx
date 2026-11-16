// Agents inside the editor (docs/API-studio.md §7.8): a console or the orchestrator as a pane,
// in the right side bar or as an editor tab. A console here is a normal session without a tile
// on the board, so its status, its needs-you and the link to the editor work as for any other.
// It starts in the card's part; a Root card asks: at the project root (every part) or in a part.

import { createContext, useContext, useEffect, useMemo, useRef, useState, useSyncExternalStore } from 'react';
import { api, errorMessage } from '../../api';
import { useEffectiveSettings } from '../../agents/settings';
import { restartConsole } from '../../agents/actions';
import { agentById, byPriority, useMachineAgents } from '../../agents/catalog';
import { agentPill } from '../../agents/state';
import type { BoardController } from '../../board/controller';
import { connectIntent, directedSessions, directorsOf, editorAgents } from '../../board/groups';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { Menu, type MenuEntry } from '../../components/Menu';
import { toast } from '../../components/Toast';
import { prefs } from '../../lib/format';
import { navigate } from '../../lib/router';
import type { Agent, BoardNode, Project, Session } from '../../lib/types';
import { orchError, sendCommand } from '../../orch/actions';
import { useStoreSessions } from '../../orch/hooks';
import { localMentions } from '../../orch/mentions';
import { AttachChips, useAttachments } from '../../orch/Attachments';
import { MentionInput } from '../../orch/MentionInput';
import { replyDraftOf, replyText, useLeadName, useTalkFirst, type ReplyDraft } from '../../orch/lead';
import { AgentChip, GENERAL_IDEAS, LeadAvatar, OrderWelcome, ReplyChip } from '../../orch/OrderBits';
import { orchStore, useOrch } from '../../orch/store';
import { ConfirmationCard, ThreadList } from '../../orch/ThreadView';
import { store, useProject, useSession, useSessions, useStoreReady } from '../../store';
import { PlacePicker } from '../../board/PlacePicker';
import { isMultiPart, placeFromValue, placeValue } from '../../lib/placement';
import { isRoot, type Placement } from '../../lib/parts';
import { activeKey, defaultPlaceOf, rememberPlace, TerminalView } from './BottomPanel';
import { fitsCard, offered, placeChoices, placeDir } from './partGroups';
import { mountedPart } from './partGit';
import { keyOfUri, LOCAL_MACHINE, type FileKey } from './paths';
import { orderText, type AgentContext } from './SidePanels';
import { useWs } from './useWs';
import { nameOf, ORCHESTRATOR_PANE, type Workspace } from './workspace';

/** What a pane needs from the board around the card; nodeId is the card's (the editor node). */
export const PaneEnv = createContext<{ project: Project; ctl: BoardController; nodeId?: string } | null>(null);

type Where = 'side' | 'tab';

/** A place picked in a Root card's agent menu: it holds while the card works on the same file. */
const picked = new WeakMap<Workspace, { place: Placement; at: FileKey | null }>();

/** Where the card's next agent starts: what was picked for the file in use, else the default. */
export function agentPlace(ws: Workspace): Placement {
  const c = picked.get(ws);
  return c && c.at === activeKey(ws) && offered(ws.paths, c.place) ? c.place : defaultPlaceOf(ws);
}

const placeName = (ws: Workspace, p: Placement) => (isRoot(p) ? 'the project root (every part)' : (mountedPart(ws, p.partId)?.name ?? 'the part'));

/** "Start in: Root | admin | frontend", a row of the agent menus of a Root card. */
function placeChoice(ws: Workspace): MenuEntry {
  return {
    type: 'choice',
    label: 'Start in',
    value: placeValue(agentPlace(ws)),
    options: placeChoices(ws.paths).map((p) => ({
      value: placeValue(p),
      label: isRoot(p) ? 'Root' : (mountedPart(ws, p.partId)?.name ?? p.partId),
      title: isRoot(p) ? 'Root (all parts): on This PC, it reaches every part' : placeName(ws, p),
    })),
    onSelect: (v) => pickPlace(ws, v),
  };
}

/** Pick where the card's next agent starts (placeValue form). */
function pickPlace(ws: Workspace, v: string) {
  const place = placeFromValue(v);
  picked.set(ws, { place, at: activeKey(ws) });
  rememberPlace(ws, place);
  ws.changed();
}

/**
 * Start an agent for this editor: a console with no tile on the board, in the card's part (on
 * that part's machine, or on This PC for a server part, as the hub decides), or in a Root card at
 * the place picked in its agent menu, else the card's default (BottomPanel defaultPlaceOf). `card`
 * is the editor node it is opened in: an orchestrator card wired to that node directs it.
 */
export async function startAgentPane(ws: Workspace, workspaceId: string, agent: Agent, where: Where, place?: Placement, card?: string) {
  try {
    const at = place ?? agentPlace(ws);
    const s = await api.createSession({ workspaceId, preset: agent.id, agent: agent.id, panel: true, ...(card ? { card } : {}), cols: 100, rows: 30, ...ws.placeOf(placeDir(ws.paths, at)) });
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
  /** A project on a server while agents on servers are off: the menu says where to turn them on. */
  serverOff: (() => void) | null;
  /** The editor card (node id) the menu is in: what a new agent records as its card. */
  card?: string;
}

export function usePaneChoices(ws: Workspace, workspaceId: string): PaneChoices {
  // agents run on the part's machine, or on This PC (a root, or a server part through the hub)
  const { agents } = useMachineAgents(ws.paths.multi ? LOCAL_MACHINE : ws.parts[0].machineId);
  const sessions = useSessions();
  const project = useProject(ws.projectId);
  const multiPart = !!project && isMultiPart(project);
  const remote = !!useEffectiveSettings().agents?.remoteTools;
  const sorted = useMemo(() => agents.filter((a) => a.kind === 'agent' && a.available !== false).sort(byPriority), [agents]);
  const placed = ws.paneConsoles;
  // a card of one part offers its own part's agents; a Root card every one
  const unplaced = sessions.filter((s) => s.panel && s.agentKind === 'agent' && s.workspaceId === workspaceId && s.status !== 'exited' && !placed.includes(s.id) && fitsCard(s, ws.paths, multiPart));
  // a part on a server (a Root card: any) while agents on servers are off
  const onServer = ws.paths.multi ? ws.parts.some((p) => p.machineId !== LOCAL_MACHINE) : ws.parts[0].machineId !== LOCAL_MACHINE;
  const serverOff = onServer && !remote ? () => navigate('/settings/agents') : null;
  const card = useContext(PaneEnv)?.nodeId;
  return { agents: sorted, unplaced, serverOff, card };
}

export function agentMenu(ws: Workspace, workspaceId: string, { agents, unplaced, serverOff, card }: PaneChoices, where: Where): MenuEntry[] {
  return [
    ...unplaced.map<MenuEntry>((s) => ({ label: `Show ${s.handle ? `@${s.handle}` : s.name}`, icon: 'terminal', title: 'Already running in this editor', onSelect: () => ws.placePane(s.id, where) })),
    ...(unplaced.length ? (['sep'] as MenuEntry[]) : []),
    ...(serverOff
      ? ([
          { label: 'Turn on agents on servers…', icon: 'sliders', title: 'Agents stay on This PC and work on this server through the hub. Settings › Agents', onSelect: serverOff },
          'sep',
        ] as MenuEntry[])
      : []),
    // a Root card asks where the agent starts: the root (every part) or one part
    ...(ws.paths.multi && agents.length ? ([placeChoice(ws)] as MenuEntry[]) : []),
    ...agents.map<MenuEntry>((a) => ({
      label: a.name,
      icon: 'bot',
      title: ws.paths.multi ? `Start ${a.name} in ${placeName(ws, agentPlace(ws))}` : `Start ${a.name} in the project folder`,
      onSelect: () => void startAgentPane(ws, workspaceId, a, where, agentPlace(ws), card),
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

/**
 * For an agent's pane: the board's orchestrator cards, the editor card that holds the agent (the
 * one it was opened in; an older agent's card of its placement; else the card showing the pane),
 * and which orchestrator cards direct that card.
 */
function useDirectors(s: Session): { cards: BoardNode[]; directors: BoardNode[]; nodeId: string; ctl: BoardController } | null {
  const env = useContext(PaneEnv);
  const ctl = env?.ctl;
  const doc = useSyncExternalStore(ctl?.sync.subscribe ?? noSubscribe, ctl?.sync.getState ?? noDoc);
  if (!ctl || !env?.nodeId || !doc) return null;
  const at = { workspaceId: ctl.workspaceId, project: env.project };
  const holder = doc.nodes.find((n) => n.kind === 'editor' && editorAgents(doc.nodes, n, [s], at).length)?.id ?? env.nodeId;
  const cards = doc.nodes.filter((n) => n.kind === 'orchestrator');
  return { cards, directors: directorsOf(doc.nodes, doc.edges, holder), nodeId: holder, ctl };
}
const noSubscribe = () => () => undefined;
const noDoc = () => null;

const cardName = (n: BoardNode) => (typeof n.data.title === 'string' && n.data.title ? n.data.title : 'Orchestrator');

/**
 * "Directed by Lead" and the link to the board's orchestrator cards, over an agent's pane: the
 * same orchestrator → editor wire as one drawn on the board, so every agent of this card takes
 * the orders of the cards it is wired to.
 */
function PaneLink({ session }: { session: Session }) {
  const d = useDirectors(session);
  if (!d || (!d.cards.length && !d.directors.length)) return null;
  const { ctl, nodeId, cards, directors } = d;
  const at = { workspaceId: ctl.workspaceId, project: ctl.project };
  const link = (c: BoardNode) => {
    const intent = connectIntent(ctl.nodes, ctl.sync.state.edges, c.id, nodeId, at);
    if (!intent.ok) {
      if (intent.reason) toast.info(intent.reason);
      return;
    }
    ctl.apply([intent.op]);
    toast.success(intent.label === 'Disconnected' ? `Disconnected from ${cardName(c)}.` : `Connected: ${cardName(c)} directs the agents of this editor.`);
  };
  const free = cards.filter((c) => !directors.includes(c));
  const items: MenuEntry[] = [
    ...(free.length ? ([{ type: 'heading', label: 'Connect to orchestrator' }] as MenuEntry[]) : []),
    ...free.map<MenuEntry>((c) => ({ label: `${cardName(c)}…`, icon: 'orchestrator', title: `${cardName(c)} gives orders to the agents of this editor`, onSelect: () => link(c) })),
    ...(free.length && directors.length ? (['sep'] as MenuEntry[]) : []),
    ...directors.map<MenuEntry>((c) => ({ label: `Disconnect from ${cardName(c)}`, icon: 'close', onSelect: () => link(c) })),
  ];
  return (
    <div className="wb-pane-link">
      {directors.map((c) => (
        <span key={c.id} className="wb-pane-directed" title={`${cardName(c)} directs the agents of this editor`}>
          <Icon name="orchestrator" />
          <span className="truncate">Directed by {cardName(c)}</span>
        </span>
      ))}
      <span className="spacer" />
      <Menu label="Orchestrator link" icon="wire" items={items} />
    </div>
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
      {s?.agentKind === 'agent' && <PaneLink session={s} />}
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
  const key = model ? keyOfUri(model.uri) : null;
  if (!ed || !model || !key) return null;
  // as the card shows it: "admin/src/x.ts" in a Root card (the lead is at the root)
  const rel = ws.relLabel(key);
  const name = nameOf(key);
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
    // with the agents inside an editor card wired to it
    const wired = new Set(directedSessions(doc.nodes, doc.edges, card, sessions, { workspaceId: ctl.workspaceId, project }));
    return running.filter((s) => wired.has(s.id));
  }, [sessions, card, project, doc, ctl.workspaceId]);
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
            answer: (m, t) => sendCommand(project.id, replyText(t, replyDraftOf(m, name)), { workspaceId: ctl.workspaceId, cardId: card || null, replyTo: m.id }),
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
            local={() => localMentions(ctl.nodes, ctl.sync.state.edges, sessions, (a) => agentById(a)?.name ?? a, card || undefined, { workspaceId: ctl.workspaceId, project })}
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
  const project = useProject(ws.projectId);
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
          {project && ws.paths.multi && <PlacePicker project={project} value={placeValue(agentPlace(ws))} onChange={(v) => pickPlace(ws, v)} label="Start in" />}
          <div className="wb-right-list">
            {choices.unplaced.map((s) => (
              <Button key={s.id} variant="ghost" icon="terminal" onClick={() => ws.placePane(s.id, 'side')}>
                Show {s.handle ? `@${s.handle}` : s.name}
              </Button>
            ))}
            {choices.agents.map((a) => (
              <Button key={a.id} variant="ghost" icon="bot" onClick={() => void startAgentPane(ws, workspaceId, a, 'side', undefined, choices.card)}>
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
