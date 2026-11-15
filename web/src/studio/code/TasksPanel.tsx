// The Tasks tab of the bottom panel: what the project's files say can be run, with its output,
// where it failed, and a way to hand a failure to an agent (docs/API-studio.md §7.12). Each part
// has its own tasks, run in that part; a Root card lists them under each part's name.

import { useEffect, useMemo, useRef, useState } from 'react';
import { errorMessage } from '../../api';
import { Button } from '../../components/Button';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { jobApi, taskApi } from '../api';
import { useStudio, waitJob } from '../store';
import type { ProjectTask } from '../types';
import { taskFile, taskKey } from './partGroups';
import type { PartService } from './partsvc';
import { cleanAbs } from './paths';
import { parseTaskOutput, stripAnsiTail, type AnsiTail } from './taskOutput';
import { useWs } from './useWs';
import type { Workspace } from './workspace';

/** Longer than the hub lets a task run. */
const WAIT_MS = 31 * 60_000;
const SHOWN = 200 << 10;
const FOR_AGENT = 6 << 10;

/** Run a part's task: in that part, its output in the card's Tasks tab. */
export async function runTask(ws: Workspace, part: PartService, task: ProjectTask) {
  const id = taskKey(part.partId, task.id);
  ws.panelOpen = true;
  ws.panelView = 'tasks';
  ws.taskSelected = id;
  try {
    const job = await taskApi.run(part.gitTarget, task.id);
    const run = { jobId: job.id, state: 'running', error: '' };
    ws.taskRuns.set(id, run);
    ws.changed();
    const done = await waitJob(job.id, WAIT_MS);
    run.state = done.state;
    run.error = done.error ?? '';
  } catch (err) {
    const run = ws.taskRuns.get(id);
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

interface PartTasks {
  part: PartService;
  tasks: ProjectTask[];
  error: string;
}

export function TasksPanel({ ws }: { ws: Workspace }) {
  useWs(ws);
  const [lists, setLists] = useState<PartTasks[] | null>(null);
  const outRef = useRef<HTMLPreElement>(null);
  const multi = ws.paths.multi;
  // each part's selected repository decides where its tasks are read
  const repos = ws.parts.map((p) => `${p.partId}:${p.gitTarget.repo}`).join('\n');

  const load = () => {
    void Promise.all(
      ws.parts.map((part) =>
        taskApi.list(part.gitTarget).then(
          (tasks): PartTasks => {
            part.tasks = tasks;
            return { part, tasks, error: '' };
          },
          (err): PartTasks => ({ part, tasks: [], error: errorMessage(err) }),
        ),
      ),
    ).then(setLists);
  };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(load, [ws, repos]);

  const found = lists?.flatMap((l) => l.tasks.map((task) => ({ part: l.part, task }))).find((x) => taskKey(x.part.partId, x.task.id) === ws.taskSelected) ?? null;
  const selected = found?.task ?? null;
  const run = found ? ws.taskRuns.get(taskKey(found.part.partId, found.task.id)) : undefined;
  const output = useStudio((s) => (run ? s.output[run.jobId] : undefined));
  const tail = useRef<AnsiTail | null>(null);
  const jobId = run?.jobId ?? '';
  const text = useMemo(() => (tail.current = stripAnsiTail(tail.current, jobId, output ?? '', SHOWN)).clean, [jobId, output]);
  const parsed = useMemo(() => parseTaskOutput(text), [text]);

  useEffect(() => {
    const el = outRef.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, [text]);

  /** A file the output names: on the task's part's machine, against its folder (the part's git root, else its folder). */
  const openProblem = (file: string, line: number, col: number) => {
    if (!found) return;
    const { part, task } = found;
    const k = taskFile(part.machineId, cleanAbs(part.git?.root ?? '') ?? part.root, task.cwd, file);
    if (k) ws.open(k, { pin: true, source: true, line, col });
  };

  const sections = useMemo(
    () =>
      (lists ?? []).map((l) => {
        const m = new Map<string, ProjectTask[]>();
        for (const t of l.tasks) m.set(t.group, [...(m.get(t.group) ?? []), t]);
        return { ...l, groups: [...m.entries()] };
      }),
    [lists],
  );
  const none = !!lists && lists.every((l) => !l.tasks.length && !l.error);

  return (
    <div className="wb-tasks nowheel">
      <div className="wb-tasks-list">
        <div className="wb-section-title">
          <span>Tasks</span>
          <button type="button" className="wb-section-act" title="Read the project's files again" onClick={load}>
            Refresh
          </button>
        </div>
        {lists === null && <p className="wb-panel-msg">Reading the project…</p>}
        {none && <p className="wb-panel-msg">Nothing to run was found: no package.json scripts, Makefile, go.mod, Cargo.toml or pytest setup.</p>}
        {sections.map(({ part, groups, error, tasks }) => (
          <div key={part.partId}>
            {multi && (
              <div className="wb-section-title wb-part-head" title={`${part.name}: ${part.root}`}>
                <Icon name="folder" className="wb-icon" />
                <span className="truncate">{part.name}</span>
                <span className="spacer" />
                <span className="wb-result-count tabular">{tasks.length}</span>
              </div>
            )}
            {error && <p className="wb-panel-msg">{error}</p>}
            {multi && !error && !tasks.length && !none && <p className="wb-panel-msg">Nothing to run here.</p>}
            {groups.map(([group, list]) => (
              <div key={group}>
                <div className="wb-tasks-group truncate">{group}</div>
                {list.map((t) => {
                  const id = taskKey(part.partId, t.id);
                  const r = ws.taskRuns.get(id);
                  return (
                    <div key={t.id} className={`wb-row wb-task ${ws.taskSelected === id ? 'is-selected' : ''}`} title={t.command} onClick={() => { ws.taskSelected = id; ws.changed(); }}>
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
                            void runTask(ws, part, t);
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
