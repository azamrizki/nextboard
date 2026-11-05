// Two views of the workbench's side bar. Agents: the project thread with the current file
// or selection as context (Ctrl+L), so asking an agent about code is one shortcut away.
// Settings: editor preferences, commands on save, language servers.

import { useEffect, useRef, useState, useSyncExternalStore, useMemo } from 'react';
import { errorMessage } from '../../api';
import { patchSettings, useEffectiveSettings } from '../../agents/settings';
import { Button } from '../../components/Button';
import { Checkbox, SegmentedControl } from '../../components/form';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { readGroup } from '../../board/kinds';
import type { BoardController } from '../../board/controller';
import { isAgentSession, sendCommand } from '../../orch/actions';
import { ThreadList } from '../../orch/ThreadView';
import { useSessions } from '../../store';
import { MarkdownView } from '../MarkdownView';
import { useStudio } from '../store';
import { useWs } from './useWs';
import type { QuickTurn, Workspace } from './workspace';
import { insertMention, mentionAt, mentionedFiles } from './fileMentions';
import { fuzzyTop } from './fuzzy';
import { splitPath } from '../../git/common';
import { FileIcon } from './Explorer';

export interface AgentContext {
  label: string;
  rel: string;
  language: string;
  /** Selected code ("" = the file as a whole). */
  code: string;
  startLine: number;
  endLine: number;
}

/** A question waiting to be sent as soon as the view is up (context menu of the editor). */
export interface AgentRequest {
  prompt: string;
  tick: number;
}

interface Target {
  value: string;
  label: string;
  group: 'Connected' | 'Answer here' | 'Project' | 'Consoles' | 'Groups' | 'Orchestrators';
  /** "@handle" put in front of the order. */
  mention?: string;
  /** The orchestrator card that takes the order, and whose thread shows the answer. */
  cardId?: string;
}

/** Everything on this board that can take a question, wired ones first. */
function useTargets(ctl: BoardController, nodeId: string): Target[] {
  const sessions = useSessions();
  // follows wires drawn on the board, or made by the planner
  useSyncExternalStore(ctl.sync.subscribe, ctl.sync.getState);
  const wired = new Set(ctl.sync.state.edges.filter((e) => e.source === nodeId && e.kind === 'wire').map((e) => e.target));
  const out: Target[] = [{ value: 'quick', label: 'Quick answer (no console)', group: 'Answer here' }, { value: 'planner', label: 'Project thread (planner decides)', group: 'Project' }];
  const add = (nid: string, t: Target) => out.push(wired.has(nid) ? { ...t, group: 'Connected' } : t);
  for (const n of ctl.nodes) {
    if (n.kind === 'terminal') {
      const s = sessions.find((x) => x.id === n.data.sessionId);
      if (s?.handle && isAgentSession(s)) add(n.id, { value: `console:${s.handle}`, label: `@${s.handle}`, group: 'Consoles', mention: `@${s.handle}` });
    } else if (n.kind === 'group') {
      const name = readGroup(n).name;
      if (name) add(n.id, { value: `group:${name}`, label: `@${name}`, group: 'Groups', mention: `@${name}` });
    } else if (n.kind === 'orchestrator') {
      const title = typeof n.data.title === 'string' && n.data.title ? n.data.title : 'Orchestrator';
      add(n.id, { value: `card:${n.id}`, label: title, group: 'Orchestrators', cardId: n.id });
    }
  }
  return out;
}

const fence = (c: AgentContext) => `\`\`\`${c.language}\n${c.code}\n\`\`\``;

/** An order is at most 4000 characters: the code is cut to fit, the path stays. */
export function orderText(text: string, c: AgentContext | null): string {
  if (!c) return text;
  const where = c.code ? `${c.rel}, lines ${c.startLine}-${c.endLine}` : c.rel;
  const head = `${text}\n\nContext: ${where}.`;
  if (!c.code) return head;
  const room = 3900 - head.length;
  if (room < 200) return head.slice(0, 3990);
  return `${head}\n${fence({ ...c, code: c.code.length > room - 40 ? `${c.code.slice(0, room - 80)}\n… (cut: read the file for the rest)` : c.code })}`;
}

function QuickTurnView({ t }: { t: QuickTurn }) {
  const live = useStudio((s) => (t.jobId && t.status === 'running' ? s.output[t.jobId] : ''));
  const text = t.status === 'running' ? live : t.text;
  return (
    <li className="wb-quick-turn">
      <div className="msg msg-user">
        <div className="msg-bubble">
          <span className="msg-text">{t.question}</span>
        </div>
        {t.contextLabel && <span className="msg-time">{t.contextLabel}</span>}
      </div>
      <div className="wb-quick-answer">
        {text ? <MarkdownView text={text} className="md-note" /> : t.status === 'running' ? <p className="text-3"><span className="spinner" aria-hidden="true" /> Reading the code…</p> : null}
        {t.status === 'failed' && <p className="text-danger">{t.error}</p>}
      </div>
    </li>
  );
}

const MENTION_ROWS = 8;
const MENTION_FILES = 5;
const MENTION_BYTES = 20 << 10;

export function AgentsPanel({
  ws,
  ctl,
  nodeId,
  context,
  setContext,
  request,
  focusTick,
}: {
  ws: Workspace;
  ctl: BoardController;
  nodeId: string;
  context: AgentContext | null;
  setContext: (c: AgentContext | null) => void;
  request: AgentRequest | null;
  focusTick: number;
}) {
  useWs(ws);
  const [text, setText] = useState('');
  const [sending, setSending] = useState(false);
  const inputRef = useRef<HTMLTextAreaElement>(null);
  const [files, setFiles] = useState<readonly string[]>([]);
  const [mention, setMention] = useState<{ start: number; query: string } | null>(null);
  const [picked, setPicked] = useState(0);
  const suggestions = useMemo(() => (mention ? fuzzyTop(mention.query, files, MENTION_ROWS).map((h) => h.path) : []), [mention, files]);

  useEffect(() => {
    ws.fileList()
      .then((l) => setFiles(l.items ?? []))
      .catch(() => setFiles([]));
  }, [ws]);

  const takeMention = (path: string) => {
    const el = inputRef.current;
    if (!el || !mention) return;
    const next = insertMention(text, el.selectionStart, mention.start, path);
    setText(next.text);
    setMention(null);
    window.requestAnimationFrame(() => {
      el.focus();
      el.setSelectionRange(next.caret, next.caret);
    });
  };
  const listRef = useRef<HTMLOListElement>(null);
  const targets = useTargets(ctl, nodeId);
  const firstWired = targets.find((t) => t.group === 'Connected');
  const target = targets.find((t) => t.value === ws.agentTarget) ?? firstWired ?? targets[0];

  useEffect(() => {
    inputRef.current?.focus();
  }, [focusTick]);

  // a wire just drawn from the card says where questions should go
  const wiredValue = firstWired?.value;
  useEffect(() => {
    if (wiredValue) {
      ws.agentTarget = wiredValue;
      ws.changed();
    }
  }, [ws, wiredValue]);

  useEffect(() => {
    const el = listRef.current;
    if (el) el.parentElement!.scrollTop = el.parentElement!.scrollHeight;
  }, [ws.quickTurns.length]);

  const send = async (raw: string, ctx: AgentContext | null) => {
    const t = raw.trim();
    if (!t) return;
    setSending(true);
    try {
      if (target.value === 'quick') {
        // the hidden CLI only answers from what it is given, so the files go along
        const named = mentionedFiles(t, new Set(files)).slice(0, MENTION_FILES);
        const attached = await Promise.all(named.map(async (rel) => ({ path: rel, text: (await ws.currentText(ws.abs(rel))).slice(0, MENTION_BYTES) })));
        void ws.askQuick(t, ctx, attached);
      } else {
        const body = orderText(t, ctx);
        await sendCommand(ws.pid, target.mention ? `${target.mention} ${body}` : body, { workspaceId: ctl.workspaceId, cardId: target.cardId ?? null, selection: [] });
      }
      setText('');
      setContext(null);
    } catch (err) {
      toast.error(`The order was not sent: ${errorMessage(err)}`);
    } finally {
      setSending(false);
    }
  };

  // "Explain this" and friends from the editor's menu go out right away
  const handled = useRef(0);
  useEffect(() => {
    if (!request || request.tick === handled.current) return;
    handled.current = request.tick;
    if (request.prompt) void send(request.prompt, context);
    // sent once per request, with the context of that moment
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [request]);

  const groups = ['Connected', 'Answer here', 'Project', 'Consoles', 'Groups', 'Orchestrators'] as const;
  return (
    <div className="wb-view">
      <header className="wb-view-head">
        <h2>Ask</h2>
        {target.value === 'quick' && ws.quickTurns.length > 0 && <Button size="sm" variant="ghost" iconOnly icon="broom" aria-label="Clear the answers" title="Clear the answers" onClick={() => ws.clearQuick()} />}
      </header>
      <div className="wb-ask-target">
        <label htmlFor="wb-ask-to">To</label>
        <select
          id="wb-ask-to"
          className="control control-sm"
          value={target.value}
          onChange={(e) => {
            ws.agentTarget = e.target.value;
            ws.changed();
          }}
        >
          {groups.map((g) => {
            const list = targets.filter((t) => t.group === g);
            return list.length ? (
              <optgroup key={g} label={g}>
                {list.map((t) => (
                  <option key={t.value} value={t.value}>
                    {t.label}
                  </option>
                ))}
              </optgroup>
            ) : null;
          })}
        </select>
      </div>
      <div className="wb-agents-thread nowheel">
        {target.value === 'quick' ? (
          <div className="wb-quick nowheel">
            {!ws.quickTurns.length && <p className="thread-empty">Select code and press Ctrl+L, or right-click it. A hidden CLI reads the project and answers here; it cannot change files. Pick a console above when work should be done.</p>}
            <ol className="plain" ref={listRef}>
              {ws.quickTurns.map((t) => (
                <QuickTurnView key={t.id} t={t} />
              ))}
            </ol>
          </div>
        ) : (
          <ThreadList key={target.cardId ?? ws.pid} threadId={target.cardId ?? ws.pid} compact empty="Orders and answers appear here. Edits your agents make show up in the tree and in open tabs." />
        )}
      </div>
      <form
        className="wb-agents-form"
        onSubmit={(e) => {
          e.preventDefault();
          void send(text, context);
        }}
      >
        {context && (
          <span className="wb-context-chip">
            <Icon name="fileCode" />
            <span className="truncate" title={context.rel}>
              {context.label}
            </span>
            <button type="button" aria-label="Remove this context" onClick={() => setContext(null)}>
              <Icon name="close" />
            </button>
          </span>
        )}
        <textarea
          ref={inputRef}
          className="control prose"
          rows={3}
          aria-label="Your question"
          placeholder={context ? 'Ask about this code. @ names a file. Enter sends, Shift+Enter makes a new line.' : 'Ask about the project. @ names a file. Enter sends, Shift+Enter makes a new line.'}
          value={text}
          onChange={(e) => {
            setText(e.target.value);
            setMention(mentionAt(e.target.value, e.target.selectionStart));
            setPicked(0);
          }}
          onKeyDown={(e) => {
            if (suggestions.length) {
              if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
                e.preventDefault();
                setPicked((i) => (i + (e.key === 'ArrowDown' ? 1 : suggestions.length - 1)) % suggestions.length);
                return;
              }
              if (e.key === 'Enter' || e.key === 'Tab') {
                e.preventDefault();
                takeMention(suggestions[Math.min(picked, suggestions.length - 1)]);
                return;
              }
              if (e.key === 'Escape') {
                e.preventDefault();
                e.stopPropagation();
                setMention(null);
                return;
              }
            }
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault();
              void send(text, context);
            }
          }}
        />
        {suggestions.length > 0 && (
          <ul className="wb-mentions" role="listbox" aria-label="Files">
            {suggestions.map((path, i) => {
              const { dir, name } = splitPath(path);
              return (
                <li key={path} role="option" aria-selected={i === picked}>
                  <button type="button" className={`wb-row ${i === picked ? 'is-selected' : ''}`} onMouseDown={(e) => e.preventDefault()} onClick={() => takeMention(path)}>
                    <FileIcon name={name} />
                    <span className="wb-row-name truncate">{name}</span>
                    {dir && <span className="wb-result-dir truncate">{dir}</span>}
                  </button>
                </li>
              );
            })}
          </ul>
        )}
        <Button type="submit" size="sm" variant="primary" icon="send" loading={sending} disabled={!text.trim()}>
          Send
        </Button>
      </form>
    </div>
  );
}

function OnSaveRules() {
  const settings = useEffectiveSettings();
  const rules = settings.editor?.onSave ?? [];
  const [draft, setDraft] = useState({ pattern: '', command: '' });
  const save = (next: { pattern: string; command: string }[]) =>
    patchSettings({ editor: { onSave: next } }).catch((err) => toast.error(`Could not save the rule: ${errorMessage(err)}`));
  return (
    <section className="wb-settings-section">
      <h3>Run a command on save</h3>
      <p className="text-3">In the project folder, in this order. {'{file}'} is the saved file. Stops at the first failure and after 60 s.</p>
      {rules.map((r, i) => (
        <div key={`${r.pattern}:${i}`} className="wb-rule">
          <code className="truncate" title={r.pattern}>
            {r.pattern}
          </code>
          <code className="truncate grow" title={r.command}>
            {r.command}
          </code>
          <Button size="sm" variant="ghost" iconOnly icon="close" aria-label={`Remove the rule for ${r.pattern}`} onClick={() => void save(rules.filter((_, x) => x !== i))} />
        </div>
      ))}
      <form
        className="wb-rule-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (!draft.pattern.trim() || !draft.command.trim()) return;
          void save([...rules, { pattern: draft.pattern.trim(), command: draft.command.trim() }]);
          setDraft({ pattern: '', command: '' });
        }}
      >
        <input className="control control-sm mono" aria-label="File pattern" placeholder="*.go or src/**/*.ts" value={draft.pattern} onChange={(e) => setDraft({ ...draft, pattern: e.target.value })} />
        <input className="control control-sm mono" aria-label="Command" placeholder="gofmt -w {file}" value={draft.command} onChange={(e) => setDraft({ ...draft, command: e.target.value })} />
        <Button type="submit" size="sm" icon="plus" disabled={!draft.pattern.trim() || !draft.command.trim()}>
          Add
        </Button>
      </form>
    </section>
  );
}

export function SettingsPanel({ ws, onInstall }: { ws: Workspace; onInstall: (command: string) => void }) {
  useWs(ws);
  const p = ws.editorPrefs;
  return (
    <div className="wb-view">
      <header className="wb-view-head">
        <h2>Settings</h2>
      </header>
      <div className="wb-settings nowheel">
        <section className="wb-settings-section">
          <h3>Editor</h3>
          <label className="wb-field">
            <span>Font size</span>
            <input className="control control-sm tabular" type="number" min={9} max={32} value={p.fontSize} onChange={(e) => ws.setPrefs({ fontSize: Math.min(32, Math.max(9, Number(e.target.value) || 13)) })} />
          </label>
          <label className="wb-field">
            <span>Tab size</span>
            <input className="control control-sm tabular" type="number" min={1} max={8} value={p.tabSize} onChange={(e) => ws.setPrefs({ tabSize: Math.min(8, Math.max(1, Number(e.target.value) || 2)) })} />
          </label>
          <div className="wb-field">
            <span>Auto save</span>
            <SegmentedControl
              size="sm"
              label="Auto save"
              value={p.autoSave}
              onChange={(autoSave) => ws.setPrefs({ autoSave })}
              options={[
                { value: 'off', label: 'Off' },
                { value: 'afterDelay', label: 'After 1 s' },
                { value: 'onFocusChange', label: 'On blur' },
              ]}
            />
          </div>
          <div className="wb-field wb-field-stack">
            <span>AI suggestions (ghost text)</span>
            <SegmentedControl
              size="sm"
              label="AI suggestions"
              value={p.aiInline}
              onChange={(aiInline) => ws.setPrefs({ aiInline })}
              options={[
                { value: 'off', label: 'Off' },
                { value: 'manual', label: 'Alt+\\' },
                { value: 'auto', label: 'Typing' },
              ]}
            />
            <small className="text-3">Each one is a run of your Claude Code or Codex CLI: it takes seconds and spends your plan. “Typing” asks whenever you pause.</small>
          </div>
          <Checkbox checked={p.formatOnSave} label="Format on save" onChange={(formatOnSave) => ws.setPrefs({ formatOnSave })} />
          <Checkbox checked={p.shareSelection} label="Share selected text with linked Claude consoles" onChange={(shareSelection) => ws.setPrefs({ shareSelection })} />
          <Checkbox checked={p.minimap} label="Minimap" onChange={(minimap) => ws.setPrefs({ minimap })} />
          <Checkbox checked={p.wordWrap} label="Word wrap" onChange={(wordWrap) => ws.setPrefs({ wordWrap })} />
          <Checkbox checked={p.stickyScroll} label="Sticky scroll" onChange={(stickyScroll) => ws.setPrefs({ stickyScroll })} />
          <Checkbox checked={p.bracketColors} label="Bracket pair colours" onChange={(bracketColors) => ws.setPrefs({ bracketColors })} />
          <Checkbox checked={p.renderWhitespace} label="Show whitespace" onChange={(renderWhitespace) => ws.setPrefs({ renderWhitespace })} />
        </section>
        <OnSaveRules />
        <section className="wb-settings-section">
          <h3>Language servers</h3>
          <p className="text-3">IntelliSense for more languages comes from servers on the project’s machine. NextBoard never installs them for you.</p>
          {ws.lsp.servers.map((s) => (
            <div key={s.id} className="wb-lsp-row">
              <span className={`wb-lsp-dot ${s.available ? 'is-on' : ''}`} aria-hidden="true" />
              <span className="grow truncate" title={s.available ? s.command : s.install}>
                {s.name}
                <span className="text-3"> · {s.languages.join(', ')}</span>
              </span>
              {!s.available && s.install.includes(' ') && !s.install.startsWith('Installed') && (
                <Button size="sm" variant="ghost" onClick={() => onInstall(s.install)} title={s.install}>
                  Install…
                </Button>
              )}
            </div>
          ))}
          {!ws.lsp.servers.length && <p className="text-3">Checking the machine…</p>}
          <Button size="sm" icon="restart" onClick={() => ws.lsp.restart()}>
            Restart language servers
          </Button>
        </section>
      </div>
    </div>
  );
}
