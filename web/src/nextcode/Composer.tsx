// NextCode's composer: Enter sends, Shift+Enter is a new line, "/" at the start lists the CLI's
// slash commands, "@" lists the files of the console's folder (fileMentions.ts), and Stop ends the
// turn while the agent works. Files pasted, dropped on the chat or picked with + become chips and go
// with the message as their paths on its last line (attach.ts). The draft and the chips stay per
// console, so they survive a move between the board, a Code view tab and a phone.

import { useContext, useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { clipboardFiles } from '../board/files';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Menu } from '../components/Menu';
import { useSession } from '../store';
import { attachedMessage } from './attach';
import { ComposerChips, useChatAttachments } from './ChatAttachments';
import { fileQueryAt, insertFile, type FileOption } from './fileMentions';
import { matchCommands, modeLabel, modesFor } from './format';
import { ChatAccount } from './Items';
import type { PermissionMode } from './types';
import { useFileOptions } from './useFileOptions';

/** What was typed and not sent, per console, while this page is open. */
const drafts = new Map<string, string>();

interface ComposerProps {
  sessionId: string;
  /** "@Arkan", for the placeholder. */
  who: string;
  variant: 'tab' | 'card' | 'phone';
  working: boolean;
  stopping: boolean;
  disabled: boolean;
  commands: string[];
  mode: PermissionMode;
  onMode: (m: PermissionMode) => void;
  onSend: (text: string) => Promise<boolean>;
  onStop: () => void;
  /** Bumped to give the field the keyboard. */
  focusTick?: number;
  /** The model chip of the card and the phone (the tab has it in its toolbar). */
  model?: ReactNode;
}

/** The permission mode as a chip with a menu (the board card and the phone; the tab has a segmented control). */
export function ModeMenu({ mode, onMode }: { mode: PermissionMode; onMode: (m: PermissionMode) => void }) {
  // the API engine's Auto is a rule of its own, not Claude's classifier
  const { engine } = useContext(ChatAccount);
  return (
    <Menu
      label={`Permission mode: ${modeLabel(mode)}`}
      className="nc-mode-menu"
      triggerClassName="nc-chip"
      trigger={
        <>
          <span>{modeLabel(mode)}</span>
          <Icon name="chevronDown" />
        </>
      }
      items={[
        { type: 'heading', label: 'Permission mode' },
        ...modesFor(engine).map((m) => ({ label: m.label, title: m.title, checked: m.value === mode, danger: m.value === 'bypassPermissions', onSelect: () => onMode(m.value) })),
      ]}
    />
  );
}

/** A path with its matched characters marked. */
function marked(text: string, positions: number[]): ReactNode {
  if (!positions.length) return text;
  const at = new Set(positions);
  const out: ReactNode[] = [];
  let run = '';
  let on = false;
  for (let i = 0; i <= text.length; i++) {
    const m = at.has(i);
    if (i === text.length || m !== on) {
      if (run) out.push(on ? <mark key={i}>{run}</mark> : run);
      run = '';
      on = m;
    }
    if (i < text.length) run += text[i];
  }
  return out;
}

export function Composer({ sessionId, who, variant, working, stopping, disabled, commands, mode, onMode, onSend, onStop, focusTick, model }: ComposerProps) {
  const session = useSession(sessionId);
  const att = useChatAttachments(sessionId);
  const [text, setTextState] = useState(() => drafts.get(sessionId) ?? '');
  const [caret, setCaret] = useState(() => (drafts.get(sessionId) ?? '').length);
  const [sending, setSending] = useState(false);
  const [active, setActive] = useState(0);
  // Esc closes the list until the text changes
  const [closedFor, setClosedFor] = useState<string | null>(null);
  const ref = useRef<HTMLTextAreaElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const pendingCaret = useRef<number | null>(null);
  const listId = useId();
  const setText = (t: string) => {
    setTextState(t);
    if (t) drafts.set(sessionId, t);
    else drafts.delete(sessionId);
  };

  useEffect(() => {
    if (focusTick) ref.current?.focus();
  }, [focusTick]);

  // grows with the text up to a few lines
  const maxRows = variant === 'tab' ? 10 : 6;
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = 'auto';
    const cs = getComputedStyle(el);
    const line = parseFloat(cs.lineHeight) || 20;
    const pad = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
    el.style.height = `${Math.min(el.scrollHeight, line * maxRows + pad)}px`;
  }, [text, maxRows]);

  // the caret goes where an inserted name ends, in the same commit as the text
  useLayoutEffect(() => {
    const pos = pendingCaret.current;
    if (pos === null) return;
    pendingCaret.current = null;
    const el = ref.current;
    if (!el) return;
    el.focus();
    el.setSelectionRange(pos, pos);
    setCaret(pos);
  }, [text]);

  // what follows a "/" that starts the text, while no space has ended the command's name
  const typed = /^\/(\S*)$/.exec(text)?.[1] ?? null;
  const commandOptions = useMemo(() => (typed !== null && closedFor !== text ? matchCommands(commands, typed) : []), [typed, closedFor, text, commands]);
  // an "@word" at the caret, when no command is being typed
  const fileQ = !commandOptions.length && closedFor !== text && !disabled ? fileQueryAt(text, caret) : null;
  const files = useFileOptions(session, fileQ ? fileQ.query : null);
  const fileOpen = !!fileQ && files.available;
  const kind: 'slash' | 'file' | null = commandOptions.length ? 'slash' : fileOpen ? 'file' : null;
  const count = kind === 'slash' ? commandOptions.length : kind === 'file' ? files.options.length : 0;
  const open = kind !== null;
  useEffect(() => setActive(0), [count, typed, fileQ?.query]);

  const pickCommand = (name: string) => {
    setText(`/${name} `);
    pendingCaret.current = name.length + 2;
    ref.current?.focus();
  };

  const pickFile = (opt: FileOption) => {
    if (!fileQ) return;
    const el = ref.current;
    const at = el?.selectionStart ?? caret;
    const next = insertFile(text, at, fileQ.start, opt);
    pendingCaret.current = next.caret;
    setText(next.text);
  };

  /** "@" at the caret, opening the file list. */
  const typeAt = () => {
    const el = ref.current;
    const at = el?.selectionStart ?? text.length;
    const lead = at > 0 && !/\s/.test(text[at - 1]) ? ' ' : '';
    pendingCaret.current = at + lead.length + 1;
    setClosedFor(null);
    setText(`${text.slice(0, at)}${lead}@${text.slice(at)}`);
  };

  const canSend = !disabled && !sending && !att.busy && (!!text.trim() || att.paths.length > 0);

  const send = async () => {
    if (!canSend) return;
    const msg = attachedMessage(text, att.paths);
    setSending(true);
    const ok = await onSend(msg);
    setSending(false);
    if (ok) {
      setText('');
      setCaret(0);
      att.clear();
    }
    ref.current?.focus();
  };

  const placeholder = disabled ? 'This console has ended' : `Message ${who}…${variant === 'tab' ? '  / for commands, @ for files' : ''}`;

  return (
    <div className={`nc-composer ${open ? 'has-menu' : ''}`}>
      {open && (
        <div className="nc-slash" id={listId} role="listbox" aria-label={kind === 'slash' ? 'Slash commands' : 'Files'}>
          {kind === 'slash' &&
            commandOptions.map((c, i) => (
              <div
                key={c}
                id={`${listId}-${i}`}
                role="option"
                aria-selected={i === active}
                className={`nc-slash-opt ${i === active ? 'is-active' : ''}`}
                onMouseDown={(e) => {
                  e.preventDefault();
                  pickCommand(c);
                }}
                onMouseEnter={() => setActive(i)}
              >
                <span className="nc-slash-name">/{c}</span>
              </div>
            ))}
          {kind === 'file' &&
            files.options.map((f, i) => (
              <div
                key={f.path}
                id={`${listId}-${i}`}
                role="option"
                aria-selected={i === active}
                className={`nc-slash-opt nc-file-opt ${i === active ? 'is-active' : ''}`}
                title={f.path}
                onMouseDown={(e) => {
                  e.preventDefault();
                  pickFile(f);
                }}
                onMouseEnter={() => setActive(i)}
              >
                <Icon name={f.dir ? 'folder' : 'file'} />
                <span className="nc-file-path">{marked(f.path, f.positions)}</span>
              </div>
            ))}
          {kind === 'file' && files.loading && (
            <div className="nc-slash-note" role="status">
              <span className="spinner" aria-hidden="true" /> Listing files…
            </div>
          )}
          {kind === 'file' && !files.loading && !files.options.length && <div className="nc-slash-note">{fileQ?.query ? `No file matches “${fileQ.query}”` : 'No files here'}</div>}
          {kind === 'file' && !files.loading && files.note && <div className="nc-slash-note">{files.note}</div>}
        </div>
      )}
      <ComposerChips att={att} inputRef={ref} />
      <textarea
        ref={ref}
        className="nc-input"
        rows={1}
        value={text}
        placeholder={placeholder}
        aria-label={`Message to ${who}`}
        disabled={disabled}
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-activedescendant={open && count ? `${listId}-${active}` : undefined}
        spellCheck
        onChange={(e) => {
          // typing puts the caret itself: an insert that changed nothing must not move it later
          pendingCaret.current = null;
          setText(e.target.value);
          setCaret(e.target.selectionStart ?? e.target.value.length);
        }}
        onSelect={(e) => setCaret(e.currentTarget.selectionStart ?? 0)}
        onPaste={(e) => {
          const pasted = clipboardFiles(e.clipboardData);
          if (!pasted.length) return;
          e.preventDefault();
          att.add(pasted);
        }}
        onBlur={() => window.setTimeout(() => document.activeElement !== ref.current && setClosedFor(text), 150)}
        onFocus={() => setClosedFor((c) => (c === text ? null : c))}
        onKeyDown={(e) => {
          // never reaches the board's or the editor's shortcuts
          e.stopPropagation();
          if (open) {
            if (count && e.key === 'ArrowDown') {
              e.preventDefault();
              setActive((i) => (i + 1) % count);
              return;
            }
            if (count && e.key === 'ArrowUp') {
              e.preventDefault();
              setActive((i) => (i - 1 + count) % count);
              return;
            }
            if (kind === 'slash' && (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing && commandOptions[active] !== typed))) {
              e.preventDefault();
              pickCommand(commandOptions[active]);
              return;
            }
            if (kind === 'file' && count && (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing))) {
              e.preventDefault();
              pickFile(files.options[active]);
              return;
            }
            if (e.key === 'Escape') {
              e.preventDefault();
              setClosedFor(text);
              return;
            }
          }
          if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
            e.preventDefault();
            void send();
          }
        }}
      />
      <div className="nc-cbar">
        <Button size="sm" variant="ghost" iconOnly icon="plus" aria-label="Attach files or pictures" title="Attach files or pictures (or paste, or drop them on the chat)" disabled={disabled} onClick={() => fileRef.current?.click()} />
        <input
          ref={fileRef}
          type="file"
          multiple
          hidden
          onChange={(e) => {
            const picked = Array.from(e.target.files ?? []);
            e.target.value = '';
            if (picked.length) att.add(picked);
            ref.current?.focus();
          }}
        />
        {variant === 'tab' && (
          <>
            <Button size="sm" variant="ghost" iconOnly icon="at" aria-label="Mention a file" title="Mention a file (@)" disabled={disabled} onClick={typeAt} />
            <Button size="sm" variant="ghost" iconOnly icon="sparkle" aria-label="Slash commands" title="Slash commands (/)" disabled={disabled || !commands.length} onClick={() => {
              setText('/');
              setClosedFor(null);
              pendingCaret.current = 1;
              ref.current?.focus();
            }} />
          </>
        )}
        {variant !== 'tab' && <ModeMenu mode={mode} onMode={onMode} />}
        {variant !== 'tab' && model}
        <span className="spacer" />
        {working && (
          <Button size="sm" variant="secondary" icon="stop" className="nc-stop" loading={stopping} disabled={stopping} title="Stop the agent's turn" onClick={onStop}>
            {stopping ? 'Stopping' : 'Stop'}
          </Button>
        )}
        <Button
          size="sm"
          variant="primary"
          iconOnly
          icon="send"
          className="nc-send"
          aria-label="Send"
          title={att.busy ? 'Send: waits for the files to be attached' : working ? 'Send: it waits for the current turn' : 'Send (Enter)'}
          disabled={!canSend}
          loading={sending}
          onClick={() => void send()}
        />
      </div>
    </div>
  );
}
