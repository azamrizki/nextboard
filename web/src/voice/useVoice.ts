// Push-to-talk and dictation for the board (D11, D13). Only active after the user turned
// voice on in Settings › Voice (the disclosure explains where the audio goes).
//   - Hold Space on the board (not in a console or a field), or hold / tap the mic:
//     words appear live in the command bar; release sends the order (source "voice");
//     Esc cancels; a recording cut at 60 s is not sent.
//   - Hold the dictation key (default Right Alt): the words are typed — never submitted —
//     into the focused field, or the focused / single selected console (delivery guard
//     "dictation"; a console that can't take it hands the text to the command bar).
//     Spoken punctuation works; any other key cancels; at 5 minutes the text so far lands.

import { useCallback, useEffect, useRef, useState } from 'react';
import { ApiError } from '../api';
import { toast } from '../components/Toast';
import type { BoardController } from '../board/controller';
import { isRunning, sessionOf } from '../board/controller';
import { orchApi } from '../orch/api';
import { getVoicePrefs, listen, punctuate, speechSupported, useVoicePrefs, type Listening } from './voice';

export type VoiceMode = 'idle' | 'talk' | 'dictate';
type Target = { kind: 'field'; el: HTMLInputElement | HTMLTextAreaElement } | { kind: 'console'; sessionId: string; label: string } | { kind: 'bar' };

const TALK_MAX = 60_000;
const DICTATE_MAX = 5 * 60_000;

function typing(t: EventTarget | null): boolean {
  const el = t as HTMLElement | null;
  if (!el?.closest) return false;
  if (el.isContentEditable || /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) return true;
  return !!el.closest('.xterm, [role="menu"], .modal-layer, .popover, .cm-editor');
}

interface Opts {
  ctl: BoardController;
  /** Send an order typed by voice. */
  onOrder: (text: string) => Promise<void> | void;
  /** Put text into the command bar input. */
  onBarText: (text: string) => void;
}

export function useVoice({ ctl, onOrder, onBarText }: Opts) {
  const prefs = useVoicePrefs();
  const active = prefs.enabled && speechSupported();
  const [mode, setMode] = useState<VoiceMode>('idle');
  const [text, setText] = useState('');
  const [target, setTarget] = useState('');
  const rec = useRef<Listening | null>(null);
  const timer = useRef(0);
  const capped = useRef(false);
  const targetRef = useRef<Target>({ kind: 'bar' });
  const cb = useRef({ onOrder, onBarText });
  cb.current = { onOrder, onBarText };

  const finish = useCallback(() => {
    window.clearTimeout(timer.current);
    rec.current = null;
    setMode('idle');
    setTarget('');
  }, []);

  const deliver = useCallback(async (raw: string) => {
    const words = punctuate(raw);
    if (!words.trim()) return;
    const t = targetRef.current;
    if (t.kind === 'field') {
      t.el.focus();
      if (!document.execCommand('insertText', false, words)) t.el.setRangeText(words, t.el.selectionStart ?? t.el.value.length, t.el.selectionEnd ?? t.el.value.length, 'end');
      return;
    }
    if (t.kind === 'console') {
      try {
        await orchApi.deliver(t.sessionId, { text: words, submit: false, guard: 'dictation', source: 'dictation' });
        return;
      } catch (err) {
        if (!(err instanceof ApiError && err.status === 409)) toast.error(`Dictation not typed: ${err instanceof Error ? err.message : String(err)}`);
        else toast.info(`${t.label} isn’t at a prompt; the text went to the command bar.`);
      }
    }
    cb.current.onBarText(words);
  }, []);

  const start = useCallback(
    (m: Exclude<VoiceMode, 'idle'>, tgt: Target = { kind: 'bar' }) => {
      if (rec.current) return;
      const p = getVoicePrefs();
      capped.current = false;
      targetRef.current = tgt;
      setTarget(tgt.kind === 'console' ? tgt.label : tgt.kind === 'field' ? 'the focused field' : m === 'talk' ? '' : 'the command bar');
      setText('');
      const l = listen({
        lang: p.lang,
        onText: setText,
        onDone: (t) => {
          finish();
          setText('');
          if (m === 'talk') {
            if (capped.current) return;
            if (t) void cb.current.onOrder(t);
          } else void deliver(t);
        },
        onError: (code) => {
          finish();
          setText('');
          if (code === 'not-allowed' || code === 'service-not-allowed') toast.error('The browser blocked the microphone. Allow it for this site, then try again.');
          else if (code === 'network') toast.error('Speech recognition needs the browser’s online service and it can’t be reached.');
          else if (code !== 'no-speech' && code !== 'aborted') toast.error(`Voice stopped: ${code}.`);
        },
      });
      if (!l) {
        toast.error('This browser can’t listen right now.');
        return;
      }
      rec.current = l;
      setMode(m);
      timer.current = window.setTimeout(
        () => {
          if (m === 'talk') {
            capped.current = true;
            rec.current?.cancel();
            finish();
            setText('');
            toast.info('Stopped at 60 seconds; nothing was sent.');
          } else rec.current?.stop();
        },
        m === 'talk' ? TALK_MAX : DICTATE_MAX,
      );
    },
    [deliver, finish],
  );

  const stop = useCallback(() => rec.current?.stop(), []);
  const cancel = useCallback(() => {
    rec.current?.cancel();
    finish();
    setText('');
  }, [finish]);

  const dictationTarget = useCallback((): Target => {
    const el = document.activeElement as HTMLElement | null;
    const xterm = el?.closest?.('.react-flow__node');
    if (el?.classList.contains('xterm-helper-textarea') && xterm) {
      const n = ctl.node(xterm.getAttribute('data-id') ?? '');
      const s = n ? sessionOf(n) : undefined;
      if (s) return { kind: 'console', sessionId: s.id, label: s.handle ? `@${s.handle}` : s.name };
    }
    if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA') && !el.closest('.cmd-input')) return { kind: 'field', el: el as HTMLInputElement };
    const sel = ctl.selectedNodes().filter((n) => n.kind === 'terminal');
    if (sel.length === 1) {
      const s = sessionOf(sel[0]);
      if (s && isRunning(s)) return { kind: 'console', sessionId: s.id, label: s.handle ? `@${s.handle}` : s.name };
    }
    return { kind: 'bar' };
  }, [ctl]);

  useEffect(() => {
    if (!active) return;
    const down = (e: KeyboardEvent) => {
      const p = getVoicePrefs();
      if (rec.current) {
        if (e.key === 'Escape') {
          e.preventDefault();
          e.stopPropagation();
          cancel();
        } else if (mode === 'dictate' && e.code !== p.dictationKey) cancel();
        else if (e.code === 'Space' || e.code === p.dictationKey) e.preventDefault();
        return;
      }
      if (e.repeat || document.querySelector('.modal-layer')) return;
      if (e.code === p.dictationKey && !e.ctrlKey && !e.metaKey) {
        e.preventDefault();
        start('dictate', dictationTarget());
        return;
      }
      if (p.holdSpace && e.code === 'Space' && !e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey && !typing(e.target)) {
        e.preventDefault();
        start('talk');
      }
    };
    const up = (e: KeyboardEvent) => {
      if (!rec.current) return;
      const p = getVoicePrefs();
      if ((mode === 'talk' && e.code === 'Space') || (mode === 'dictate' && e.code === p.dictationKey)) {
        e.preventDefault();
        stop();
      }
    };
    window.addEventListener('keydown', down, true);
    window.addEventListener('keyup', up, true);
    return () => {
      window.removeEventListener('keydown', down, true);
      window.removeEventListener('keyup', up, true);
    };
  }, [active, mode, start, stop, cancel, dictationTarget]);

  // leaving the board: never keep the microphone open
  useEffect(() => () => rec.current?.cancel(), []);

  return { active, supported: speechSupported(), mode, text, target, startTalk: () => start('talk'), stop, cancel, holdSpace: prefs.holdSpace };
}
