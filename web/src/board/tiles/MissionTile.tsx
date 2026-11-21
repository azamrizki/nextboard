// Mission tile: an orchestrator card (node kind "orchestrator", data.view "mission") shown as its
// missions. To the hub it is a card like any other: the consoles wired to it are the team, its
// thread is where the lead runs the mission and asks the owner. Here the owner writes a mission by
// hand (goal, tasks per console, order, brief), follows the running one and its questions, and
// finds the past ones to run again (docs/API-orchestration.md §22, DESIGN.md "Mission tile").

import { CanvasOnly } from './host';
import { memo, useEffect, useMemo, useState, useSyncExternalStore, type KeyboardEvent } from 'react';
import { Handle, Position, type NodeProps } from '@xyflow/react';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { SegmentedControl } from '../../components/form';
import { formatDateTime } from '../../lib/format';
import type { Session } from '../../lib/types';
import { isAgentSession, orchError, sendCommand } from '../../orch/actions';
import { orchApi } from '../../orch/api';
import { useSessionList } from '../../orch/hooks';
import { useLeadName } from '../../orch/lead';
import { isMissionActive, missionProgress, missionStateLabel, missionTone } from '../../orch/mission';
import { MissionCard, MissionStateChip } from '../../orch/MissionCard';
import {
  dependencyChoices,
  draftFrom,
  draftProblem,
  emptyDraft,
  MISSION_LIMITS,
  missionInput,
  newTask,
  removeTask,
  REVIEW_ASK,
  type MissionDraft,
  type TaskDraft,
} from '../../orch/missionDraft';
import { AgentChip, LeadAvatar } from '../../orch/OrderBits';
import { orchStore, useActiveMission, useOrch, useThread } from '../../orch/store';
import type { MissionRun } from '../../orch/types';
import { jumpToSession } from '../../agents/jump';
import type { FlowNode } from '../flow';
import { directedTerminals } from '../groups';
import { readOrchestrator } from '../kinds';
import { useBoardEnv, useUi } from '../ui';
import { TileFrame, TileHead } from './common';

type Tab = 'active' | 'new' | 'history';

const ended = (m: MissionRun) => !isMissionActive(m);

/** The missions this tile ran, newest first: from the store (live) and the hub's full list (history). */
function useTileMissions(projectId: string, id: string, past: MissionRun[]): MissionRun[] {
  const live = useOrch((s) => s.missions);
  return useMemo(() => {
    const all = new Map<string, MissionRun>();
    for (const m of past) all.set(m.id, m);
    for (const m of Object.values(live)) if (m.projectId === projectId && (m.threadId === id || m.cardId === id)) all.set(m.id, m);
    return [...all.values()].sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  }, [live, past, projectId, id]);
}

function MissionTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl, ui, project } = useBoardEnv();
  const d = readOrchestrator(data.node);
  const doc = useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);
  const connecting = useUi((s) => s.connectFrom === id);
  const lead = useLeadName(d.title);
  const active = useActiveMission(id);
  const [tab, setTab] = useState<Tab>('active');
  const [draft, setDraft] = useState<MissionDraft | null>(null);
  const [past, setPast] = useState<MissionRun[]>([]);
  const [del, setDel] = useState(false);

  useEffect(() => {
    void orchStore.loadThread(id);
    void orchStore.loadMissions(project.id);
  }, [id, project.id]);

  // the team: every console wired to the tile, directly or through a group
  const directed = directedTerminals(doc.nodes, doc.edges, id);
  const sids = new Set(directed.map((nid) => doc.nodes.find((n) => n.id === nid)?.data.sessionId).filter((s): s is string => typeof s === 'string'));
  const team = useSessionList((s) => sids.has(s.id)).filter((s) => isAgentSession(s) && !!s.handle);
  const handles = team.map((s) => s.handle!);

  const missions = useTileMissions(project.id, id, past);
  const history = missions.filter(ended);
  const activeState = active?.state;
  // the full list once the History tab opens, and again whenever a mission of this tile ends
  useEffect(() => {
    if (tab !== 'history' && activeState) return;
    let gone = false;
    orchApi
      .missionRuns(project.id, false)
      .then((list) => !gone && setPast(list.filter((m) => m.threadId === id || m.cardId === id)))
      .catch(() => undefined);
    return () => {
      gone = true;
    };
  }, [tab, activeState, project.id, id]);

  const write = (from?: MissionDraft) => {
    setDraft(from ?? draft ?? emptyDraft(handles));
    setTab('new');
  };

  const head = active ? active.goal : history[0] ? `Last: ${history[0].goal}` : 'No mission yet';
  const p = active ? missionProgress(active) : null;

  return (
    <TileFrame id={id} kind="orchestrator" selected={!!selected} label={`Mission tile led by ${lead}`} minW={360} minH={360} className={`mission-tile ${connecting ? 'is-connecting' : ''}`}>
      <CanvasOnly>
        <Handle type="target" position={Position.Left} id="in" className="nb-handle nb-handle-in" isConnectable={false} />
        <Handle type="source" position={Position.Right} id="out" className="nb-handle nb-handle-out nb-handle-wire" title="Drag to a console or a group to add it to the team" />
      </CanvasOnly>
      <TileHead
        id={id}
        icon="flag"
        title={<span className="mission-tile-title">{d.title}</span>}
        meta={
          <span className="tile-meta truncate" title={head}>
            {head}
          </span>
        }
        actions={
          <>
            {active && (
              <span className={`mission-chip tone-${missionTone(active.state)}`} title={`${missionStateLabel(active.state)}: ${active.goal}`}>
                {missionStateLabel(active.state)}
                {p && p.total > 0 ? ` · ${p.done}/${p.total}` : ''}
              </span>
            )}
            <Button
              size="sm"
              variant={connecting ? 'primary' : 'ghost'}
              icon={connecting ? 'check' : 'wire'}
              iconOnly
              className="nodrag"
              aria-label={connecting ? 'Done' : 'Connect consoles'}
              aria-pressed={connecting}
              title={connecting ? 'Stop connecting' : 'Click consoles or groups on the board to add them to the team'}
              onClick={() => ui.set({ connectFrom: connecting ? null : id })}
            />
          </>
        }
        menu={[
          { label: connecting ? 'Finish connecting' : 'Connect consoles…', icon: 'wire', onSelect: () => ui.set({ connectFrom: connecting ? null : id }) },
          { label: 'Spawn agents for this tile…', icon: 'plus', onSelect: () => ui.set({ composer: { slots: 2, group: false, orchestratorId: id } }) },
          { label: 'Show as orchestrator card', icon: 'orchestrator', title: 'The lead’s thread and input instead of the missions', onSelect: () => void ctl.updateData(id, { view: null }) },
        ]}
        menuLabel="Mission tile actions"
        deleteItem={{ label: 'Delete tile…', icon: 'trash', danger: true, onSelect: () => setDel(true) }}
      />
      <div className="mission-tabs nodrag">
        <SegmentedControl<Tab>
          size="sm"
          block
          label="Mission tile view"
          value={tab}
          onChange={(t) => (t === 'new' ? write() : setTab(t))}
          options={[
            { value: 'active', label: 'Active', icon: 'flag' },
            { value: 'new', label: 'New', icon: 'plus' },
            { value: 'history', label: history.length ? `History · ${history.length}` : 'History', icon: 'history' },
          ]}
        />
      </div>
      <div className="mission-tile-body nodrag nowheel">
        {tab === 'active' && <ActiveView id={id} lead={lead} mission={active} last={history[0]} team={team} onWrite={() => write()} />}
        {tab === 'new' && (
          <NewView
            id={id}
            lead={lead}
            team={team}
            handles={handles}
            busy={!!active}
            draft={draft ?? emptyDraft(handles)}
            onChange={setDraft}
            onConnect={() => ui.set({ connectFrom: id })}
            onCancel={() => {
              setDraft(null);
              setTab('active');
            }}
            onStarted={() => {
              setDraft(null);
              setTab('active');
            }}
          />
        )}
        {tab === 'history' && <HistoryView missions={history} lead={lead} onRunAgain={(m) => write(draftFrom(m))} />}
      </div>
      <footer className="mission-tile-foot">
        <Icon name="users" />
        <span className="truncate">
          {team.length ? `Team: ${team.length === 1 ? '1 console' : `${team.length} consoles`} wired` : 'No console wired yet'}
        </span>
        <span className="spacer" />
        <LeadAvatar name={lead} />
        <span className="truncate">Led by {lead}</span>
      </footer>
      {del && (
        <ConfirmDialog title={`Delete ${d.title}?`} confirmLabel="Delete tile" danger onClose={() => setDel(false)} onConfirm={() => ctl.deleteNodes([id])}>
          <p>Its wires are removed; the consoles keep running{active ? ' and the mission goes on in the lead’s thread' : ''}. Undo (Ctrl+Z) brings the tile and its wires back.</p>
        </ConfirmDialog>
      )}
    </TileFrame>
  );
}

/** The running mission with its controls and an answer box for the lead; else the last one and a way to start. */
function ActiveView({ id, lead, mission, last, team, onWrite }: { id: string; lead: string; mission?: MissionRun; last?: MissionRun; team: Session[]; onWrite: () => void }) {
  if (!mission) {
    return (
      <div className="mission-empty">
        <span className="orch-badge" aria-hidden="true">
          <Icon name="flag" />
        </span>
        <p className="mission-empty-title">No mission running</p>
        <p className="text-2">
          {team.length
            ? `Write the goal and a task for each console; ${lead} runs it to the end and asks you only when it must.`
            : 'Wire consoles to this tile first: they are the team the mission is given to.'}
        </p>
        <Button size="sm" variant="primary" icon="plus" onClick={onWrite}>
          New mission
        </Button>
        {last && (
          <div className="mission-last">
            <p className="mission-sub">Last mission</p>
            <MissionCard mission={last} lead={lead} compact />
          </div>
        )}
      </div>
    );
  }
  return (
    <div className="mission-active">
      <MissionCard key={mission.id} mission={mission} lead={lead} pinned defaultOpen />
      <AnswerBox id={id} lead={lead} mission={mission} />
    </div>
  );
}

/** Talk to the lead about the mission: answers its question, changes the plan while it awaits approval. */
function AnswerBox({ id, lead, mission }: { id: string; lead: string; mission: MissionRun }) {
  const { ctl, project } = useBoardEnv();
  const thread = useThread(id);
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const asking = mission.state === 'waiting_owner';
  // the lead's question: its last mission message while the mission waits for the owner
  const question = asking ? [...thread.messages].reverse().find((m) => m.kind === 'mission' && m.missionId === mission.id)?.text : undefined;
  const send = async () => {
    const t = text.trim();
    if (!t) return;
    setBusy(true);
    try {
      await sendCommand(project.id, t, { workspaceId: ctl.workspaceId, cardId: id });
      setText('');
    } catch (err) {
      toast.error(`Not sent: ${orchError(err)}`);
    } finally {
      setBusy(false);
    }
  };
  const keys = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      void send();
    }
  };
  const placeholder =
    mission.state === 'awaiting_approval' ? `Ask ${lead} to change the plan…` : asking ? `Answer ${lead}…` : `Tell ${lead} something about the mission…`;
  return (
    <div className={`mission-answer ${asking ? 'is-asking' : ''}`}>
      {question && (
        <p className="mission-question">
          <LeadAvatar name={lead} />
          <span>
            <strong>{lead} asks you</strong> {question}
          </span>
        </p>
      )}
      <div className="orch-box">
        <textarea className="control" rows={asking ? 2 : 1} value={text} placeholder={placeholder} aria-label={placeholder} disabled={busy} onChange={(e) => setText(e.target.value)} onKeyDown={keys} />
        <Button size="sm" variant="primary" iconOnly icon="send" aria-label="Send" disabled={!text.trim() || busy} loading={busy} onClick={() => void send()} />
      </div>
    </div>
  );
}

/** The form: goal, done when, the team, tasks with their console and order, the brief, review first. */
function NewView({
  id,
  lead,
  team,
  handles,
  busy,
  draft,
  onChange,
  onConnect,
  onCancel,
  onStarted,
}: {
  id: string;
  lead: string;
  team: Session[];
  handles: string[];
  busy: boolean;
  draft: MissionDraft;
  onChange: (d: MissionDraft) => void;
  onConnect: () => void;
  onCancel: () => void;
  onStarted: () => void;
}) {
  const { ctl, project } = useBoardEnv();
  const [sending, setSending] = useState(false);
  const [tried, setTried] = useState(false);
  const [brief, setBrief] = useState(!!draft.doc);
  const problem = draftProblem(draft, handles);
  const set = (p: Partial<MissionDraft>) => onChange({ ...draft, ...p });
  const setTask = (tid: string, p: Partial<TaskDraft>) => set({ tasks: draft.tasks.map((t) => (t.id === tid ? { ...t, ...p } : t)) });
  const addTask = (handle = handles[0] ?? '') => set({ tasks: [...draft.tasks, newTask(draft.tasks, handle)] });

  const start = async () => {
    setTried(true);
    if (problem || busy) return;
    setSending(true);
    try {
      const ms = await orchApi.createMission(project.id, missionInput(draft, id, ctl.workspaceId));
      orchStore.upsertMission(ms);
      if (draft.review) {
        // the lead looks the plan over and proposes it again; the owner approves on the tile
        await sendCommand(project.id, REVIEW_ASK, { workspaceId: ctl.workspaceId, cardId: id });
        toast.success(`${lead} is reviewing the plan. Approve it on the tile when it is ready.`);
      } else if (ms.confirmationId) {
        // written by the owner: approving it is the owner's own click on Start
        orchStore.upsertConfirmation(await orchApi.decide(ms.confirmationId, 'confirm', 'approve'));
        toast.success('Mission started.');
      }
      onStarted();
    } catch (err) {
      toast.error(`The mission did not start: ${orchError(err)}`);
    } finally {
      setSending(false);
    }
  };

  return (
    <form
      className="mission-form"
      onSubmit={(e) => {
        e.preventDefault();
        void start();
      }}
    >
      {busy && (
        <p className="mission-waiting" role="status">
          <Icon name="alertCircle" />
          <span>A mission is still running on this tile. Stop it, or add another Mission tile for a second team.</span>
        </p>
      )}
      <label className="field">
        <span className="field-label">Goal</span>
        <textarea className="control" rows={2} maxLength={MISSION_LIMITS.goal} value={draft.goal} placeholder="What the team should achieve" onChange={(e) => set({ goal: e.target.value })} />
      </label>
      <label className="field">
        <span className="field-label">
          Done when<span className="optional"> (optional)</span>
        </span>
        <input className="control" maxLength={MISSION_LIMITS.doneWhen} value={draft.doneWhen} placeholder="e.g. the tests pass and the page works on a phone" onChange={(e) => set({ doneWhen: e.target.value })} />
      </label>

      <div className="field">
        <span className="field-label">Team</span>
        <div className="orch-chips mission-team-chips">
          {team.map((s) => (
            <AgentChip key={s.id} session={s} onMention={() => addTask(s.handle!)} onMenu={(e) => (e.preventDefault(), jumpToSession(s.id))} />
          ))}
          <Button size="sm" variant="ghost" icon="wire" onClick={onConnect}>
            {team.length ? 'Wire more' : 'Wire a console'}
          </Button>
        </div>
        <span className="field-hint">Click a console to give it a task.</span>
      </div>

      <div className="field">
        <span className="field-label">Tasks</span>
        <ol className="mission-task-rows">
          {draft.tasks.map((t) => {
            const choices = dependencyChoices(draft.tasks, t.id);
            const unknown = t.handle && !handles.some((h) => h.toLowerCase() === t.handle.toLowerCase());
            return (
              <li key={t.id} className="mission-task-row">
                <div className="mission-task-line">
                  <span className="mission-task-id tabular">{t.id}</span>
                  <select className="control" aria-label={`Console for ${t.id}`} value={t.handle} onChange={(e) => setTask(t.id, { handle: e.target.value })}>
                    {!t.handle && <option value="">Pick a console</option>}
                    {unknown && <option value={t.handle}>@{t.handle} (not wired)</option>}
                    {handles.map((h) => (
                      <option key={h} value={h}>
                        @{h}
                      </option>
                    ))}
                  </select>
                  <input className="control" aria-label={`Title of ${t.id}`} maxLength={MISSION_LIMITS.title} value={t.title} placeholder="Title (optional)" onChange={(e) => setTask(t.id, { title: e.target.value })} />
                  <Button size="sm" variant="ghost" iconOnly icon="close" aria-label={`Remove ${t.id}`} title="Remove this task" disabled={draft.tasks.length === 1} onClick={() => set({ tasks: removeTask(draft.tasks, t.id) })} />
                </div>
                <textarea
                  className="control"
                  rows={2}
                  aria-label={`What ${t.id} should do`}
                  maxLength={MISSION_LIMITS.text}
                  value={t.text}
                  placeholder="What this console should do"
                  onChange={(e) => setTask(t.id, { text: e.target.value })}
                />
                {choices.length > 0 && (
                  <div className="mission-after-pick" role="group" aria-label={`${t.id} starts after`}>
                    <span className="text-3">Starts after</span>
                    {choices.map((c) => {
                      const on = t.dependsOn.includes(c);
                      return (
                        <button
                          key={c}
                          type="button"
                          className={`mission-dep ${on ? 'is-on' : ''}`}
                          aria-pressed={on}
                          onClick={() => setTask(t.id, { dependsOn: on ? t.dependsOn.filter((x) => x !== c) : [...t.dependsOn, c] })}
                        >
                          {c}
                        </button>
                      );
                    })}
                  </div>
                )}
              </li>
            );
          })}
        </ol>
        <Button size="sm" variant="ghost" icon="plus" disabled={draft.tasks.length >= MISSION_LIMITS.tasks} onClick={() => addTask()}>
          Add task
        </Button>
      </div>

      {brief ? (
        <label className="field">
          <span className="field-label">
            Shared brief<span className="optional"> (Markdown, optional)</span>
          </span>
          <textarea className="control mono" rows={5} value={draft.doc} placeholder="Contracts, names, decisions every console should follow" onChange={(e) => set({ doc: e.target.value })} />
        </label>
      ) : (
        <Button size="sm" variant="ghost" icon="fileText" className="mission-brief-add" onClick={() => setBrief(true)}>
          Add a shared brief
        </Button>
      )}

      <div className="setting-row mission-review">
        <div className="setting-text">
          <span className="setting-title">Let {lead} review the plan first</span>
          <span className="setting-hint">{draft.review ? `${lead} may change the plan; you approve it on this tile.` : 'Off: it starts as written when you press Start.'}</span>
        </div>
        <button type="button" role="switch" className="switch" aria-checked={draft.review} aria-label={`Let ${lead} review the plan first`} onClick={() => set({ review: !draft.review })}>
          <span className="switch-knob" aria-hidden="true" />
        </button>
      </div>

      {tried && problem && (
        <p className="field-error" role="alert">
          <Icon name="alertCircle" />
          {problem}
        </p>
      )}
      <p className="mission-form-note text-3">Rounds and time limits come from Settings → Lead agent.</p>
      <div className="mission-form-actions">
        <span className="spacer" />
        <Button size="sm" variant="secondary" onClick={onCancel}>
          Cancel
        </Button>
        <Button size="sm" variant="primary" type="submit" icon={draft.review ? 'send' : 'play'} loading={sending} disabled={busy || (tried && !!problem)}>
          {draft.review ? `Send to ${lead}` : 'Start mission'}
        </Button>
      </div>
    </form>
  );
}

/** The tile's past missions: state, when, how many tasks; the details and Run again a click away. */
function HistoryView({ missions, lead, onRunAgain }: { missions: MissionRun[]; lead: string; onRunAgain: (m: MissionRun) => void }) {
  const [open, setOpen] = useState<string | null>(null);
  if (!missions.length) return <p className="mission-none">No finished mission on this tile yet.</p>;
  return (
    <ul className="plain mission-history">
      {missions.map((m) => {
        const p = missionProgress(m);
        const tasks = m.tasks?.length ?? 0;
        const isOpen = open === m.id;
        return (
          <li key={m.id} className={`mission-history-row ${isOpen ? 'is-open' : ''}`}>
            <button type="button" className="mission-history-head" aria-expanded={isOpen} onClick={() => setOpen(isOpen ? null : m.id)}>
              <span className="mission-history-goal truncate" title={m.goal}>
                {m.goal}
              </span>
              <Icon name={isOpen ? 'chevronUp' : 'chevronDown'} className="mission-chev" />
              <span className="mission-history-meta">
                <MissionStateChip state={m.state} />
                <span className="text-3 tabular truncate">
                  {tasks === 1 ? '1 task' : `${tasks} tasks`}
                  {p.done ? ` · ${p.done} done` : ''} · {formatDateTime(m.endedAt || m.updatedAt)}
                </span>
              </span>
            </button>
            {isOpen && (
              <div className="mission-history-body">
                <MissionCard mission={m} lead={lead} compact defaultOpen />
                <Button size="sm" variant="ghost" icon="restart" onClick={() => onRunAgain(m)}>
                  Run again
                </Button>
              </div>
            )}
          </li>
        );
      })}
    </ul>
  );
}

export const MissionTile = memo(MissionTileImpl);
