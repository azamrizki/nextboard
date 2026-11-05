// The Tasks tab of the bottom panel: what the project's files say can be run, with its output,
// where it failed, and a way to hand a failure to an agent (docs/API-studio.md §7.12).

import { useEffect, useMemo, useRef, useState } from 'react';
import { errorMessage } from '../../api';
import { Button } from '../../components/Button';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { jobApi, taskApi } from '../api';
import { useStudio, waitJob } from '../store';
import type { ProjectTask } from '../types';
import { parseTaskOutput, stripAnsi } from './taskOutput';
import type { Workspace } from './workspace';

/** Longer than the hub lets a task run. */
const WAIT_MS = 31 * 60_000;
const SHOWN = 200 << 10;
const FOR_AGENT = 6 << 10;

export async function runTask(ws: Workspace, task: ProjectTask) {
  ws.panelOpen = true;
  ws.panelView = 'tasks';
  ws.taskSelected = task.id;
  try {
    const job = await taskApi.run(ws.gitTarget, task.id);
    const run = { jobId: job.id, state: 'running', error: '' };
    ws.taskRuns.set(task.id, run);
    ws.changed();
    const done = await waitJob(job.id, WAIT_MS);
    run.state = done.state;
    run.error = done.error ?? '';
  } catch (err) {
    const run = ws.taskRuns.get(task.id);
    if (run) run.state = 'failed';
    toast.error(`Could not run it: ${errorMessage(err)}`);
  }
  ws.changed();
}

function StateDot({ state }: { state?: string }) {
  if (state === 'running') return <span className="spinner wb-task-spin" aria-label="Running" />;
  if (state === 'done') return <Icon name="check" className="wb-task-ok" aria-label="Passed" />;
  if (state === 'failed') return <Icon name="close" className="wb-task-bad" aria-label="Failed" />;
  if (state === 'cancelled') return <Icon name="minus" className="wb-task-off" aria-label="Stopped" />;
  return <span className="wb-task-none" aria-hidden="true" />;
}

export function TasksPanel({ ws }: { ws: Workspace }) {
  const [tasks, setTasks] = useState<ProjectTask[] | null>(null);
  const [error, setError] = useState('');
  const target = ws.gitTarget;
  const outRef = useRef<HTMLPreElement>(null);

  const load = () => {
    setError('');
    taskApi
      .list(target)
      .then((list) => {
        setTasks(list);
        ws.tasks = list;
      })
      .catch((err) => {
        setTasks([]);
        setError(errorMessage(err));
      });
  };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(load, [ws, target.repo]);

  const selected = tasks?.find((t) => t.id === ws.taskSelected) ?? null;
  const run = selected ? ws.taskRuns.get(selected.id) : undefined;
  const output = useStudio((s) => (run ? s.output[run.jobId] : undefined));
  const text = useMemo(() => {
    const t = stripAnsi(output ?? '');
    return t.length > SHOWN ? t.slice(-SHOWN) : t;
  }, [output]);
  const parsed = useMemo(() => parseTaskOutput(text), [text]);

  useEffect(() => {
    const el = outRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [text]);

  const openProblem = (file: string, line: number, col: number) => {
    const dir = selected?.cwd ? `/${selected.cwd}` : '';
    const base = ws.git?.root?.replace(/\/+$/, '') ?? ws.root;
    ws.open(file.startsWith('/') ? file : `${base}${dir}/${file.replace(/^\.\//, '')}`, { pin: true, source: true, line, col });
  };

  const groups = useMemo(() => {
    const m = new Map<string, ProjectTask[]>();
    for (const t of tasks ?? []) m.set(t.group, [...(m.get(t.group) ?? []), t]);
    return [...m.entries()];
  }, [tasks]);

  return (
    <div className="wb-tasks nowheel">
      <div className="wb-tasks-list">
        <div className="wb-section-title">
          <span>Tasks</span>
          <button type="button" className="wb-section-act" title="Read the project's files again" onClick={load}>
            Refresh
          </button>
        </div>
        {tasks === null && <p className="wb-panel-msg">Reading the project…</p>}
        {error && <p className="wb-panel-msg">{error}</p>}
        {tasks && !tasks.length && !error && <p className="wb-panel-msg">Nothing to run was found: no package.json scripts, Makefile, go.mod, Cargo.toml or pytest setup.</p>}
        {groups.map(([group, list]) => (
          <div key={group}>
            <div className="wb-tasks-group truncate">{group}</div>
            {list.map((t) => {
              const r = ws.taskRuns.get(t.id);
              return (
                <div key={t.id} className={`wb-row wb-task ${ws.taskSelected === t.id ? 'is-selected' : ''}`} title={t.command} onClick={() => { ws.taskSelected = t.id; ws.changed(); }}>
                  <StateDot state={r?.state} />
                  <span className="wb-row-name truncate">{t.name}</span>
                  {t.kind === 'test' && <span className="wb-task-kind">test</span>}
                  {r?.state === 'running' ? (
                    <button
                      type="button"
                      className="wb-result-act is-shown"
                      aria-label={`Stop ${t.name}`}
                      title="Stop"
                      onClick={(e) => {
                        e.stopPropagation();
                        void jobApi.cancel(r.jobId).catch((err) => toast.error(errorMessage(err)));
                      }}
                    >
                      <Icon name="close" />
                    </button>
                  ) : (
                    <button
                      type="button"
                      className="wb-result-act is-shown"
                      aria-label={`Run ${t.name}`}
                      title="Run"
                      onClick={(e) => {
                        e.stopPropagation();
                        void runTask(ws, t);
                      }}
                    >
                      <Icon name="play" />
                    </button>
                  )}
                </div>
              );
            })}
          </div>
        ))}
      </div>
      <div className="wb-tasks-out">
        {!selected && <p className="wb-panel-msg">Pick a task and press its run button.</p>}
        {selected && (
          <div className="wb-tasks-head">
            <StateDot state={run?.state} />
            <span className="mono truncate" title={selected.command}>
              {selected.command}
            </span>
            {run?.state === 'failed' && run.error && <span className="wb-task-bad truncate">{run.error}</span>}
            <span className="spacer" />
            {run?.state === 'failed' && (
              <Button
                size="sm"
                variant="ghost"
                icon="sparkle"
                onClick={() => ws.actions.ask?.(`The task \`${selected.command}\` failed. Find the cause and fix it.\n\n\`\`\`\n${text.slice(-FOR_AGENT)}\n\`\`\``)}
              >
                Ask an agent to fix
              </Button>
            )}
          </div>
        )}
        {(parsed.failed.length > 0 || parsed.problems.length > 0) && (
          <div className="wb-tasks-problems">
            {parsed.failed.map((f) => (
              <div key={f} className="wb-task-failed truncate" title={f}>
                <Icon name="close" className="wb-task-bad" /> {f}
              </div>
            ))}
            {parsed.problems.map((p) => (
              <button key={`${p.file}:${p.line}:${p.col}`} type="button" className="wb-row wb-task-problem" title={p.message} onClick={() => openProblem(p.file, p.line, p.col)}>
                <span className="wb-task-where mono truncate">
                  {p.file}:{p.line}
                </span>
                {p.message && <span className="wb-result-dir truncate">{p.message}</span>}
              </button>
            ))}
          </div>
        )}
        {run && (
          <pre className="wb-tasks-text mono" ref={outRef}>
            {text}
          </pre>
        )}
      </div>
    </div>
  );
}
