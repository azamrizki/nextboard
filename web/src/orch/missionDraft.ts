// A mission the owner writes by hand on a mission tile: the form's draft, the checks the hub
// makes too (so the form says what is wrong before sending), and the request it becomes. Pure.

import type { MissionInput, MissionRun } from './types';

/** The hub's limits (internal/orch/missions.go). */
export const MISSION_LIMITS = { goal: 2000, doneWhen: 1000, doc: 32 << 10, tasks: 40, title: 200, text: 8000 } as const;

export interface TaskDraft {
  /** "t1", "t2", …: stable while the form is open, renumbered on send. */
  id: string;
  /** The console's handle, without "@". */
  handle: string;
  title: string;
  text: string;
  dependsOn: string[];
}

export interface MissionDraft {
  goal: string;
  doneWhen: string;
  doc: string;
  tasks: TaskDraft[];
  /** The lead reads the plan and may change it before the owner approves; else it starts as written. */
  review: boolean;
}

export function emptyDraft(handles: string[] = []): MissionDraft {
  return { goal: '', doneWhen: '', doc: '', tasks: [newTask([], handles[0] ?? '')], review: false };
}

/** A new task row: the next free id, the given console. */
export function newTask(tasks: TaskDraft[], handle: string): TaskDraft {
  const n = tasks.reduce((max, t) => Math.max(max, Number(t.id.slice(1)) || 0), 0) + 1;
  return { id: `t${n}`, handle, title: '', text: '', dependsOn: [] };
}

/** Remove a task and every dependency on it. */
export function removeTask(tasks: TaskDraft[], id: string): TaskDraft[] {
  return tasks.filter((t) => t.id !== id).map((t) => (t.dependsOn.includes(id) ? { ...t, dependsOn: t.dependsOn.filter((d) => d !== id) } : t));
}

/** Whether `id` (directly or through others) already waits for `on`: adding on → id would close a circle. */
export function dependsOnTransitively(tasks: TaskDraft[], id: string, on: string): boolean {
  const byId = new Map(tasks.map((t) => [t.id, t]));
  const seen = new Set<string>();
  const walk = (cur: string): boolean => {
    if (cur === on) return true;
    if (seen.has(cur)) return false;
    seen.add(cur);
    return (byId.get(cur)?.dependsOn ?? []).some(walk);
  };
  return walk(id);
}

/** The tasks `id` may wait for: every other task that does not already wait for `id`. */
export function dependencyChoices(tasks: TaskDraft[], id: string): string[] {
  return tasks.filter((t) => t.id !== id && !dependsOnTransitively(tasks, t.id, id)).map((t) => t.id);
}

/** What is wrong with the draft, the first problem only; '' when it can be sent. */
export function draftProblem(d: MissionDraft, handles: string[]): string {
  const L = MISSION_LIMITS;
  if (!d.goal.trim()) return 'Write the goal.';
  if (d.goal.length > L.goal) return `The goal is longer than ${L.goal} characters.`;
  if (d.doneWhen.length > L.doneWhen) return `"Done when" is longer than ${L.doneWhen} characters.`;
  if (new Blob([d.doc]).size > L.doc) return 'The shared brief is larger than 32 KB.';
  if (!d.tasks.length) return 'Add at least one task.';
  if (d.tasks.length > L.tasks) return `At most ${L.tasks} tasks.`;
  const known = new Set(handles.map((h) => h.toLowerCase()));
  for (const t of d.tasks) {
    if (!t.handle) return `Pick a console for ${t.id}.`;
    if (!known.has(t.handle.toLowerCase())) return `@${t.handle} (${t.id}) is not wired to this tile.`;
    if (!t.text.trim()) return `Write what ${t.id} should do.`;
    if (t.title.length > L.title || t.text.length > L.text) return `${t.id}: title ≤ ${L.title}, instruction ≤ ${L.text} characters.`;
  }
  return '';
}

/** The request: tasks renumbered t1… in their order, a title from the instruction when none was written. */
export function missionInput(d: MissionDraft, threadId: string, workspaceId: string): MissionInput & { workspaceId: string } {
  const ids = new Map(d.tasks.map((t, i) => [t.id, `t${i + 1}`]));
  return {
    goal: d.goal.trim(),
    ...(d.doneWhen.trim() ? { doneWhen: d.doneWhen.trim() } : {}),
    ...(d.doc.trim() ? { doc: d.doc.trim() } : {}),
    tasks: d.tasks.map((t) => ({
      id: ids.get(t.id)!,
      handle: t.handle,
      title: t.title.trim() || titleOf(t.text),
      text: t.text.trim(),
      ...(t.dependsOn.length ? { dependsOn: t.dependsOn.map((x) => ids.get(x)).filter((x): x is string => !!x) } : {}),
    })),
    threadId,
    cardId: threadId,
    workspaceId,
  };
}

/** A short title: the instruction's first line, cut at a word near 60 characters. */
export function titleOf(text: string): string {
  const line = text.trim().split('\n')[0].trim();
  if (line.length <= 60) return line;
  const cut = line.slice(0, 60);
  const sp = cut.lastIndexOf(' ');
  return `${(sp > 30 ? cut.slice(0, sp) : cut).trimEnd()}…`;
}

/** A finished mission as a new draft ("Run again"); consoles no longer wired keep their handle for the owner to change. */
export function draftFrom(m: MissionRun): MissionDraft {
  const tasks = (m.tasks ?? []).map((t) => ({ id: t.id, handle: t.handle.replace(/^@/, ''), title: t.title, text: t.text, dependsOn: [...(t.dependsOn ?? [])] }));
  return { goal: m.goal, doneWhen: m.doneWhen ?? '', doc: m.doc ?? '', tasks: tasks.length ? tasks : [newTask([], '')], review: false };
}

/** The message that asks the lead to look over a plan the owner wrote, before it is approved. */
export const REVIEW_ASK =
  'I wrote this mission plan myself. Review it before it starts: fill gaps, split or merge tasks, fix their order, and propose it again. Keep the goal.';
