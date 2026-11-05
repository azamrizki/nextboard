// Order input with the "@" menu (docs/API-orchestration.md §18, D1/D2): a growing
// textarea (Enter sends, Shift+Enter new line), ↑/↓ history (50, per client), and a
// combobox listbox of mention candidates (↑/↓ move, Tab/Enter accept, Esc dismiss).
// A pasted file goes to This PC; the box around the input shows it (docs/API-studio.md §7.9).
// Used by the command bar and by orchestrator cards.

import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type ClipboardEvent, type KeyboardEvent, type RefObject } from 'react';
import { agentById } from '../agents/catalog';
import { agentPill } from '../agents/state';
import { clipboardFiles } from '../board/files';
import { Icon, type IconName } from '../components/Icon';
import { prefs } from '../lib/format';
import { store } from '../store';
import { orchApi } from './api';
import type { Attachments } from './Attachments';
import { filterMentions, mentionToken } from './mentions';
import type { Mention } from './types';

const KIND_ICON: Record<string, IconName> = { everyone: 'users', console: 'terminal', group: 'group', role: 'flag', agent: 'bot' };
const KIND_LABEL: Record<string, string> = { everyone: 'Everyone', console: 'Console', group: 'Group', role: 'Role', agent: 'Agent' };

const HISTORY_MAX = 50;
function loadHistory(key: string): string[] {
  try {
    const v = JSON.parse(prefs.get(`cmdhist.${key}`) ?? '[]');
    return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string').slice(-HISTORY_MAX) : [];
  } catch {
    return [];
  }
}
export function pushHistory(key: string, text: string) {
  const h = loadHistory(key).filter((x) => x !== text);
  h.push(text);
  prefs.set(`cmdhist.${key}`, JSON.stringify(h.slice(-HISTORY_MAX)));
}

/** Hub list (when supported) merged over the local list. */
let hubMentions: boolean | null = null;

interface MentionInputProps {
  projectId: string;
  workspaceId: string;
  cardId?: string | null;
  value: string;
  onChange: (v: string) => void;
  onSubmit: (text: string) => void;
  /** Local candidates (board + sessions). */
  local: () => Mention[];
  placeholder: string;
  label: string;
  historyKey: string;
  disabled?: boolean;
  inputRef?: RefObject<HTMLTextAreaElement | null>;
  onEscape?: () => void;
  maxRows?: number;
  className?: string;
  /** Open the menu upward (bar) or downward (card). */
  menuPlacement?: 'up' | 'down';
  /** What is pasted goes here (useAttachments), and may be all an order has. */
  attachments: Attachments;
}

export function MentionInput({
  projectId,
  workspaceId,
  cardId,
  value,
  onChange,
  onSubmit,
  local,
  placeholder,
  label,
  historyKey,
  disabled,
  inputRef,
  onEscape,
  maxRows = 6,
  className,
  menuPlacement = 'up',
  attachments,
}: MentionInputProps) {
  const ownRef = useRef<HTMLTextAreaElement>(null);
  const ref = inputRef ?? ownRef;
  const listId = useId();
  const [query, setQuery] = useState<{ q: string; start: number } | null>(null);
  const [active, setActive] = useState(0);
  const [remote, setRemote] = useState<Mention[] | null>(null);
  const hist = useRef<{ list: string[]; i: number; draft: string } | null>(null);
  const pendingCaret = useRef<number | null>(null);
  useLayoutEffect(() => {
    const pos = pendingCaret.current;
    if (pos === null) return;
    pendingCaret.current = null;
    const el = ref.current;
    if (!el) return;
    el.focus();
    el.setSelectionRange(pos, pos);
  }, [value, ref]);

  // auto-grow up to maxRows
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    el.style.height = 'auto';
    const cs = getComputedStyle(el);
    const line = parseFloat(cs.lineHeight) || 20;
    const padY = parseFloat(cs.paddingTop) + parseFloat(cs.paddingBottom);
    const border = parseFloat(cs.borderTopWidth) + parseFloat(cs.borderBottomWidth);
    el.style.height = `${Math.min(el.scrollHeight, line * maxRows + padY) + border}px`;
  }, [value, ref, maxRows]);

  // ask the hub for candidates (debounced); fall back to the local list
  useEffect(() => {
    if (!query || hubMentions === false) {
      setRemote(null);
      return;
    }
    const t = window.setTimeout(() => {
      orchApi
        .mentions(projectId, { workspaceId, cardId: cardId ?? undefined, q: query.q })
        .then((list) => {
          hubMentions = true;
          setRemote(list);
        })
        .catch(() => {
          hubMentions = false;
          setRemote(null);
        });
    }, 120);
    return () => window.clearTimeout(t);
  }, [query, projectId, workspaceId, cardId]);

  const options = useMemo(() => {
    if (!query) return [];
    const base = remote ?? filterMentions(local(), query.q);
    return base.slice(0, 30);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query, remote]);

  useEffect(() => setActive(0), [query?.q, options.length]);

  const detect = (text: string, caret: number) => {
    const before = text.slice(0, caret);
    const m = /(^|[\s,(])@([\w-]*)$/.exec(before);
    if (m) setQuery({ q: m[2], start: caret - m[2].length - 1 });
    else setQuery(null);
  };

  const accept = (m: Mention) => {
    if (!query) return;
    const el = ref.current;
    const caret = el?.selectionStart ?? value.length;
    const token = mentionToken(m) + ' ';
    const next = value.slice(0, query.start) + token + value.slice(caret).replace(/^\s+/, '');
    // the caret goes right after the token in the same commit (a deferred move would
    // land in the middle of whatever the user types next)
    pendingCaret.current = query.start + token.length;
    onChange(next);
    setQuery(null);
  };

  const onPaste = (e: ClipboardEvent<HTMLTextAreaElement>) => {
    const files = clipboardFiles(e.clipboardData);
    if (!files.length) return;
    e.preventDefault();
    attachments.add(files);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    e.stopPropagation(); // never reaches board shortcuts
    if (query && options.length) {
      if (e.key === 'ArrowDown') {
        e.preventDefault();
        setActive((i) => (i + 1) % options.length);
        return;
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault();
        setActive((i) => (i - 1 + options.length) % options.length);
        return;
      }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) {
        e.preventDefault();
        accept(options[active]);
        return;
      }
    }
    if (e.key === 'Escape') {
      e.preventDefault();
      if (query) setQuery(null);
      else onEscape?.();
      return;
    }
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing) {
      e.preventDefault();
      const text = value.trim();
      if (disabled || !attachments.canSend(text)) return;
      if (text) pushHistory(historyKey, text);
      hist.current = null;
      onSubmit(text);
      return;
    }
    const el = e.currentTarget;
    const single = !value.includes('\n');
    if (e.key === 'ArrowUp' && (single || el.selectionStart === 0) && !query) {
      const h = hist.current ?? { list: loadHistory(historyKey), i: -1, draft: value };
      if (!h.list.length) return;
      e.preventDefault();
      const i = h.i < 0 ? h.list.length - 1 : Math.max(0, h.i - 1);
      hist.current = { ...h, i };
      onChange(h.list[i]);
      return;
    }
    if (e.key === 'ArrowDown' && hist.current && (single || el.selectionStart === value.length) && !query) {
      e.preventDefault();
      const h = hist.current;
      if (h.i >= h.list.length - 1) {
        hist.current = null;
        onChange(h.draft);
      } else {
        hist.current = { ...h, i: h.i + 1 };
        onChange(h.list[h.i + 1]);
      }
    }
  };

  const open = !!query && options.length > 0;
  const sessions = store.getState().sessions;

  return (
    <div className={`mention-input ${className ?? ''}`}>
      <textarea
        ref={ref}
        className="control mention-textarea"
        rows={1}
        value={value}
        placeholder={placeholder}
        aria-label={label}
        disabled={disabled}
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={open}
        aria-controls={open ? listId : undefined}
        aria-activedescendant={open ? `${listId}-${active}` : undefined}
        spellCheck
        maxLength={4000}
        onChange={(e) => {
          hist.current = null;
          onChange(e.target.value);
          detect(e.target.value, e.target.selectionStart ?? e.target.value.length);
        }}
        onKeyDown={onKeyDown}
        onPaste={onPaste}
        onKeyUp={(e) => {
          if (e.key === 'ArrowLeft' || e.key === 'ArrowRight' || e.key === 'Home' || e.key === 'End')
            detect(e.currentTarget.value, e.currentTarget.selectionStart ?? 0);
        }}
        onClick={(e) => detect(e.currentTarget.value, e.currentTarget.selectionStart ?? 0)}
        onBlur={() => window.setTimeout(() => setQuery(null), 150)}
      />
      {open && (
        <div className={`mention-menu ${menuPlacement === 'down' ? 'is-down' : ''}`} id={listId} role="listbox" aria-label="Direct to">
          <div className="mention-head" aria-hidden="true">
            Direct to
          </div>
          {options.map((m, i) => {
            const s = m.kind === 'console' ? sessions[m.id] : undefined;
            const pill = s ? agentPill(s) : null;
            return (
              <div
                key={`${m.kind}-${m.id}`}
                id={`${listId}-${i}`}
                role="option"
                aria-selected={i === active}
                className={`mention-opt ${i === active ? 'is-active' : ''}`}
                onMouseDown={(e) => {
                  e.preventDefault();
                  accept(m);
                }}
                onMouseEnter={() => setActive(i)}
              >
                <Icon name={KIND_ICON[m.kind] ?? 'at'} />
                <span className="mention-label truncate">{m.kind === 'console' ? `@${m.handle ?? m.label}` : m.kind === 'agent' ? `@${m.id}` : m.label}</span>
                {m.kind === 'agent' && agentById(m.id) && <span className="mention-kind truncate">{agentById(m.id)!.name}</span>}
                {m.board && <span className="mention-kind truncate">{m.board}</span>}
                <span className="mention-kind">{KIND_LABEL[m.kind] ?? m.kind}</span>
                {pill && <span className={`agent-pill tone-${pill.tone} mention-pill`}>{pill.label}</span>}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
