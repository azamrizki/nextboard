// Missions in the thread (orchestrated teamwork): the card pinned at the top of a thread while one
// runs, the same card as the first message of a mission in the history, its plan in the approval
// card, the final summary, and what the team says to each other as compact rows. Shared by the
// command bar's thread, orchestrator cards and the editor's orchestrator pane, like ThreadView.

import { useEffect, useRef, useState } from 'react';
import { jumpToSession } from '../agents/jump';
import { useMarkdown } from '../board/lazy';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon } from '../components/Icon';
import { toast } from '../components/Toast';
import { formatDateTime } from '../lib/format';
import { MarkdownView } from '../studio/MarkdownView';
import { decide, orchError } from './actions';
import { orchApi } from './api';
import { useVisibleTick } from './hooks';
import {
  isMissionActive,
  isMissionSummary,
  missionProgress,
  missionStateLabel,
  missionTone,
  openQuestions,
  roundsLabel,
  taskStateLabel,
  taskTone,
  tasksByAgent,
  teamConversation,
  teamIcon,
  teamRoute,
  teamVerb,
  timeLeftLabel,
  timeOf,
} from './mission';
import { orchStore, useOrch, useThread } from './store';
import type { Confirmation, MissionRun, ThreadMessage } from './types';

const clock = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
};

/** Longer than this, a text starts folded. */
const FOLD_CHARS = 200;

function Fold({ text, className, lines = 2 }: { text: string; className: string; lines?: 2 | 3 | 6 }) {
  const long = text.length > FOLD_CHARS || text.split('\n').length > lines;
  const [open, setOpen] = useState(false);
  return (
    <>
      <span className={`${className} ${long && !open ? `clamp-${lines}` : ''}`}>{text}</span>
      {long && (
        <button type="button" className="link-btn plan-more" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? 'Show less' : 'Show all'}
        </button>
      )}
    </>
  );
}

/** Markdown from the lead (the brief, the summary); the text alone until the renderer has loaded. */
function Md({ text, className }: { text: string; className: string }) {
  const md = useMarkdown();
  if (!md) return <div className={`${className} is-plain`}>{text}</div>;
  return <MarkdownView className={`${className} msg-md`} text={text} onLink={() => true} />;
}

export function MissionStateChip({ state }: { state: string }) {
  return <span className={`mission-state tone-${missionTone(state)}`}>{missionStateLabel(state)}</span>;
}

/** The open confirmation of a mission (its approval, a limit, a held step), if any. */
function useMissionConfirmation(m: MissionRun): Confirmation | undefined {
  return useOrch((s) => {
    const c = m.confirmationId ? s.confirmations[m.confirmationId] : undefined;
    if (c) return c.state === 'pending' ? c : undefined;
    for (const id in s.confirmations) {
      const x = s.confirmations[id];
      if (x.missionId === m.id && x.state === 'pending') return x;
    }
    return undefined;
  });
}

/** Tasks per agent: what each one does, its state and what it reported. */
function MissionTasks({ mission, plan }: { mission: MissionRun; plan?: boolean }) {
  const agents = tasksByAgent(mission.tasks);
  if (!agents.length) return <p className="mission-none">No tasks yet.</p>;
  return (
    <ul className="mission-agents" aria-label="Tasks per agent">
      {agents.map((a) => {
        const sid = a.tasks.find((t) => t.sessionId)?.sessionId;
        return (
          <li key={a.handle} className="mission-agent">
            {sid ? (
              <button type="button" className="plan-handle link-btn" title={`Show @${a.handle}`} onClick={() => jumpToSession(sid)}>
                @{a.handle}
              </button>
            ) : (
              <span className="plan-handle">@{a.handle}</span>
            )}
            <ol className="mission-tasks">
              {a.tasks.map((t) => (
                <li key={t.id} className={`mission-task state-${t.state}`}>
                  <div className="mission-task-head">
                    {!plan && <span className={`mission-dot tone-${taskTone(t.state)}`} aria-hidden="true" />}
                    <span className="mission-task-title">{t.title || t.id}</span>
                    {!!t.dependsOn?.length && <span className="mission-after">after {t.dependsOn.join(', ')}</span>}
                    {!plan && <span className={`mission-task-state tone-${taskTone(t.state)}`}>{taskStateLabel(t.state)}</span>}
                  </div>
                  {plan && t.text && <Fold className="plan-text" text={t.text} lines={3} />}
                  {!plan && t.result && <Fold className="mission-result" text={t.result} />}
                </li>
              ))}
            </ol>
          </li>
        );
      })}
    </ul>
  );
}

/** The shared brief, folded until asked for. */
function MissionDoc({ mission, open: startOpen = false }: { mission: MissionRun; open?: boolean }) {
  const [open, setOpen] = useState(startOpen);
  if (!mission.doc?.trim()) return null;
  return (
    <div className="mission-section">
      <button type="button" className="mission-toggle" aria-expanded={open} onClick={() => setOpen(!open)}>
        <Icon name="fileText" />
        <span>Shared brief</span>
        {mission.docRev > 0 && <span className="mission-sub tabular">rev {mission.docRev}</span>}
        <Icon name={open ? 'chevronUp' : 'chevronDown'} className="mission-chev" />
      </button>
      {open && <Md className="mission-doc" text={mission.doc} />}
    </div>
  );
}

/** What the approval card shows under its prompt (which names the goal): the plan, the tasks per agent, the brief. */
export function MissionPlan({ mission }: { mission: MissionRun }) {
  return (
    <div className="mission-plan">
      {mission.summary && <Fold className="mission-summary-text" text={mission.summary} lines={3} />}
      {mission.doneWhen && (
        <p className="mission-when">
          <strong>Done when</strong> {mission.doneWhen}
        </p>
      )}
      <MissionTasks mission={mission} plan />
      <MissionDoc mission={mission} />
    </div>
  );
}

/** A message one member of the team sent another, as one compact row. */
export function TeamRow({ m, lead, answer }: { m: ThreadMessage; lead: string; answer?: boolean }) {
  const kind = m.team?.kind ?? 'update';
  const r = teamRoute(m, lead);
  return (
    <li className={`msg msg-team kind-${kind} ${answer ? 'is-answer' : ''}`} data-msg={m.id}>
      <div className="team-row">
        <span className="team-icon" title={teamVerb(kind)}>
          <Icon name={teamIcon(kind)} />
        </span>
        <span className="team-route">
          <strong>{r.from}</strong>
          {r.to ? (
            <>
              <Icon name="arrowRight" className="team-arrow" />
              <span className="sr-only">{teamVerb(kind)}</span>
              <strong>{r.to}</strong>
            </>
          ) : (
            <span className="team-verb">{teamVerb(kind)}</span>
          )}
        </span>
        <time className="msg-time" dateTime={m.at} title={formatDateTime(m.at)}>
          {clock(m.at)}
        </time>
      </div>
      {m.text && kind !== 'doc' && <Fold className="team-text" text={m.text} />}
    </li>
  );
}

/** Several team messages in a row: folded to the latest, the rest a click away. */
export function TeamRun({ msgs, lead }: { msgs: ThreadMessage[]; lead: string }) {
  const [open, setOpen] = useState(false);
  const who = [...new Set(msgs.map((m) => teamRoute(m, lead).from))];
  const shown = open ? msgs : msgs.slice(-1);
  return (
    <li className="msg msg-team-run">
      <button type="button" className="team-run-head" aria-expanded={open} onClick={() => setOpen(!open)}>
        <Icon name="users" />
        <span>Team conversation · {msgs.length} messages</span>
        <span className="team-run-who truncate">{who.join(', ')}</span>
        <Icon name={open ? 'chevronUp' : 'chevronDown'} className="mission-chev" />
      </button>
      <ol className="team-rows">
        {shown.map((m) => (
          <TeamRow key={m.id} m={m} lead={lead} />
        ))}
      </ol>
    </li>
  );
}

/** The team conversation inside the mission card. */
function MissionTeam({ mission, messages, lead }: { mission: MissionRun; messages: ThreadMessage[]; lead: string }) {
  const [open, setOpen] = useState(false);
  const rows = teamConversation(messages, mission.id);
  if (!rows.length) return null;
  return (
    <div className="mission-section">
      <button type="button" className="mission-toggle" aria-expanded={open} onClick={() => setOpen(!open)}>
        <Icon name="users" />
        <span>Team conversation</span>
        <span className="mission-sub tabular">{rows.length}</span>
        <Icon name={open ? 'chevronUp' : 'chevronDown'} className="mission-chev" />
      </button>
      {open && (
        <ol className="team-rows mission-team">
          {rows.map((x) => (
            <TeamRow key={x.m.id} m={x.m} lead={lead} answer={x.answer} />
          ))}
        </ol>
      )}
    </div>
  );
}

/** Pause, resume and stop; the open question's answers (Approve, Continue) when there is one. */
function MissionControls({ mission, conf, compact }: { mission: MissionRun; conf?: Confirmation; compact?: boolean }) {
  const [busy, setBusy] = useState('');
  const [stop, setStop] = useState(false);
  const run = async (what: 'pause' | 'resume') => {
    setBusy(what);
    try {
      orchStore.upsertMission(await (what === 'pause' ? orchApi.pauseMission(mission.id) : orchApi.resumeMission(mission.id)));
    } catch (err) {
      toast.error(`Could not ${what} the mission: ${orchError(err)}`);
    } finally {
      setBusy('');
    }
  };
  const answer = async (optionId: string) => {
    if (!conf) return;
    setBusy(optionId);
    await decide(conf, optionId === 'cancel' ? 'cancel' : 'confirm', optionId === 'cancel' || optionId === 'confirm' ? undefined : optionId);
    setBusy('');
  };
  const options = conf ? [...conf.options].sort((a, b) => (a.id === 'cancel' ? -1 : b.id === 'cancel' ? 1 : 0)) : [];
  const awaiting = mission.state === 'awaiting_approval';
  return (
    <div className="mission-actions">
      {mission.state === 'paused' ? (
        <Button size="sm" variant="ghost" icon="play" iconOnly={compact} aria-label="Resume mission" title="Send what is next again" loading={busy === 'resume'} onClick={() => void run('resume')}>
          Resume
        </Button>
      ) : (
        !awaiting && (
          <Button size="sm" variant="ghost" icon="pause" iconOnly={compact} aria-label="Pause mission" title="Send nothing new; agents keep what they are doing" loading={busy === 'pause'} onClick={() => void run('pause')}>
            Pause
          </Button>
        )
      )}
      {!(awaiting && conf) && (
        <Button size="sm" variant="danger-ghost" icon="stop" iconOnly={compact} aria-label="Stop mission" title="End the mission" onClick={() => setStop(true)}>
          Stop
        </Button>
      )}
      <span className="spacer" />
      {options.map((o) => (
        <Button
          key={o.id}
          size="sm"
          variant={o.id === 'cancel' ? 'secondary' : 'primary'}
          loading={busy === o.id}
          disabled={!!busy && busy !== o.id}
          onClick={() => void answer(o.id)}
        >
          {o.label}
        </Button>
      ))}
      {stop && (
        <ConfirmDialog
          title="Stop the mission?"
          confirmLabel="Stop mission"
          danger
          onClose={() => setStop(false)}
          onConfirm={async () => {
            orchStore.upsertMission(await orchApi.stopMission(mission.id));
          }}
        >
          <p>Orders not typed yet are cancelled and nothing new is sent. Agents finish what they are doing now. {mission.lead || 'The lead'} writes where the mission got to.</p>
        </ConfirmDialog>
      )}
    </div>
  );
}

/**
 * One mission: goal, state, progress, rounds and time left; the tasks per agent, the brief, the
 * team conversation and the result a click away. Pinned: at the top of its thread, with controls.
 */
export function MissionCard({ mission, lead, pinned, compact }: { mission: MissionRun; lead: string; pinned?: boolean; compact?: boolean }) {
  const thread = useThread(mission.threadId);
  const conf = useMissionConfirmation(mission);
  const active = isMissionActive(mission);
  const ref = useRef<HTMLElement>(null);
  // the time left comes from the deadline at each render (a mission event renders too); the tick
  // only keeps it moving while the card is on screen
  useVisibleTick(ref, active && mission.state !== 'awaiting_approval');
  const [open, setOpen] = useState(false);
  const p = missionProgress(mission);
  const left = timeLeftLabel(mission, Date.now());
  const questions = active ? openQuestions(thread.messages, mission.id) : 0;
  const approved = !!timeOf(mission.approvedAt);
  const meta = [
    approved ? roundsLabel(mission) : '',
    left,
    questions ? `${questions} open question${questions === 1 ? '' : 's'}` : '',
  ].filter(Boolean);
  return (
    <section ref={ref} className={`mission-card tone-${missionTone(mission.state)} ${pinned ? 'is-pinned' : ''} ${compact ? 'is-compact' : ''}`} aria-label={`Mission: ${mission.goal}`} data-mission={mission.id}>
      <div className="mission-head">
        <Icon name="flag" className="mission-icon" />
        <span className="mission-kicker">Mission</span>
        <MissionStateChip state={mission.state} />
        <span className="spacer" />
        {p.total > 0 && (
          <span className="mission-count tabular" title={`${p.done} of ${p.total} tasks done`}>
            {p.done}/{p.total}
          </span>
        )}
        <button type="button" className="msg-act mission-expand" aria-expanded={open} aria-label={open ? 'Hide the mission details' : 'Show the mission details'} title={open ? 'Hide details' : 'Show tasks, brief and team'} onClick={() => setOpen(!open)}>
          <Icon name={open ? 'chevronUp' : 'chevronDown'} />
        </button>
      </div>
      <p className={`mission-goal ${open ? '' : 'clamp-2'}`} title={open ? undefined : mission.goal}>
        {mission.goal}
      </p>
      {p.total > 0 && (
        <div className="order-bar" role="progressbar" aria-valuemin={0} aria-valuemax={p.total} aria-valuenow={p.done} aria-label={`${p.done} of ${p.total} tasks done`}>
          <span style={{ width: `${(p.done / p.total) * 100}%` }} />
        </div>
      )}
      {meta.length > 0 && <p className="mission-meta tabular">{meta.join(' · ')}</p>}
      {mission.state === 'waiting_owner' && mission.waiting && (
        <p className="mission-waiting" role="status">
          <Icon name="alertCircle" />
          <span>{mission.waiting}</span>
        </p>
      )}
      {pinned && active && <MissionControls mission={mission} conf={conf} compact={compact} />}
      {open && (
        <div className="mission-details">
          {mission.summary && <Fold className="mission-summary-text" text={mission.summary} lines={3} />}
          {mission.doneWhen && (
            <p className="mission-when">
              <strong>Done when</strong> {mission.doneWhen}
            </p>
          )}
          <MissionTasks mission={mission} />
          <MissionDoc mission={mission} />
          <MissionTeam mission={mission} messages={thread.messages} lead={lead} />
          {mission.result && <Md className="mission-doc" text={mission.result} />}
        </div>
      )}
    </section>
  );
}

/** A thread's mission state message: the mission's card at its start, its summary at the end, a line otherwise. */
export function MissionMessage({ m, lead, first, last }: { m: ThreadMessage; lead: string; first: boolean; last: boolean }) {
  const mission = useOrch((s) => (m.missionId ? s.missions[m.missionId] : undefined));
  useEffect(() => {
    if (m.missionId && !mission) void orchStore.loadMission(m.missionId);
  }, [m.missionId, mission]);
  const time = (
    <time className="msg-time" dateTime={m.at} title={formatDateTime(m.at)}>
      {clock(m.at)}
    </time>
  );
  if (isMissionSummary(m, mission, last)) {
    // the hub heads the summary with "Mission done: <goal>", which the card's head already says
    const result = mission?.result?.trim() ?? '';
    const body = result && m.text.includes(result) ? result : m.text || result;
    return (
      <li className="msg msg-mission is-summary" data-msg={m.id}>
        <div className="mission-summary">
          <div className="mission-head">
            <Icon name="check" className="mission-icon" />
            <span className="mission-kicker">Mission done</span>
            <span className="mission-goal-line truncate" title={mission?.goal}>
              {mission?.goal}
            </span>
            <span className="spacer" />
            {time}
          </div>
          <Md className="msg-text mission-result-md" text={body} />
        </div>
      </li>
    );
  }
  return (
    <li className="msg msg-mission" data-msg={m.id}>
      {m.text && (
        <div className="mission-line">
          <Icon name="flag" />
          {/* where a stopped mission got to, or the lead's question: Markdown over several lines */}
          {m.text.includes('\n') ? <Md className="msg-text mission-line-md" text={m.text} /> : <span className="msg-text">{m.text}</span>}
          {time}
        </div>
      )}
      {/* while it runs, the card pinned above is this one */}
      {first && mission && !isMissionActive(mission) && <MissionCard mission={mission} lead={lead} compact />}
    </li>
  );
}
