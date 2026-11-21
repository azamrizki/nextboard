// The entries of a NextCode chat: the owner's messages, the agent's Markdown, folded thinking,
// tool calls (a row for small tools, a diff for file edits, the command and the end of its output
// for Bash, a group for a sub-agent's work), errors and notices, and what is being streamed.

import { createContext, memo, useContext, useMemo, useState, type ReactNode } from 'react';
import { useMarkdown } from '../board/lazy';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { formatDateTime } from '../lib/format';
import { navigate } from '../lib/router';
import { splitAttachedLine } from './attach';
import { SentFiles } from './ChatAttachments';
import { hunks, toolDiff, type Diff } from './diff';
import { isLoginError, loginHelp } from './format';
import type { Stream } from './reducer';
import { splitBlocks, useSmoothText } from './smooth';
import { inputText, tail, todosOf, toolArg, toolIcon, toolLabel, toolLook, toolMeta, toolPath } from './tools';
import type { Item, Tool } from './types';

/**
 * The sub-agents' items and open streams by their tool call (reducer.ts groupItems/groupStreams).
 * Only a sub-agent's group reads them, so the other entries are not drawn again while a reply
 * streams.
 */
export const ChildItems = createContext<Map<string, Item[]>>(new Map());
export const ChildStreams = createContext<Map<string, Stream[]>>(new Map());

/**
 * What the console runs on, for what an error says: its engine ("claude" | "codex" | "api") and
 * whether an API provider runs it instead of a login (always so on the API engine).
 */
export const ChatAccount = createContext<{ engine: string; onProvider: boolean; signIn?: () => void }>({ engine: '', onProvider: false });
const NO_ITEMS: Item[] = [];
const NO_STREAMS: Stream[] = [];

/** The caret goes inside the last element, at the end of the text. */
function withCaret(html: string): string {
  const caret = '<span class="nc-caret" aria-hidden="true"></span>';
  const m = /((?:<\/[a-z0-9]+>\s*)+)$/i.exec(html);
  return m ? html.slice(0, m.index) + caret + m[1] : html + caret;
}

/** The agent's Markdown (rendered once the renderer has loaded; the text alone until then). */
export function Markdown({ text, caret = false, className = '' }: { text: string; caret?: boolean; className?: string }) {
  const md = useMarkdown();
  const html = useMemo(() => {
    if (!md) return '';
    const out = md.renderMarkdown(text);
    return caret ? withCaret(out) : out;
  }, [md, text, caret]);
  if (!md) {
    return (
      <div className={`nc-md is-plain ${className}`}>
        {text}
        {caret && <span className="nc-caret" aria-hidden="true" />}
      </div>
    );
  }
  // eslint-disable-next-line react/no-danger -- sanitised by renderMarkdown
  return <div className={`nc-md ${className}`} dangerouslySetInnerHTML={{ __html: html }} />;
}

/** One block of a reply being written (smooth.ts splitBlocks): drawn again only when it changes. */
const MdBlock = memo(function MdBlock({ src, caret }: { src: string; caret: boolean }) {
  const md = useMarkdown();
  const html = useMemo(() => {
    if (!md) return '';
    const out = md.renderMarkdown(src);
    return caret ? withCaret(out) : out;
  }, [md, src, caret]);
  // eslint-disable-next-line react/no-danger -- sanitised by renderMarkdown
  return <div className="nc-md-block" dangerouslySetInnerHTML={{ __html: html }} />;
});

/**
 * Markdown that is still being written: the blocks that are done keep what they were drawn as,
 * and only the last one is rendered again as text comes, however long the reply grows.
 */
function GrowingMarkdown({ text, caret, className = '' }: { text: string; caret: boolean; className?: string }) {
  const md = useMarkdown();
  const { done, tail } = useMemo(() => splitBlocks(text), [text]);
  if (!md) return <Markdown text={text} caret={caret} className={className} />;
  return (
    <div className={`nc-md ${className}`}>
      {done.map((src, i) => (
        <MdBlock key={i} src={src} caret={false} />
      ))}
      <MdBlock key={done.length} src={tail} caret={caret} />
    </div>
  );
}

/**
 * The agent's reply when it is complete. Its streamed block may not all be shown yet (smooth.ts):
 * the rest is shown on from there, then the whole reply is rendered as one.
 */
function ReplyText({ item, className }: { item: Item; className: string }) {
  const text = item.text ?? '';
  const shown = useSmoothText(text, false, item.stream);
  if (shown.length < text.length) return <GrowingMarkdown text={shown} caret className={className} />;
  return <Markdown text={text} className={className} />;
}

/** The mark of NextCode: NextBoard's monochrome "N›" with the agent's violet dot. */
export function NcMark({ small = false }: { small?: boolean }) {
  return (
    <span className={`nc-mark ${small ? 'is-sm' : ''}`} aria-hidden="true">
      N›
    </span>
  );
}

/** Longer than this, the owner's message starts folded. */
const FOLD = 600;

function UserMessage({ item }: { item: Item }) {
  // the files that went with it: chips, not their paths
  const { text, files } = useMemo(() => splitAttachedLine(item.text ?? ''), [item.text]);
  const long = text.length > FOLD;
  const [open, setOpen] = useState(false);
  return (
    <div className="nc-me" title={formatDateTime(item.at)}>
      {text && <span className={`nc-me-text ${long && !open ? 'is-folded' : ''}`}>{text}</span>}
      {long && (
        <button type="button" className="link-btn nc-more" aria-expanded={open} onClick={() => setOpen(!open)}>
          {open ? 'Show less' : 'Show all'}
        </button>
      )}
      {files.length > 0 && <SentFiles files={files} />}
    </div>
  );
}

function Thinking({ text, live = false }: { text: string; live?: boolean }) {
  const [open, setOpen] = useState(false);
  const label = live ? 'Thinking…' : 'Thought';
  if (!text.trim()) {
    return (
      <div className="nc-thought">
        <Icon name="brain" />
        <span>{label}</span>
        {live && <span className="nc-dots" aria-hidden="true" />}
      </div>
    );
  }
  return (
    <div className={`nc-thought-wrap ${open ? 'is-open' : ''}`}>
      <button type="button" className="nc-thought" aria-expanded={open} onClick={() => setOpen(!open)}>
        <Icon name="brain" />
        <span>{label}</span>
        <Icon name={open ? 'chevronUp' : 'chevronDown'} className="nc-chev" />
      </button>
      {open && <div className="nc-thought-text">{text}</div>}
    </div>
  );
}

function ErrorItem({ text }: { text: string }) {
  const account = useContext(ChatAccount);
  // on an API provider there is no login to sign in to: the provider's key was refused
  const help = isLoginError(text) ? loginHelp(account.engine, account.onProvider) : null;
  return (
    <div className="nc-err" role="alert">
      <strong>{help ? help.title : 'Something went wrong'}</strong>
      <span className="nc-err-text">{text}</span>
      {help && (
        <>
          <span className="nc-err-hint">{help.hint}</span>
          <span className="nc-err-actions">
            <Button size="sm" variant="primary" onClick={() => (account.signIn && !account.onProvider ? account.signIn() : navigate(help.to))}>
              {help.action}
            </Button>
          </span>
        </>
      )}
    </div>
  );
}

const STATUS_LABEL: Record<string, string> = { error: 'error', denied: 'denied', stopped: 'stopped' };

/** The right end of a tool's head: a spinner while it runs, what it gave, or how it ended. */
function ToolState({ tool, waiting, done }: { tool: Tool; waiting: boolean; done?: ReactNode }) {
  if (tool.status === 'running') {
    return waiting ? (
      <span className="nc-tstate is-needs">needs you</span>
    ) : (
      <span className="nc-tstate">
        <span className="spinner nc-spin" aria-label="Running" />
      </span>
    );
  }
  if (tool.status !== 'done') return <span className={`nc-tstate is-${tool.status}`}>{STATUS_LABEL[tool.status] ?? tool.status}</span>;
  return done ? <span className="nc-tstate is-done">{done}</span> : null;
}

function DiffStat({ diff }: { diff: Diff }) {
  return (
    <>
      <span className="nc-add">+{diff.added}</span> <span className="nc-del">−{diff.removed}</span>
    </>
  );
}

/** Lines of a diff shown before "Show all". */
const DIFF_LINES = 14;

export function DiffBody({ diff }: { diff: Diff }) {
  const [all, setAll] = useState(false);
  const shown = useMemo(() => hunks(diff.lines), [diff]);
  const cut = !all && shown.length > DIFF_LINES;
  const list = cut ? shown.slice(0, DIFF_LINES) : shown;
  if (!shown.length) return <div className="nc-tbody nc-quiet">No change in the text.</div>;
  return (
    <div className="nc-tbody nc-diff">
      {list.map((l, i) =>
        l === null ? (
          <span key={i} className="nc-gap">
            ⋯
          </span>
        ) : (
          <span key={i} className={l.op === '+' ? 'nc-dadd' : l.op === '-' ? 'nc-ddel' : 'nc-dctx'}>
            {l.op === ' ' ? ' ' : l.op === '-' ? '−' : '+'} {l.text || ' '}
          </span>
        ),
      )}
      {cut && (
        <button type="button" className="link-btn nc-more" onClick={() => setAll(true)}>
          Show all {shown.length} lines
        </button>
      )}
    </div>
  );
}

/** Lines of a command's output shown before "Show all". */
const TAIL = 8;

function Output({ text, error = false, lines = TAIL }: { text: string; error?: boolean; lines?: number }) {
  const [all, setAll] = useState(false);
  const t = useMemo(() => tail(text, lines), [text, lines]);
  if (!text.trim()) return null;
  return (
    <div className={`nc-out ${error ? 'is-error' : ''}`}>
      {!all && t.cut > 0 && (
        <button type="button" className="link-btn nc-more" onClick={() => setAll(true)}>
          {t.cut} earlier {t.cut === 1 ? 'line' : 'lines'}
        </button>
      )}
      <pre>{all ? text : t.text}</pre>
    </div>
  );
}

function BashBody({ tool }: { tool: Tool }) {
  const o = (tool.input ?? {}) as Record<string, unknown>;
  const cmd = typeof o.command === 'string' ? o.command : '';
  return (
    <div className="nc-tbody">
      <pre className="nc-cmdline">
        <span className="nc-prompt">$ </span>
        {cmd}
      </pre>
      <Output text={tool.output ?? ''} error={tool.status === 'error'} />
    </div>
  );
}

function TodoBody({ tool }: { tool: Tool }) {
  const todos = todosOf(tool.input);
  if (!todos.length) return null;
  return (
    <ul className="nc-tbody nc-todos">
      {todos.map((t, i) => (
        <li key={i} className={`is-${t.status}`}>
          <Icon name={t.status === 'completed' ? 'check' : t.status === 'in_progress' ? 'arrowRight' : 'circle'} />
          <span>{t.status === 'in_progress' && t.activeForm ? t.activeForm : t.content}</span>
        </li>
      ))}
    </ul>
  );
}

/** What an unknown or small tool was given and gave, for its expanded row. */
function DetailsBody({ tool }: { tool: Tool }) {
  const input = inputText(tool.input);
  const smallInput = toolPath(tool.name, tool.input) || ['Grep', 'Glob', 'WebSearch', 'WebFetch'].includes(tool.name);
  return (
    <div className="nc-tbody">
      {!smallInput && input && input !== '{}' && <pre className="nc-input">{input}</pre>}
      {tool.clipped && <p className="nc-quiet">The input was too long and is cut here.</p>}
      {tool.output ? <Output text={tool.output} error={tool.status === 'error'} lines={16} /> : <p className="nc-quiet">{tool.status === 'running' ? 'Running…' : 'No output.'}</p>}
    </div>
  );
}

interface ToolProps {
  item: Item;
  compact: boolean;
  waiting: boolean;
  waitingFor: Set<string>;
}

function ToolHead({ tool, arg, open, onToggle, state }: { tool: Tool; arg: string; open?: boolean; onToggle?: () => void; state: ReactNode }) {
  const body = (
    <>
      <Icon name={toolIcon(tool.name)} className="nc-ticon" />
      <span className="nc-tname">{toolLabel(tool.name)}</span>
      <span className="nc-targ" title={arg}>
        {arg}
      </span>
      {state}
    </>
  );
  if (!onToggle) return <div className="nc-thead">{body}</div>;
  return (
    <button type="button" className="nc-thead" aria-expanded={open} onClick={onToggle}>
      {body}
    </button>
  );
}

function SubAgent({ item, compact, waitingFor, waiting }: ToolProps) {
  const tool = item.tool!;
  const childItems = useContext(ChildItems).get(item.id) ?? NO_ITEMS;
  const childStreams = useContext(ChildStreams).get(item.id) ?? NO_STREAMS;
  const running = tool.status === 'running';
  const [open, setOpen] = useState<boolean | null>(null);
  // open while it works in a wide view; a finished one folds unless opened
  const shown = open ?? (running && !compact);
  const calls = childItems.filter((c) => c.kind === 'tool').length;
  const o = (tool.input ?? {}) as Record<string, unknown>;
  const kind = typeof o.subagent_type === 'string' && o.subagent_type !== 'general-purpose' ? o.subagent_type : '';
  const arg = toolArg(tool.name, tool.input, compact);
  return (
    <div className={`nc-sub ${shown ? 'is-open' : ''}`}>
      <ToolHead
        tool={tool}
        arg={kind ? `${arg} · ${kind}` : arg}
        open={shown}
        onToggle={() => setOpen(!shown)}
        state={<ToolState tool={tool} waiting={waiting} done={`${calls} ${calls === 1 ? 'tool' : 'tools'}`} />}
      />
      {shown && (
        <div className="nc-sub-body">
          {childItems.map((c) => (
            <ItemView key={c.id} item={c} compact waitingFor={waitingFor} nested />
          ))}
          {childStreams.map((s) => (
            <StreamView key={s.id} stream={s} compact />
          ))}
          {!childItems.length && !childStreams.length && <p className="nc-quiet">{running ? 'Starting…' : 'Nothing was kept of its work.'}</p>}
          {tool.output && !running && (
            <div className="nc-sub-result">
              <span className="nc-label">Result</span>
              <Markdown text={tool.output} className="is-small" />
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function ToolView(props: ToolProps) {
  const { item, compact, waiting } = props;
  const tool = item.tool!;
  const look = toolLook(tool.name);
  const diff = useMemo(() => (look === 'diff' ? toolDiff(tool.name, tool.input) : null), [look, tool.name, tool.input]);
  // the wide view shows a diff, a command and a failed call's output at once; the rest on a click
  const [open, setOpen] = useState<boolean | null>(null);
  if (look === 'agent') return <SubAgent {...props} />;
  const arg = toolArg(tool.name, tool.input, compact);
  const wide = !compact && (look === 'diff' || look === 'bash' || look === 'todo');
  const shown = open ?? (wide || (!compact && tool.status === 'error'));
  const done = diff ? <DiffStat diff={diff} /> : look === 'bash' ? 'done' : toolMeta(tool);
  const state = <ToolState tool={tool} waiting={waiting} done={done} />;
  const body = shown ? look === 'diff' && diff ? <DiffBody diff={diff} /> : look === 'bash' ? <BashBody tool={tool} /> : look === 'todo' ? <TodoBody tool={tool} /> : <DetailsBody tool={tool} /> : null;
  return (
    <div className={`nc-tool ${shown ? 'is-open' : ''} is-${tool.status}`}>
      <ToolHead tool={tool} arg={arg} open={shown} onToggle={() => setOpen(!shown)} state={state} />
      {body}
    </div>
  );
}

interface ItemProps {
  item: Item;
  compact: boolean;
  /** The tool calls a request waits on. */
  waitingFor: Set<string>;
  /** Inside a sub-agent's group. */
  nested?: boolean;
}

function ItemViewImpl({ item, compact, waitingFor, nested = false }: ItemProps) {
  switch (item.kind) {
    case 'user':
      return <UserMessage item={item} />;
    case 'text':
      return <ReplyText item={item} className={nested ? 'is-small' : ''} />;
    case 'thinking':
      return <Thinking text={item.text ?? ''} />;
    case 'tool':
      if (!item.tool) return null;
      return <ToolView item={item} compact={compact} waiting={waitingFor.has(item.id)} waitingFor={waitingFor} />;
    case 'prompt':
      return <TaskPrompt text={item.text ?? ''} />;
    case 'error':
      return <ErrorItem text={item.text ?? ''} />;
    case 'notice':
      return <span className="nc-notice">{item.text}</span>;
    default:
      return null;
  }
}

export const ItemView = memo(ItemViewImpl);

/** The task a sub-agent was given. */
function TaskPrompt({ text }: { text: string }) {
  const [open, setOpen] = useState(false);
  return (
    <div className="nc-prompt-item">
      <button type="button" className="nc-thought" aria-expanded={open} onClick={() => setOpen(!open)}>
        <Icon name="message" />
        <span>Task given</span>
        <Icon name={open ? 'chevronUp' : 'chevronDown'} className="nc-chev" />
      </button>
      {open && <div className="nc-thought-text">{text}</div>}
    </div>
  );
}

/** The arguments a streamed tool call has so far (its JSON is not whole yet). */
function partialArg(json: string): string {
  const m = /"(command|file_path|notebook_path|pattern|url|query|description)"\s*:\s*"((?:[^"\\]|\\.)*)/.exec(json);
  if (!m) return '';
  try {
    return (JSON.parse(`"${m[2].replace(/\\$/, '')}"`) as string).split('\n')[0];
  } catch {
    return m[2];
  }
}

/** A block being streamed: text with the caret, thinking, or a tool call being written. */
export const StreamView = memo(function StreamView({ stream, compact }: { stream: Stream; compact: boolean }) {
  const live = !stream.ended;
  if (stream.kind === 'thinking') return <Thinking text={stream.text} live={live} />;
  if (stream.kind === 'tool') {
    const tool: Tool = { name: stream.name || 'Tool', status: live ? 'running' : 'stopped' };
    return (
      <div className="nc-tool">
        <ToolHead tool={tool} arg={partialArg(stream.text)} state={<ToolState tool={tool} waiting={false} />} />
      </div>
    );
  }
  return <StreamText stream={stream} live={live} className={compact && stream.parent ? 'is-small' : ''} />;
});

/** A reply being streamed, shown smoothly (smooth.ts), with the caret while more comes. */
function StreamText({ stream, live, className }: { stream: Stream; live: boolean; className: string }) {
  const shown = useSmoothText(stream.text, live, stream.id, true);
  const caret = live || shown.length < stream.text.length;
  if (!shown && caret) {
    return (
      <div className="nc-md">
        <span className="nc-caret" aria-hidden="true" />
      </div>
    );
  }
  return <GrowingMarkdown text={shown} caret={caret} className={className} />;
}
