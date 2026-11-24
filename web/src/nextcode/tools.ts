// How the chat names a tool call: its icon, its main argument and a short note on its result,
// read from the tool's input and output as Claude Code gives them. Unknown tools (MCP servers,
// newer CLIs) fall back to their first short text argument.

import type { IconName } from '../components/Icon';
import { NEXTCODE_AGENTS_TOOL, SUB_AGENT_TOOLS } from './reducer';
import type { Tool } from './types';

export type Input = Record<string, unknown>;

export const inputOf = (v: unknown): Input => (v && typeof v === 'object' && !Array.isArray(v) ? (v as Input) : {});
const str = (v: unknown) => (typeof v === 'string' ? v : '');
const firstLine = (s: string) => {
  const i = s.indexOf('\n');
  return i < 0 ? s : `${s.slice(0, i)} …`;
};

/** What a tool's card looks like: a row, a diff, a command, a sub-agent's group, a list. */
export type ToolLook = 'row' | 'diff' | 'bash' | 'agent' | 'todo';

export function toolLook(name: string): ToolLook {
  if (name === 'Edit' || name === 'Write' || name === 'MultiEdit') return 'diff';
  if (name === 'Bash') return 'bash';
  if (SUB_AGENT_TOOLS.includes(name)) return 'agent';
  if (name === 'TodoWrite') return 'todo';
  return 'row';
}

const ICONS: Record<string, IconName> = {
  Read: 'fileText',
  Write: 'file',
  Edit: 'pencil',
  MultiEdit: 'pencil',
  NotebookEdit: 'pencil',
  NotebookRead: 'fileText',
  Bash: 'terminal',
  BashOutput: 'terminal',
  KillShell: 'terminal',
  KillBash: 'terminal',
  Grep: 'search',
  Glob: 'folder',
  LS: 'folder',
  WebFetch: 'globe',
  WebSearch: 'globe',
  Task: 'users',
  Agent: 'users',
  TodoWrite: 'checklist',
  ExitPlanMode: 'checklist',
  AskUserQuestion: 'message',
  Skill: 'sparkle',
  SlashCommand: 'sparkle',
};

export function toolIcon(name: string): IconName {
  if (name === NEXTCODE_AGENTS_TOOL) return 'users';
  if (name.startsWith('mcp__')) return 'plug';
  return ICONS[name] ?? 'bolt';
}

/** "mcp__github__create_issue" → "github · create_issue"; any other name as it is. */
export function toolLabel(name: string): string {
  if (name === NEXTCODE_AGENTS_TOOL) return 'Sub-agents';
  if (!name.startsWith('mcp__')) return name;
  const [, server = '', ...rest] = name.split('__');
  return rest.length ? `${server} · ${rest.join('__')}` : server;
}

/** A long path cut to its last few parts: "…/api/signup.ts". */
export function shortPath(p: string, keep = 3): string {
  const parts = p.split('/').filter(Boolean);
  if (parts.length <= keep) return p;
  return `…/${parts.slice(-keep).join('/')}`;
}

export const baseName = (p: string) => p.split('/').filter(Boolean).pop() ?? p;

/** The file a file tool works on. */
export function toolPath(name: string, input: unknown): string {
  const o = inputOf(input);
  return str(o.file_path) || str(o.notebook_path) || (name === 'LS' ? str(o.path) : '');
}

/** The main argument of a call, as one line (a path cut to its end when `compact`). */
export function toolArg(name: string, input: unknown, compact = false): string {
  const o = inputOf(input);
  const path = toolPath(name, input);
  if (path) {
    const shown = compact ? baseName(path) : shortPath(path, 4);
    if (name === 'Read' && typeof o.offset === 'number') {
      const from = o.offset;
      const to = typeof o.limit === 'number' ? from + o.limit - 1 : 0;
      return `${shown}  ${to ? `lines ${from}–${to}` : `from line ${from}`}`;
    }
    return shown;
  }
  switch (name) {
    case 'Bash':
      return firstLine(str(o.command));
    case 'Grep': {
      const where = str(o.path) || str(o.glob) || str(o.type);
      return [str(o.pattern), where && (compact ? baseName(where) : shortPath(where))].filter(Boolean).join('  ');
    }
    case 'Glob':
      return [str(o.pattern), !compact && str(o.path) ? shortPath(str(o.path)) : ''].filter(Boolean).join('  ');
    case 'WebFetch':
      return str(o.url);
    case 'WebSearch':
      return str(o.query);
    case 'Task':
    case 'Agent':
      return str(o.description) || firstLine(str(o.prompt));
    case NEXTCODE_AGENTS_TOOL: {
      // "3 tasks · API routes, web pages, tests"
      const tasks = Array.isArray(o.tasks) ? o.tasks.map((t) => str(inputOf(t).description)).filter(Boolean) : [];
      const n = Array.isArray(o.tasks) ? o.tasks.length : 1;
      return [`${n} ${n === 1 ? 'task' : 'tasks'}`, tasks.join(', ')].filter(Boolean).join(' · ');
    }
    case 'TodoWrite': {
      const n = Array.isArray(o.todos) ? o.todos.length : 0;
      return `${n} ${n === 1 ? 'to-do' : 'to-dos'}`;
    }
    case 'AskUserQuestion': {
      const q = Array.isArray(o.questions) ? inputOf(o.questions[0]) : {};
      return str(q.question);
    }
    case 'Skill':
      return str(o.skill) || str(o.command);
    case 'SlashCommand':
      return str(o.command);
    case 'BashOutput':
    case 'KillShell':
    case 'KillBash':
      return str(o.bash_id) || str(o.shell_id);
    default:
      break;
  }
  for (const k of ['command', 'path', 'pattern', 'url', 'query', 'description', 'prompt', 'name', 'title']) {
    if (str(o[k])) return firstLine(str(o[k]));
  }
  for (const v of Object.values(o)) if (typeof v === 'string' && v && v.length <= 200) return firstLine(v);
  return '';
}

const lineCount = (s: string) => (s ? s.replace(/\n+$/, '').split('\n').length : 0);

/** A short note on what a finished call gave: "120 lines", "4 files". */
export function toolMeta(t: Tool): string {
  if (t.status !== 'done') return '';
  const out = t.output ?? '';
  switch (t.name) {
    case 'Read': {
      const n = lineCount(out);
      return n ? `${n} ${n === 1 ? 'line' : 'lines'}` : '';
    }
    case 'Grep':
    case 'Glob':
    case 'LS': {
      const found = /^Found (\d+ \w+)/.exec(out);
      if (found) return found[1];
      if (/^No (files|matches) found/.test(out)) return 'none';
      const n = lineCount(out);
      return n ? `${n} ${n === 1 ? 'result' : 'results'}` : '';
    }
    default:
      return '';
  }
}

/** The last lines of a command's output. */
export function tail(s: string, lines: number): { text: string; cut: number } {
  const all = s.replace(/\n+$/, '').split('\n');
  if (all.length <= lines) return { text: all.join('\n'), cut: 0 };
  return { text: all.slice(-lines).join('\n'), cut: all.length - lines };
}

export interface Todo {
  content: string;
  status: string;
  activeForm?: string;
}

export function todosOf(input: unknown): Todo[] {
  const o = inputOf(input);
  if (!Array.isArray(o.todos)) return [];
  return o.todos.map((t) => inputOf(t)).map((t) => ({ content: str(t.content), status: str(t.status), activeForm: str(t.activeForm) || undefined }));
}

/** An input as readable text for the details of an unknown tool. */
export function inputText(input: unknown): string {
  if (input === undefined || input === null) return '';
  if (typeof input === 'string') return input;
  try {
    return JSON.stringify(input, null, 2);
  } catch {
    return String(input);
  }
}

export interface Question {
  question: string;
  header: string;
  multiSelect: boolean;
  options: { label: string; description: string }[];
}

/** AskUserQuestion's questions. */
export function questionsOf(input: unknown): Question[] {
  const o = inputOf(input);
  if (!Array.isArray(o.questions)) return [];
  return o.questions
    .map((q) => inputOf(q))
    .filter((q) => str(q.question))
    .map((q) => ({
      question: str(q.question),
      header: str(q.header),
      multiSelect: q.multiSelect === true,
      options: (Array.isArray(q.options) ? q.options : []).map((x) => inputOf(x)).filter((x) => str(x.label)).map((x) => ({ label: str(x.label), description: str(x.description) })),
    }));
}

/** ExitPlanMode's plan. */
export const planOf = (input: unknown) => str(inputOf(input).plan);

/** One line of the owner's answer: the question's header (or the question), and what was chosen. */
export interface AnswerLine {
  /** The question's header, else its text. */
  label: string;
  /** label is a header (short, shown in capitals), not the question's text. */
  header: boolean;
  /** What was chosen; own is the owner's words, not one of the options. */
  values: { text: string; own: boolean }[];
}

/** The owner's answer as lines, in the order the questions were asked (answers to questions the input does not hold last). */
export function answerLines(input: unknown, answers: Record<string, string[]> | undefined): AnswerLine[] {
  const qs = questionsOf(input);
  const got = answers ?? {};
  const lines: AnswerLine[] = [];
  const seen = new Set<string>();
  for (const q of qs) {
    const labels = got[q.question];
    if (!labels?.length || seen.has(q.question)) continue;
    seen.add(q.question);
    const options = new Set(q.options.map((o) => o.label));
    lines.push({ label: q.header || q.question, header: !!q.header, values: labels.map((t) => ({ text: t, own: !options.has(t) })) });
  }
  for (const [q, labels] of Object.entries(got).sort(([a], [b]) => a.localeCompare(b))) {
    if (!seen.has(q) && labels?.length) lines.push({ label: q, header: false, values: labels.map((t) => ({ text: t, own: false })) });
  }
  return lines;
}

/** When the owner answered: the time today ("10:42"), else the day too. */
export function answerTime(at: string, now = new Date()): string {
  const d = new Date(at);
  if (Number.isNaN(d.getTime())) return '';
  const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
  if (d.toDateString() === now.toDateString()) return time;
  const day = d.toLocaleDateString(undefined, { month: 'short', day: 'numeric', ...(d.getFullYear() !== now.getFullYear() ? { year: 'numeric' as const } : {}) });
  return `${day}, ${time}`;
}
