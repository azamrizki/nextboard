// Loops panel (docs/API-orchestration.md §11, D20; L): recurring instructions that the
// hub runs 24/7 — on a schedule, a cron line, when an agent finishes a turn, or when its
// output shows a pattern — with stop conditions and goals. Live status and history.

import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { agentById } from '../../agents/catalog';
import type { BoardController } from '../../board/controller';
import { readGroup, readOrchestrator } from '../../board/kinds';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Checkbox, Field, SegmentedControl, SelectField, TextField } from '../../components/form';
import { Icon, type IconName } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { formatCountdown, formatDateTime, timeAgo } from '../../lib/format';
import type { Project } from '../../lib/types';
import { isAgentSession, orchError } from '../actions';
import { orchApi } from '../api';
import { useNow, useWorkspaceSessions } from '../hooks';
import { is404, orchStore, useOrch } from '../store';
import type { LoopDef, LoopGoal, LoopHistoryEntry, LoopInput, LoopTargets, LoopTemplate, LoopTrigger } from '../types';

type TriggerKind = LoopTrigger['kind'];
const PRESETS = [5, 10, 15, 30, 60, 120];
const DURATIONS = [
  { v: 900, l: '15 min' },
  { v: 1800, l: '30 min' },
  { v: 3600, l: '1 hour' },
  { v: 7200, l: '2 hours' },
  { v: 14400, l: '4 hours' },
  { v: 28800, l: '8 hours' },
  { v: 86400, l: '24 hours' },
];

/** Shown when the hub has no template list (names are NextBoard's own). */
const FALLBACK_TEMPLATES: LoopTemplate[] = [
  { name: 'Status ping', text: 'Give me a one-line status of what you are doing.', trigger: { kind: 'interval', everySec: 1800 } },
  { name: 'Nudge idle agents', text: 'If you are waiting on something, continue with the next step of your task.', trigger: { kind: 'agentIdle' } },
  { name: 'Build fixer', text: 'Run the build and fix any error you find.', trigger: { kind: 'output', pattern: 'error', regex: false } },
  { name: 'Test guard', text: 'Run the test suite and fix any failure.', trigger: { kind: 'interval', everySec: 900 }, stop: { goal: { kind: 'agentJudged', text: 'All tests pass', agent: 'claude' } } },
  { name: 'Checkpoint commit', text: 'Commit your current work with a short message describing it.', trigger: { kind: 'interval', everySec: 3600 } },
  { name: 'Review round', text: 'Review the latest changes for bugs and risky code; reply with findings only.', trigger: { kind: 'interval', everySec: 7200 } },
  { name: 'Daily summary', text: 'Summarise what you did today in five bullets.', trigger: { kind: 'cron', expr: '0 18 * * 1-5' } },
];

export function triggerLabel(t: LoopTrigger): string {
  switch (t.kind) {
    case 'interval': {
      const m = Math.round(t.everySec / 60);
      return m % 60 === 0 && m >= 60 ? `Every ${m / 60} h` : `Every ${m} min`;
    }
    case 'cron':
      return `Cron ${t.expr}`;
    case 'agentIdle':
      return 'When an agent finishes a turn';
    case 'output':
      return `When output shows “${t.pattern}”`;
  }
}

const STATUS_TONE: Record<string, string> = { running: 'badge-success', paused: 'badge-warning', stopped: '', done: 'badge-info' };
const STOP_REASON: Record<string, string> = {
  maxRuns: 'reached its run limit',
  time: 'reached its time limit',
  goal: 'goal met',
  output: 'saw the stop text',
  user: 'stopped by you',
  error: 'stopped after an error',
};

export default function LoopsPanel({ ctl, project, onClose }: { ctl: BoardController; project: Project; onClose: () => void }) {
  const all = useOrch((s) => s.loops);
  const loaded = useOrch((s) => s.loopsLoaded[project.id]);
  const [editing, setEditing] = useState<LoopDef | 'new' | null>(null);
  const [history, setHistory] = useState<string | null>(null);
  const [del, setDel] = useState<LoopDef | null>(null);
  const now = useNow(1000);

  useEffect(() => {
    void orchStore.loadLoops(project.id, true);
  }, [project.id]);

  useEffect(() => {
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('.modal-layer') && !(e.target as HTMLElement)?.closest?.('input, textarea, select')) onClose();
    };
    document.addEventListener('keydown', esc);
    return () => document.removeEventListener('keydown', esc);
  }, [onClose]);

  const loops = useMemo(
    () => Object.values(all).filter((l) => l.projectId === project.id).sort((a, b) => (a.createdAt ?? '').localeCompare(b.createdAt ?? '') || a.name.localeCompare(b.name)),
    [all, project.id],
  );
  const running = loops.filter((l) => l.state?.status === 'running').length;

  const act = async (fn: () => Promise<LoopDef>, done?: string) => {
    try {
      orchStore.upsertLoop(await fn());
      if (done) toast.success(done);
    } catch (err) {
      toast.error(orchError(err));
    }
  };

  return (
    <aside className="side-panel loops-panel" aria-label="Loops">
      <div className="side-panel-head">
        <Icon name="loop" />
        <h2>Loops</h2>
        {running > 0 && <span className="badge badge-success">{running} running</span>}
        <span className="spacer" />
        {running > 0 && !editing && (
          <Button size="sm" variant="ghost" icon="stop" onClick={() => void orchApi.stopAllLoops().then((r) => toast.success(`Stopped ${r.stopped} loop${r.stopped === 1 ? '' : 's'}.`), (err) => toast.error(orchError(err)))}>
            Stop all
          </Button>
        )}
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close loops" onClick={onClose} />
      </div>
      {editing ? (
        <LoopEditor ctl={ctl} project={project} loop={editing === 'new' ? null : editing} onDone={() => setEditing(null)} />
      ) : (
        <>
          <p className="side-panel-hint">The hub sends these instructions to your agents on schedule, even when no browser is open. Busy agents are skipped, never interrupted.</p>
          {loaded === 'unsupported' && <div className="list-msg">This hub does not run loops yet. Update NextBoard on the hub.</div>}
          {loaded === 'loading' && !loops.length && (
            <div className="list-msg" role="status">
              <span className="spinner" aria-hidden="true" /> Loading…
            </div>
          )}
          {loaded !== 'unsupported' && (
            <Button variant="primary" size="sm" icon="plus" className="loops-new" onClick={() => setEditing('new')}>
              New loop
            </Button>
          )}
          {loaded === 'ready' && loops.length === 0 && <div className="list-msg">No loops yet. Start from a template, like “Test guard”.</div>}
          <ul className="loop-list">
            {loops.map((l) => {
              const st = l.state?.status ?? (l.enabled ? 'running' : 'stopped');
              const next = l.state?.nextRunAt ? Date.parse(l.state.nextRunAt) - now : NaN;
              return (
                <li key={l.id} className={`loop-item status-${st}`}>
                  <div className="loop-top">
                    <span className="loop-name truncate">{l.name}</span>
                    <span className={`badge ${STATUS_TONE[st] ?? ''}`}>{st[0].toUpperCase() + st.slice(1)}</span>
                  </div>
                  <div className="loop-meta">
                    <span>{triggerLabel(l.trigger)}</span>
                    <span>· {targetsLabel(ctl, l.targets)}</span>
                  </div>
                  <p className="loop-text clamp-2" title={l.text}>
                    {l.text}
                  </p>
                  <div className="loop-meta tabular">
                    <span>
                      {l.state?.runs ?? 0}
                      {l.stop.maxRuns ? ` / ${l.stop.maxRuns}` : ''} runs
                    </span>
                    {st === 'running' && Number.isFinite(next) && <span>· next in {formatCountdown(Math.max(0, next))}</span>}
                    {st === 'running' && !Number.isFinite(next) && l.trigger.kind !== 'interval' && l.trigger.kind !== 'cron' && <span>· waiting for a trigger</span>}
                    {l.state?.lastRunAt && <span>· last {timeAgo(l.state.lastRunAt, now)}</span>}
                    {(st === 'done' || st === 'stopped') && l.state?.stoppedReason && <span>· {STOP_REASON[l.state.stoppedReason] ?? l.state.stoppedReason}</span>}
                  </div>
                  <div className="loop-actions">
                    {st === 'running' ? (
                      <Button size="sm" icon="stop" onClick={() => void act(() => orchApi.stopLoop(l.id))}>
                        Stop
                      </Button>
                    ) : (
                      <Button size="sm" icon="play" onClick={() => void act(() => orchApi.startLoop(l.id))}>
                        Start
                      </Button>
                    )}
                    <Button size="sm" variant="ghost" icon="bolt" onClick={() => void act(() => orchApi.runLoopNow(l.id), `“${l.name}” runs now.`)}>
                      Run now
                    </Button>
                    <Button size="sm" variant="ghost" icon="pencil" onClick={() => setEditing(l)}>
                      Edit
                    </Button>
                    <Button size="sm" variant="ghost" icon="history" aria-expanded={history === l.id} onClick={() => setHistory(history === l.id ? null : l.id)}>
                      History
                    </Button>
                    <span className="spacer" />
                    <Button size="sm" variant="danger-ghost" iconOnly icon="trash" aria-label={`Delete ${l.name}`} onClick={() => setDel(l)} />
                  </div>
                  {history === l.id && <LoopHistory loopId={l.id} />}
                </li>
              );
            })}
          </ul>
        </>
      )}
      {del && (
        <ConfirmDialog
          title={`Delete “${del.name}”?`}
          confirmLabel="Delete loop"
          danger
          onClose={() => setDel(null)}
          onConfirm={async () => {
            await orchApi.deleteLoop(del.id);
            orchStore.removeLoop(del.id);
          }}
        >
          <p>The loop stops and its history is removed. Agents keep what they already received.</p>
        </ConfirmDialog>
      )}
    </aside>
  );
}

function targetsLabel(ctl: BoardController, t: LoopTargets): string {
  if (t.kind === 'allAgents') return 'all agents';
  if (t.kind === 'group') {
    const g = ctl.node(t.ids[0] ?? '');
    return g ? readGroup(g).name : 'a group';
  }
  if (t.kind === 'orchestrator') {
    const c = ctl.node(t.ids[0] ?? '');
    return c ? `${readOrchestrator(c).title}’s consoles` : 'a card’s consoles';
  }
  return t.ids.length === 1 ? '1 console' : `${t.ids.length} consoles`;
}

const ENTRY_ICON: Record<string, IconName> = { run: 'play', skip: 'arrowRight', retry: 'refresh', goalCheck: 'target', stop: 'stop' };

function LoopHistory({ loopId }: { loopId: string }) {
  const entries = useOrch((s) => s.loopHistory[loopId]);
  useEffect(() => {
    void orchStore.loadLoopHistory(loopId);
  }, [loopId]);
  if (!entries) return <div className="list-msg">Loading history…</div>;
  if (!entries.length) return <div className="list-msg">No runs yet.</div>;
  return (
    <ol className="loop-history" aria-label="Loop history">
      {entries.slice(0, 30).map((e: LoopHistoryEntry, i) => (
        <li key={`${e.at}-${i}`} className={`hist-${e.kind}`}>
          <Icon name={ENTRY_ICON[e.kind] ?? 'dot'} />
          <time dateTime={e.at} title={formatDateTime(e.at)}>
            {new Date(e.at).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}
          </time>
          <span className="hist-note">
            {e.note}
            {e.goal && <> — goal {e.goal.met ? 'met' : 'not met'}{e.goal.reason ? `: ${e.goal.reason}` : ''}</>}
            {e.targets && e.targets.length > 0 && <span className="text-3"> ({e.targets.map((t) => t.result).join(', ')})</span>}
          </span>
        </li>
      ))}
    </ol>
  );
}

// ---- editor ------------------------------------------------------------------------

function LoopEditor({ ctl, project, loop, onDone }: { ctl: BoardController; project: Project; loop: LoopDef | null; onDone: () => void }) {
  const sessions = useWorkspaceSessions(ctl.workspaceId).filter(isAgentSession);
  const groups = ctl.nodes.filter((n) => n.kind === 'group');
  const cards = ctl.nodes.filter((n) => n.kind === 'orchestrator');
  const [templates, setTemplates] = useState<LoopTemplate[]>(FALLBACK_TEMPLATES);
  const [name, setName] = useState(loop?.name ?? '');
  const [text, setText] = useState(loop?.text ?? '');
  const [kind, setKind] = useState<TriggerKind>(loop?.trigger.kind ?? 'interval');
  const [every, setEvery] = useState(loop?.trigger.kind === 'interval' ? Math.round(loop.trigger.everySec / 60) : 15);
  const [cron, setCron] = useState(loop?.trigger.kind === 'cron' ? loop.trigger.expr : '*/30 9-18 * * 1-5');
  const [pattern, setPattern] = useState(loop?.trigger.kind === 'output' ? loop.trigger.pattern : '');
  const [regex, setRegex] = useState(loop?.trigger.kind === 'output' ? !!loop.trigger.regex : false);
  const [tkind, setTkind] = useState<LoopTargets['kind']>(loop?.targets.kind ?? 'allAgents');
  const [tids, setTids] = useState<string[]>(loop?.targets.ids ?? []);
  const [maxRuns, setMaxRuns] = useState<number | ''>(loop?.stop.maxRuns ?? 20);
  const [useRuns, setUseRuns] = useState(loop ? !!loop.stop.maxRuns : true);
  const [maxDur, setMaxDur] = useState<number>(loop?.stop.maxDurationSec ?? 7200);
  const [useDur, setUseDur] = useState(!!loop?.stop.maxDurationSec);
  const [stopText, setStopText] = useState(loop?.stop.outputPattern ?? '');
  const [goalKind, setGoalKind] = useState<'none' | LoopGoal['kind']>(loop?.stop.goal?.kind ?? 'none');
  const [goalText, setGoalText] = useState(loop?.stop.goal?.kind === 'agentJudged' ? loop.stop.goal.text : '');
  const [goalAgent, setGoalAgent] = useState(loop?.stop.goal?.kind === 'agentJudged' ? (loop.stop.goal.agent ?? 'claude') : 'claude');
  const [goalCmd, setGoalCmd] = useState(loop?.stop.goal?.kind === 'command' ? loop.stop.goal.command : '');
  const [goalTimeout, setGoalTimeout] = useState(loop?.stop.goal?.kind === 'command' ? (loop.stop.goal.timeoutSec ?? 300) : 300);
  const [enabled, setEnabled] = useState(loop?.enabled ?? true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    orchApi
      .loopTemplates()
      .then((t) => t.length && setTemplates(t))
      .catch(() => undefined);
  }, []);

  const applyTemplate = (t: LoopTemplate) => {
    setName(t.name);
    if (t.text) setText(t.text);
    if (t.trigger) {
      setKind(t.trigger.kind);
      if (t.trigger.kind === 'interval') setEvery(Math.round(t.trigger.everySec / 60));
      if (t.trigger.kind === 'cron') setCron(t.trigger.expr);
      if (t.trigger.kind === 'output') {
        setPattern(t.trigger.pattern);
        setRegex(!!t.trigger.regex);
      }
    }
    if (t.stop?.goal?.kind === 'agentJudged') {
      setGoalKind('agentJudged');
      setGoalText(t.stop.goal.text);
    } else setGoalKind('none');
  };

  const targetCount = tkind === 'allAgents' ? sessions.length : tkind === 'handles' ? tids.length : tkind === 'group' ? (groups.find((g) => g.id === tids[0]) ? readGroup(groups.find((g) => g.id === tids[0])!).members.length : 0) : sessions.length;
  const perHour = kind === 'interval' && every > 0 ? 60 / every : kind === 'cron' ? null : 30;
  const estimate =
    kind === 'interval'
      ? `About ${perHour! >= 1 ? Math.round(perHour!) : perHour!.toFixed(1)} prompt${perHour === 1 ? '' : 's'} per hour × ${targetCount} console${targetCount === 1 ? '' : 's'} = ${Math.round(perHour! * targetCount * 10) / 10} prompts/hour on your plans.`
      : kind === 'cron'
        ? `One prompt per console each time the schedule matches (${targetCount} console${targetCount === 1 ? '' : 's'}).`
        : `At most 30 prompts per hour per console (once every 2 minutes) × ${targetCount}.`;

  const problems = (): string => {
    if (!name.trim()) return 'Give the loop a name.';
    if (!text.trim()) return 'Write the instruction the agents receive each time.';
    if (kind === 'interval' && (every < 1 || every > 1440)) return 'Pick an interval between 1 minute and 24 hours.';
    if (kind === 'cron' && cron.trim().split(/\s+/).length !== 5) return 'A cron line has five fields: minute hour day month weekday.';
    if (kind === 'output' && pattern.trim().length < 2) return 'The output text needs at least 2 characters.';
    if ((tkind === 'handles' || tkind === 'group' || tkind === 'orchestrator') && !tids.length) return 'Pick who receives the instruction.';
    if (useRuns && (maxRuns === '' || maxRuns < 1 || maxRuns > 500)) return 'The run limit is 1–500.';
    if (goalKind === 'agentJudged' && !goalText.trim()) return 'Describe the goal the agent should check.';
    if (goalKind === 'command' && !goalCmd.trim()) return 'Enter the command that must pass.';
    return '';
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const p = problems();
    if (p) {
      setError(p);
      return;
    }
    const trigger: LoopTrigger =
      kind === 'interval' ? { kind, everySec: every * 60 } : kind === 'cron' ? { kind, expr: cron.trim() } : kind === 'output' ? { kind, pattern: pattern.trim(), regex } : { kind: 'agentIdle' };
    const goal: LoopGoal | undefined =
      goalKind === 'agentJudged' ? { kind: 'agentJudged', text: goalText.trim(), agent: goalAgent } : goalKind === 'command' ? { kind: 'command', command: goalCmd.trim(), timeoutSec: goalTimeout } : undefined;
    const body: LoopInput = {
      projectId: project.id,
      workspaceId: ctl.workspaceId,
      name: name.trim(),
      enabled,
      trigger,
      text: text.trim(),
      targets: { kind: tkind, ids: tkind === 'allAgents' ? [] : tids },
      stop: {
        ...(useRuns && maxRuns !== '' ? { maxRuns } : {}),
        ...(useDur ? { maxDurationSec: maxDur } : {}),
        ...(stopText.trim() ? { outputPattern: stopText.trim() } : {}),
        ...(goal ? { goal } : {}),
      },
    };
    setSaving(true);
    setError('');
    try {
      const saved = loop ? await orchApi.updateLoop(loop.id, body) : await orchApi.createLoop(project.id, body);
      orchStore.upsertLoop(saved);
      toast.success(loop ? `“${saved.name}” saved.` : `“${saved.name}” created${saved.state?.status === 'running' ? ' and running' : ''}.`);
      onDone();
    } catch (err) {
      setError(is404(err) ? 'This hub does not run loops yet.' : orchError(err));
    } finally {
      setSaving(false);
    }
  };

  const toggleId = (id: string) => setTids((cur) => (cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]));

  return (
    <form className="form loop-editor" onSubmit={submit} aria-label={loop ? `Edit ${loop.name}` : 'New loop'}>
      {!loop && (
        <div className="field">
          <span className="field-label">Start from a template</span>
          <div className="chip-row">
            {templates.map((t) => (
              <button key={t.name} type="button" className={`chip-btn ${name === t.name ? 'on' : ''}`} onClick={() => applyTemplate(t)} title={t.description ?? t.text}>
                {t.name}
              </button>
            ))}
          </div>
        </div>
      )}
      <TextField label="Name" value={name} onChange={(e) => setName(e.target.value)} maxLength={60} placeholder="Test guard" />
      <Field label="When" as="group">
        {({ labelId }) => (
          <SegmentedControl<TriggerKind>
            labelledBy={labelId}
            size="sm"
            block
            value={kind}
            onChange={setKind}
            options={[
              { value: 'interval', label: 'Schedule' },
              { value: 'cron', label: 'Cron' },
              { value: 'agentIdle', label: 'Agent finishes' },
              { value: 'output', label: 'Output shows' },
            ]}
          />
        )}
      </Field>
      {kind === 'interval' && (
        <div className="field">
          <span className="field-label">Every</span>
          <div className="chip-row">
            {PRESETS.map((m) => (
              <button key={m} type="button" className={`chip-btn ${every === m ? 'on' : ''}`} aria-pressed={every === m} onClick={() => setEvery(m)}>
                {m >= 60 ? `${m / 60} h` : `${m} min`}
              </button>
            ))}
            <input className="control control-sm loop-every" type="number" min={1} max={1440} value={every} onChange={(e) => setEvery(Number(e.target.value))} aria-label="Minutes between runs" />
            <span className="text-3 fs-xs">min</span>
          </div>
        </div>
      )}
      {kind === 'cron' && <TextField label="Cron line" mono value={cron} onChange={(e) => setCron(e.target.value)} hint="minute hour day month weekday, in the hub’s time zone" />}
      {kind === 'agentIdle' && <p className="fs-xs text-3">Runs when a target finishes its turn (needs live status), at most once every 2 minutes per console.</p>}
      {kind === 'output' && (
        <>
          <TextField label="Output contains" value={pattern} onChange={(e) => setPattern(e.target.value)} placeholder="FAIL" hint="Case-sensitive only when you type a capital letter. Sent once the console is free." />
          <Checkbox checked={regex} onChange={setRegex} label="Regular expression" />
        </>
      )}
      <Field label="Instruction" hint="Typed into each target exactly like an order.">
        {({ labelId: _l, ...a11y }) => <textarea className="control loop-textarea" rows={3} value={text} onChange={(e) => setText(e.target.value)} maxLength={4000} {...a11y} />}
      </Field>
      <Field label="Send to" as="group">
        {({ labelId }) => (
          <SegmentedControl<LoopTargets['kind']>
            labelledBy={labelId}
            size="sm"
            block
            value={tkind}
            onChange={(v) => {
              setTkind(v);
              setTids([]);
            }}
            options={[
              { value: 'allAgents', label: 'All agents' },
              { value: 'group', label: 'Group', disabled: !groups.length },
              { value: 'orchestrator', label: 'Card', disabled: !cards.length },
              { value: 'handles', label: 'Pick' },
            ]}
          />
        )}
      </Field>
      {tkind === 'group' && (
        <SelectField label="Group" value={tids[0] ?? ''} onChange={(e) => setTids(e.target.value ? [e.target.value] : [])}>
          <option value="">Choose a group</option>
          {groups.map((g) => (
            <option key={g.id} value={g.id}>
              {readGroup(g).name}
            </option>
          ))}
        </SelectField>
      )}
      {tkind === 'orchestrator' && (
        <SelectField label="Orchestrator card" value={tids[0] ?? ''} onChange={(e) => setTids(e.target.value ? [e.target.value] : [])}>
          <option value="">Choose a card</option>
          {cards.map((c) => (
            <option key={c.id} value={c.id}>
              {readOrchestrator(c).title}
            </option>
          ))}
        </SelectField>
      )}
      {tkind === 'handles' && (
        <div className="pick-list" role="group" aria-label="Consoles">
          {sessions.length === 0 && <p className="fs-xs text-3">No agent consoles on this board.</p>}
          {sessions.map((s) => (
            <Checkbox key={s.id} checked={tids.includes(s.id)} onChange={() => toggleId(s.id)} label={`@${s.handle ?? s.name} · ${agentById(s.agent || s.preset)?.name ?? s.agent}`} />
          ))}
        </div>
      )}
      <fieldset className="form-section">
        <legend className="section-title">Stop when</legend>
        <div className="loop-stop">
          <Checkbox checked={useRuns} onChange={setUseRuns} label="It has run" />
          <input className="control control-sm loop-num" type="number" min={1} max={500} value={maxRuns} disabled={!useRuns} onChange={(e) => setMaxRuns(e.target.value === '' ? '' : Number(e.target.value))} aria-label="Maximum runs" />
          <span className="fs-xs text-3">times</span>
        </div>
        <div className="loop-stop">
          <Checkbox checked={useDur} onChange={setUseDur} label="Time is up after" />
          <select className="control control-sm loop-dur" value={maxDur} disabled={!useDur} onChange={(e) => setMaxDur(Number(e.target.value))} aria-label="Time limit">
            {DURATIONS.map((d) => (
              <option key={d.v} value={d.v}>
                {d.l}
              </option>
            ))}
          </select>
        </div>
        <TextField label="An agent’s output shows" optional value={stopText} onChange={(e) => setStopText(e.target.value)} placeholder="All tests passed" />
        <Field label="Goal" as="group">
          {({ labelId }) => (
            <SegmentedControl<'none' | LoopGoal['kind']>
              labelledBy={labelId}
              size="sm"
              block
              value={goalKind}
              onChange={setGoalKind}
              options={[
                { value: 'none', label: 'None' },
                { value: 'agentJudged', label: 'Agent judges' },
                { value: 'command', label: 'Command passes' },
              ]}
            />
          )}
        </Field>
        {goalKind === 'agentJudged' && (
          <>
            <TextField label="Goal to check" value={goalText} onChange={(e) => setGoalText(e.target.value)} placeholder="All tests pass and the build is green" hint="A hidden Claude Code or Codex run reads the last reply and decides. It uses your plan." />
            <SelectField label="Judge" value={goalAgent} onChange={(e) => setGoalAgent(e.target.value)}>
              <option value="claude">Claude Code</option>
              <option value="codex">Codex</option>
            </SelectField>
          </>
        )}
        {goalKind === 'command' && (
          <>
            <TextField label="Command" mono value={goalCmd} onChange={(e) => setGoalCmd(e.target.value)} placeholder="npm test" hint={`Runs in ${project.path} on the project machine; exit code 0 means the goal is met.`} />
            <TextField label="Timeout (seconds)" type="number" min={10} max={3600} value={goalTimeout} onChange={(e) => setGoalTimeout(Number(e.target.value))} />
          </>
        )}
      </fieldset>
      <p className="loop-estimate fs-xs">
        <Icon name="info" /> {estimate}
      </p>
      <Checkbox checked={enabled} onChange={setEnabled} label="Start right away" />
      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
      <div className="row-end">
        <Button variant="secondary" size="sm" onClick={onDone} disabled={saving}>
          Cancel
        </Button>
        <Button type="submit" variant="primary" size="sm" icon={loop ? 'check' : 'plus'} loading={saving}>
          {loop ? 'Save loop' : 'Create loop'}
        </Button>
      </div>
    </form>
  );
}
