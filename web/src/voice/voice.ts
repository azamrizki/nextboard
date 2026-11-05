// Voice (D11–D13, docs/API-studio.md §11): everything happens in this browser — the hub
// never receives audio. Speech recognition is the browser's Web Speech API, which in
// Chrome and Edge sends the audio to Google or Microsoft, so it is strictly opt-in
// (Settings › Voice). Preferences are per browser (localStorage).

import { useSyncExternalStore } from 'react';
import { prefs } from '../lib/format';

export interface VoicePrefs {
  enabled: boolean;
  /** BCP-47 language for recognition and speech. */
  lang: string;
  holdSpace: boolean;
  /** KeyboardEvent.code held for dictation. */
  dictationKey: string;
  ttsConfirmations: boolean;
  ttsVoice: string;
}

export const VOICE_DEFAULTS: VoicePrefs = { enabled: false, lang: 'en-US', holdSpace: true, dictationKey: 'AltRight', ttsConfirmations: true, ttsVoice: '' };
const KEY = 'voice.prefs';

function read(): VoicePrefs {
  try {
    return { ...VOICE_DEFAULTS, ...(JSON.parse(prefs.get(KEY) ?? '{}') as Partial<VoicePrefs>) };
  } catch {
    return VOICE_DEFAULTS;
  }
}

let cur = read();
const listeners = new Set<() => void>();
export const getVoicePrefs = () => cur;
export function setVoicePrefs(patch: Partial<VoicePrefs>) {
  cur = { ...cur, ...patch };
  prefs.set(KEY, JSON.stringify(cur));
  listeners.forEach((l) => l());
}
export function useVoicePrefs(): VoicePrefs {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    () => cur,
  );
}

// ---- Web Speech API (not in the TS DOM lib everywhere) ----

interface RecognitionResult {
  isFinal: boolean;
  0: { transcript: string };
}
interface RecognitionEvent {
  resultIndex: number;
  results: ArrayLike<RecognitionResult>;
}
export interface Recognition {
  lang: string;
  continuous: boolean;
  interimResults: boolean;
  start(): void;
  stop(): void;
  abort(): void;
  onresult: ((e: RecognitionEvent) => void) | null;
  onerror: ((e: { error: string }) => void) | null;
  onend: (() => void) | null;
}
type RecognitionCtor = new () => Recognition;

function ctor(): RecognitionCtor | null {
  const w = window as unknown as { SpeechRecognition?: RecognitionCtor; webkitSpeechRecognition?: RecognitionCtor };
  return w.SpeechRecognition ?? w.webkitSpeechRecognition ?? null;
}

export const speechSupported = () => !!ctor();
export const ttsSupported = () => typeof window.speechSynthesis !== 'undefined';

export interface Listening {
  /** Stop and deliver the final text. */
  stop(): void;
  /** Stop and throw the text away. */
  cancel(): void;
}

/**
 * Start listening. onText gets the running transcript (final + interim) while you
 * speak; onDone the final text once stopped (not after cancel). Errors: "not-allowed"
 * (microphone blocked), "no-speech", "network"…
 */
export function listen(o: { lang: string; onText: (text: string) => void; onDone: (text: string) => void; onError: (code: string) => void }): Listening | null {
  const C = ctor();
  if (!C) return null;
  const r = new C();
  r.lang = o.lang;
  r.continuous = true;
  r.interimResults = true;
  let finalText = '';
  let interim = '';
  let cancelled = false;
  let done = false;
  r.onresult = (e) => {
    interim = '';
    for (let i = e.resultIndex; i < e.results.length; i++) {
      const res = e.results[i];
      if (res.isFinal) finalText += res[0].transcript;
      else interim += res[0].transcript;
    }
    o.onText((finalText + interim).trim());
  };
  r.onerror = (e) => {
    if (e.error === 'aborted' && cancelled) return;
    o.onError(e.error);
  };
  r.onend = () => {
    if (done) return;
    done = true;
    if (!cancelled) o.onDone((finalText + interim).trim());
  };
  try {
    r.start();
  } catch {
    return null;
  }
  return {
    stop: () => {
      try {
        r.stop();
      } catch {
        /* already stopped */
      }
    },
    cancel: () => {
      cancelled = true;
      try {
        r.abort();
      } catch {
        /* already stopped */
      }
    },
  };
}

/** Spoken punctuation for dictation (English): "comma" → ",", "new line" → "\n". */
export function punctuate(text: string): string {
  return text
    .replace(/\s*\b(new line|newline)\b\s*/gi, '\n')
    .replace(/\s*\bquestion mark\b/gi, '?')
    .replace(/\s*\b(exclamation mark|exclamation point)\b/gi, '!')
    .replace(/\s*\b(full stop|period)\b/gi, '.')
    .replace(/\s*\bcomma\b/gi, ',')
    .replace(/\s*\bcolon\b/gi, ':')
    .replace(/([,.?!:])(?=[^\s\n])/g, '$1 ')
    .replace(/ +\n/g, '\n');
}

// ---- spoken replies (TTS) ----

export function speak(text: string) {
  const p = getVoicePrefs();
  if (!p.enabled || !p.ttsConfirmations || !ttsSupported() || !text.trim()) return;
  try {
    const u = new SpeechSynthesisUtterance(text.slice(0, 300));
    u.lang = p.lang;
    const v = window.speechSynthesis.getVoices().find((x) => x.voiceURI === p.ttsVoice);
    if (v) u.voice = v;
    window.speechSynthesis.speak(u);
  } catch {
    /* no voice output */
  }
}

/** Key names for the dictation key setting. */
export const DICTATION_KEYS: { code: string; label: string }[] = [
  { code: 'AltRight', label: 'Right Alt' },
  { code: 'ControlRight', label: 'Right Ctrl' },
  { code: 'MetaRight', label: 'Right ⌘ / Win' },
  { code: 'ShiftRight', label: 'Right Shift' },
  { code: 'F8', label: 'F8' },
  { code: 'F9', label: 'F9' },
];
