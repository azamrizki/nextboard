// Orchestrator card (node kind "orchestrator", docs/API-orchestration.md §6, D10): directs
// only the consoles wired to it (directly or through a wired group). Its own thread and
// input; Connect mode or a dragged wire adds consoles. The team is a row of chips so the
// thread keeps the room; the full list with its row actions opens on demand.

import { memo, useEffect, useMemo, useRef, useState, useSyncExternalStore, type MouseEvent } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { agentById } from '../../agents/catalog';
import { jumpToSession } from '../../agents/jump';
import { agentPill } from '../../agents/state';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { ContextMenu, Menu, type MenuEntry } from '../../components/Menu';
import { toast } from '../../components/Toast';
import { prefs } from '../../lib/format';
import type { BoardNode, Session } from '../../lib/types';
import { newId } from '../../lib/ids';
import { isAgentSession, killSessions, pauseSessions, resumeSessions, sendCommand, orchError } from '../../orch/actions';
import { useStoreSessions } from '../../orch/hooks';
import { localMentions, mentionToken } from '../../orch/mentions';
import { AttachChips, useAttachments } from '../../orch/Attachments';
import { MentionInput } from '../../orch/MentionInput';
import { orchApi } from '../../orch/api';
import { replyDraftOf, replyText, useLeadName, useTalkFirst, messageAuthor, messageText, type ReplyDraft } from '../../orch/lead';
import { AgentChip, GENERAL_IDEAS, LeadAvatar, LeadProfileDialog, OrderWelcome, ReplyChip } from '../../orch/OrderBits';
import { copyText } from '../../lib/clipboard';
import { orchStore, useThread } from '../../orch/store';
import { ThreadList, type ThreadActions } from '../../orch/ThreadView';
import { restartConsole } from '../../agents/actions';
import type { FlowNode } from '../flow';
import { directedTerminals, TASK_BOARDS } from '../groups';
import { readChecklist, readGroup, readKanban, readOrchestrator } from '../kinds';
import { useBoardEnv, useUi } from '../ui';
import { InlineTitle, TileFrame, TileHead } from './common';

/** The states worth counting in the header, the ones that want attention first. */
const STATE_ORDER = ['needsYou', 'error', 'working', 'paused', 'starting', 'ready'];
/** Below this width the Connect button drops its label, and below the next the summary its count. */
const ROOMY_W = 460;
const TIGHT_W = 400;

/** Consoles that can be paused or resumed, and which of the two applies to them all. */
function pauseControl(list: Session[]) {
  // a console at its shell prompt has no agent to pause
  const running = list.filter((s) => isAgentSession(s) && s.status === 'running' && s.agentState !== 'shell');
  return { running, resume: running.length > 0 && running.every((s) => s.paused) };
}

function OrchestratorTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl, ui, project } = useBoardEnv();
  const d = readOrchestrator(data.node);
  const doc = useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);
  const sessions = useStoreSessions();
  const connecting = useUi((s) => s.connectFrom === id);
  const thread = useThread(id);
  const [text, setText] = useState('');
  const [sending, setSending] = useState(false);
  const att = useAttachments(project.id);
  const [kill, setKill] = useState<Session | null>(null);
  const [del, setDel] = useState(false);
  const [details, setDetails] = useState(() => prefs.get(`orch.team.${id}`) === 'open');
  const [chipMenu, setChipMenu] = useState<{ x: number; y: number; s: Session; nodeId: string; direct: boolean } | null>(null);
  const [reply, setReply] = useState<ReplyDraft | null>(null);
  const [profile, setProfile] = useState(false);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const name = useLeadName(d.title);
  const hubName = useLeadName();
  const talkFirst = useTalkFirst();

  useEffect(() => {
    void orchStore.loadThread(id);
  }, [id]);

  const byId = useMemo(() => new Map(doc.nodes.map((n) => [n.id, n])), [doc.nodes]);
  const allWires = doc.edges.filter((e) => e.kind === 'wire' && e.source === id);
  const taskWires = allWires.filter((e) => TASK_BOARDS.has(byId.get(e.target)?.kind ?? ''));
  const wires = allWires.filter((e) => !taskWires.includes(e));
  const directed = directedTerminals(doc.nodes, doc.edges, id);
  const sessionOfNode = (nid: string) => {
    const n = byId.get(nid);
    return n && typeof n.data.sessionId === 'string' ? sessions[n.data.sessionId] : undefined;
  };
  const agents = directed.map(sessionOfNode).filter((s): s is Session => !!s);

  const disconnect = (target: string) => {
    const e = wires.find((w) => w.target === target);
    if (e) ctl.apply([{ op: 'delEdge', id: e.id }]);
  };

  const send = async (t: string) => {
    setSending(true);
    try {
      await sendCommand(project.id, replyText(att.compose(t), reply), { workspaceId: ctl.workspaceId, cardId: id, replyTo: reply?.id });
      setText('');
      att.clear();
      setReply(null);
    } catch (err) {
      toast.error(`The order was not sent: ${orchError(err)}`);
    } finally {
      setSending(false);
    }
  };

  const fillIn = (t: string) => {
    setText(t);
    inputRef.current?.focus();
  };
  const mention = (token: string) => fillIn(`${text}${text && !text.endsWith(' ') ? ' ' : ''}${token} `);

  const threadActions: ThreadActions = {
    reply: (m, toLead) => {
      setReply(replyDraftOf(m, name, toLead));
      inputRef.current?.focus();
    },
    reuse: (t) => fillIn(att.take(t)),
  };

  const toggleDetails = () => {
    prefs.set(`orch.team.${id}`, details ? 'closed' : 'open');
    setDetails(!details);
  };

  // "4 agents · 1 needs you": the team at a glance; the tooltip counts every state
  const byState = new Map<string, { label: string; n: number }>();
  for (const s of agents) {
    const p = agentPill(s);
    const n = (byState.get(p.state)?.n ?? 0) + 1;
    byState.set(p.state, { label: p.state === 'needsYou' && n > 1 ? 'need you' : p.label.toLowerCase(), n });
  }
  const rank = (state: string) => (STATE_ORDER.indexOf(state) + 100) % 100;
  const states = [...byState.entries()].sort((a, b) => rank(a[0]) - rank(b[0]));
  const counted = states.map(([, v]) => `${v.n} ${v.label}`);
  const howMany = agents.length === 1 ? '1 agent' : `${agents.length} agents`;
  const summary = data.node.w < TIGHT_W ? (counted[0] ?? howMany) : [howMany, ...counted.slice(0, 1)].join(' · ');
  const wantsYou = byState.has('needsYou');

  const team = pauseControl(agents);

  /** A board of its own, right under the card and already wired to it. */
  const addTaskBoard = () => {
    const nodeId = newId('n');
    const col = (title: string) => ({ id: newId('i'), title, cards: [] });
    void ctl.apply([
      {
        op: 'put',
        node: { id: nodeId, kind: 'kanban', x: data.node.x, y: data.node.y + data.node.h + 40, w: 900, h: 420, data: { title: `${name}'s tasks`, columns: ['To do', 'In progress', 'Review', 'Done'].map(col) } } as BoardNode,
      },
      { op: 'putEdge', edge: { id: newId('e'), source: id, target: nodeId, kind: 'wire', data: {} } },
    ]);
  };

  const menu: MenuEntry[] = [
    { label: connecting ? 'Finish connecting' : 'Connect consoles…', icon: 'wire', onSelect: () => ui.set({ connectFrom: connecting ? null : id }) },
    { label: 'New task board', icon: 'kanban', onSelect: () => addTaskBoard() },
    { label: 'Spawn agents for this card…', icon: 'plus', onSelect: () => ui.set({ composer: { slots: 2, group: false, orchestratorId: id } }) },
    { label: 'Lead profile…', icon: 'user', onSelect: () => setProfile(true) },
    {
      label: 'Copy thread',
      icon: 'copy',
      disabled: !thread.messages.length,
      onSelect: () =>
        void copyText(thread.messages.map((m) => `${messageAuthor(m, name)}: ${messageText(m)}`).join('\n\n')).then((ok) => (ok ? toast.success('Thread copied.') : toast.error('Could not copy the thread.'))),
    },
    { label: 'Clear thread', icon: 'broom', disabled: !thread.messages.length, onSelect: () => void orchApi.clearThread(id).catch((err) => toast.error(orchError(err))) },
  ];

  const agentItems = (s: Session, nodeId: string, wiredDirect: boolean): MenuEntry[] => [
    { label: 'Focus', icon: 'target', onSelect: () => jumpToSession(s.id) },
    s.paused
      ? { label: 'Resume', icon: 'play', onSelect: () => void resumeSessions([s]) }
      : { label: 'Pause', icon: 'pause', disabled: s.status !== 'running' || s.agentState === 'shell', onSelect: () => void pauseSessions([s]) },
    { label: 'Restart', icon: 'restart', onSelect: () => void restartConsole(s, s.agentSessionId ? { mode: 'resume' } : {}) },
    'sep',
    ...(wiredDirect ? [{ label: 'Disconnect', icon: 'close', onSelect: () => disconnect(nodeId) } as MenuEntry] : []),
    { label: 'Kill…', icon: 'power', danger: true, disabled: s.status === 'exited', onSelect: () => setKill(s) },
  ];

  const openChipMenu = (e: MouseEvent, s: Session, nodeId: string, direct: boolean) => {
    e.preventDefault();
    e.stopPropagation(); // not the board's own menu for this card
    setChipMenu({ x: e.clientX, y: e.clientY, s, nodeId, direct });
  };

  const rowFor = (s: Session, nodeId: string, wiredDirect: boolean) => {
    const pill = agentPill(s);
    return (
      <li key={s.id} className="orch-row" onContextMenu={(e) => openChipMenu(e, s, nodeId, wiredDirect)}>
        <span className={`dot dot-${pill.dot}`} aria-hidden="true" />
        <button type="button" className="orch-handle nodrag" onClick={() => mention(`@${s.handle ?? s.name}`)} title="Click to mention">
          @{s.handle ?? s.name}
        </button>
        <span className="orch-agent truncate">{agentById(s.agent || s.preset)?.name ?? s.agent}</span>
        <span className="spacer" />
        <span className={`orch-state tone-${pill.tone}`} title={pill.detail}>
          {pill.label}
        </span>
        <span className="orch-row-actions">
          <Button size="sm" variant="ghost" iconOnly icon="target" className="nodrag" aria-label={`Focus @${s.handle}`} title="Show on the board" onClick={() => jumpToSession(s.id)} />
          {s.paused ? (
            <Button size="sm" variant="ghost" iconOnly icon="play" className="nodrag" aria-label={`Resume @${s.handle}`} title="Resume" onClick={() => void resumeSessions([s])} />
          ) : (
            <Button size="sm" variant="ghost" iconOnly icon="pause" className="nodrag" aria-label={`Pause @${s.handle}`} title="Pause" disabled={s.status !== 'running'} onClick={() => void pauseSessions([s])} />
          )}
          <Menu className="nodrag" label={`Actions for @${s.handle ?? s.name}`} items={agentItems(s, nodeId, wiredDirect)} />
        </span>
      </li>
    );
  };

  const placeholder = reply ? (reply.handle ? `Reply to @${reply.handle}…` : `Tell ${name}…`) : agents.length ? `Talk to ${name}… (@ to reach an agent)` : `Talk to ${name}…`;
  const roomy = data.node.w >= ROOMY_W;
  const connectConsoles = (
    <Button size="sm" icon="wire" onClick={() => ui.set({ connectFrom: id })}>
      Connect
    </Button>
  );
  const spawnAgents = (
    <Button size="sm" variant="primary" icon="plus" onClick={() => ui.set({ composer: { slots: 2, group: false, orchestratorId: id } })}>
      Spawn agent
    </Button>
  );

  return (
    <TileFrame id={id} kind="orchestrator" selected={!!selected} label={`Lead agent ${name}`} minW={320} minH={300} className={connecting ? 'is-connecting' : ''}>
      <Handle type="target" position={Position.Left} id="in" className="nb-handle nb-handle-in" isConnectable={false} />
      <Handle type="source" position={Position.Right} id="out" className="nb-handle nb-handle-out nb-handle-wire" title="Drag to a console or group to wire it" />
      <TileHead
        id={id}
        icon={<LeadAvatar name={name} />}
        title={<InlineTitle value={name} label="Lead's name" maxLength={24} onCommit={(title) => ctl.updateData(id, { title })} />}
        meta={
          <span className={`tile-meta tabular truncate orch-summary ${wantsYou ? 'is-needs' : ''}`} title={[howMany, ...counted].join(' · ')}>
            {summary}
          </span>
        }
        actions={
          <>
            {team.running.length > 0 &&
              (team.resume ? (
                <Button size="sm" variant="ghost" iconOnly icon="play" className="nodrag" aria-label="Resume all" title={`Resume all ${team.running.length}`} onClick={() => void resumeSessions(team.running)} />
              ) : (
                <Button size="sm" variant="ghost" iconOnly icon="pause" className="nodrag" aria-label="Pause all" title={`Pause all ${team.running.length}`} onClick={() => void pauseSessions(team.running)} />
              ))}
            <Button
              size="sm"
              variant={connecting ? 'primary' : 'ghost'}
              icon={connecting ? 'check' : 'wire'}
              iconOnly={!roomy}
              className="nodrag"
              aria-label={connecting ? 'Done' : 'Connect'}
              aria-pressed={connecting}
              onClick={() => ui.set({ connectFrom: connecting ? null : id })}
              title={connecting ? 'Stop connecting' : 'Click consoles or groups on the board to wire them'}
            >
              {roomy ? (connecting ? 'Done' : 'Connect') : null}
            </Button>
          </>
        }
        menu={menu}
        menuLabel={`Actions for ${name}`}
        deleteItem={{ label: 'Delete card…', icon: 'trash', danger: true, onSelect: () => setDel(true) }}
      />
      <div className="orch-body nodrag nowheel">
        {taskWires.length > 0 && (
          <ul className="plain orch-tasks" aria-label="Task boards">
            {taskWires.map((w) => {
              const t = byId.get(w.target)!;
              const tasks = t.kind === 'kanban' ? readKanban(t).columns.flatMap((c) => c.cards) : readChecklist(t).items;
              const done = tasks.filter((x) => x.done).length;
              const autoRun = t.data.autoRun === true;
              const title = t.kind === 'kanban' ? readKanban(t).title : readChecklist(t).title;
              return (
                <li key={w.id} className="orch-task-row">
                  <Icon name={t.kind === 'kanban' ? 'kanban' : 'checklist'} />
                  <span className="truncate" title={title}>
                    {title}
                  </span>
                  <span className="text-3 tabular">
                    {tasks.length - done} open · {done} done
                  </span>
                  <span className="spacer" />
                  <button
                    type="button"
                    role="switch"
                    className="switch"
                    aria-checked={autoRun}
                    aria-label={`${name} hands out the tasks of ${title}`}
                    title="On: what waits in To do goes to a free agent of the team, without asking first. Each task costs that agent a turn."
                    onClick={() => void ctl.updateData(t.id, { autoRun: autoRun ? null : true })}
                  >
                    <span className="switch-knob" aria-hidden="true" />
                  </button>
                </li>
              );
            })}
          </ul>
        )}
        {wires.length > 0 ? (
          <section className={`orch-team ${details ? 'is-open' : ''}`} aria-label="Directed consoles">
            {details ? (
              <ul className="orch-rows">
                {wires.map((w) => {
                  const t = byId.get(w.target);
                  if (!t) return null;
                  if (t.kind === 'group') {
                    const g = readGroup(t);
                    const control = pauseControl(g.members.map(sessionOfNode).filter((s): s is Session => !!s));
                    return (
                      <li key={w.id} className="orch-group">
                        <div className="orch-group-head">
                          <span className="group-dot" style={{ background: `var(--tag-${g.color})` }} aria-hidden="true" />
                          <span className="truncate">{g.name}</span>
                          <span className="orch-agent">{g.members.length === 1 ? '1 console' : `${g.members.length} consoles`}</span>
                          <span className="spacer" />
                          <span className="orch-row-actions">
                            {control.running.length > 0 && (
                              <Button
                                size="sm"
                                variant="ghost"
                                iconOnly
                                icon={control.resume ? 'play' : 'pause'}
                                className="nodrag"
                                aria-label={`${control.resume ? 'Resume' : 'Pause'} ${g.name}`}
                                title={`${control.resume ? 'Resume' : 'Pause'} this group`}
                                onClick={() => void (control.resume ? resumeSessions(control.running) : pauseSessions(control.running))}
                              />
                            )}
                            <Button size="sm" variant="ghost" iconOnly icon="close" className="nodrag" aria-label={`Disconnect ${g.name}`} title="Disconnect this group" onClick={() => disconnect(t.id)} />
                          </span>
                        </div>
                        <ul className="orch-rows nested">
                          {g.members.map((m) => {
                            const s = sessionOfNode(m);
                            return s ? rowFor(s, m, false) : null;
                          })}
                        </ul>
                      </li>
                    );
                  }
                  const s = sessionOfNode(t.id);
                  return s ? rowFor(s, t.id, true) : null;
                })}
              </ul>
            ) : (
              <div className="orch-chips">
                {wires.map((w) => {
                  const t = byId.get(w.target);
                  if (!t) return null;
                  if (t.kind === 'group') {
                    const g = readGroup(t);
                    return (
                      <span key={w.id} className="orch-chip-set">
                        <button type="button" className="orch-set-label" title={`Mention the whole of ${g.name}`} onClick={() => mention(mentionToken({ kind: 'group', id: t.id, label: g.name }))}>
                          <span className="group-dot" style={{ background: `var(--tag-${g.color})` }} aria-hidden="true" />
                          <span className="truncate">{g.name}</span>
                        </button>
                        {g.members.map((m) => {
                          const s = sessionOfNode(m);
                          return s ? <AgentChip key={s.id} session={s} onMention={() => mention(`@${s.handle ?? s.name}`)} onMenu={(e) => openChipMenu(e, s, m, false)} /> : null;
                        })}
                      </span>
                    );
                  }
                  const s = sessionOfNode(t.id);
                  return s ? <AgentChip key={s.id} session={s} onMention={() => mention(`@${s.handle ?? s.name}`)} onMenu={(e) => openChipMenu(e, s, t.id, true)} /> : null;
                })}
              </div>
            )}
            <Button
              size="sm"
              variant="ghost"
              iconOnly
              icon={details ? 'chevronUp' : 'chevronDown'}
              className="nodrag orch-team-toggle"
              aria-expanded={details}
              aria-label={details ? 'Show the team as chips' : 'Show the team in full'}
              title={details ? 'Show as chips' : 'Show every console with its actions'}
              onClick={toggleDetails}
            />
          </section>
        ) : (
          thread.messages.length > 0 && (
            <section className="orch-team is-none">
              <span className="truncate">No consoles wired</span>
              <span className="spacer" />
              {connectConsoles}
            </section>
          )
        )}
        <ThreadList
          threadId={id}
          compact
          lead={name}
          actions={threadActions}
          empty={
            wires.length ? (
              <OrderWelcome title={`Hi, I'm ${name}`} lead={name} ideas={GENERAL_IDEAS.slice(0, 2)} onIdea={fillIn}>
                <p>{talkFirst ? 'I lead this team. Tell me what you need and I will hand it out. I only stop to ask when something could destroy work or data.' : 'I lead this team. Tell me what you need and I will hand it out.'}</p>
              </OrderWelcome>
            ) : (
              <OrderWelcome title={`Hi, I'm ${name}`} lead={name}>
                <p>I have no team on this card yet. Talk to me, connect consoles from the board, or start new agents.</p>
                <div className="row-gap">
                  {connectConsoles}
                  {spawnAgents}
                </div>
              </OrderWelcome>
            )
          }
        />
      </div>
      <div className="orch-input nodrag">
        {reply && <ReplyChip reply={reply} onClear={() => setReply(null)} />}
        <div className="orch-box">
          <AttachChips att={att} inputRef={inputRef} />
          <MentionInput
            projectId={project.id}
            workspaceId={ctl.workspaceId}
            cardId={id}
            value={text}
            onChange={setText}
            onSubmit={(t) => void send(t)}
            local={() => localMentions(ctl.nodes, ctl.sync.state.edges, sessions, (a) => agentById(a)?.name ?? a, id)}
            placeholder={placeholder}
            label={`Message for ${name}`}
            onEscape={() => setReply(null)}
            historyKey={`card.${id}`}
            disabled={sending}
            inputRef={inputRef}
            maxRows={4}
            menuPlacement="up"
            attachments={att}
          />
          <Button size="sm" variant="primary" iconOnly icon="send" aria-label="Send order" disabled={!att.canSend(text) || sending} loading={sending} onClick={() => void send(text.trim())} />
        </div>
      </div>
      {profile && (
        <LeadProfileDialog
          name={name === hubName ? '' : name}
          personality={d.personality ?? ''}
          fallback={hubName}
          onSave={(title, personality) => ctl.updateData(id, { title: title || 'Orchestrator', personality })}
          onClose={() => setProfile(false)}
        />
      )}
      {chipMenu && (
        <ContextMenu x={chipMenu.x} y={chipMenu.y} label={`Actions for @${chipMenu.s.handle ?? chipMenu.s.name}`} items={agentItems(chipMenu.s, chipMenu.nodeId, chipMenu.direct)} onClose={() => setChipMenu(null)} />
      )}
      {del && (
        <ConfirmDialog title={`Delete ${d.title}?`} confirmLabel="Delete card" danger onClose={() => setDel(false)} onConfirm={() => ctl.deleteNodes([id])}>
          <p>Its wires are removed; the consoles keep running. Undo (Ctrl+Z) brings the card and its wires back.</p>
        </ConfirmDialog>
      )}
      {kill && (
        <ConfirmDialog title={`Kill @${kill.handle ?? kill.name}?`} confirmLabel="Kill console" danger onClose={() => setKill(null)} onConfirm={() => killSessions([kill])}>
          <p>The process stops. The tile stays on the board marked DONE; restart it from its menu.</p>
        </ConfirmDialog>
      )}
    </TileFrame>
  );
}

export const OrchestratorTile = memo(OrchestratorTileImpl);
