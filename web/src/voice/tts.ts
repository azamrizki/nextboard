// Spoken replies (D12): short hub lines only — a confirmation asked for one of this
// client's orders, "Ash finished", "Ash needs you". Agent replies are never read out.
// speechSynthesis runs locally; off unless voice is on and "Confirm out loud" is on.
// How they sound is speech.ts: one sentence per burst, in the voice language when it is one the
// lines are written in, and one tab of the browser talking.

import { orchStore } from '../orch/store';
import { store } from '../store';
import { attentionLine, claimLine, lineLang, sayIn, speakable } from './speech';
import { getVoicePrefs, speak } from './voice';

let installed = false;
let lastSent = 0;

/** Events this close together are one sentence: "Arkan and Bintang finished." */
const BURST_MS = 700;

/** The command bar sent an order: a confirmation in the next moments is ours. */
export function noteOrderSent() {
  lastSent = Date.now();
}

const visible = () => typeof document === 'undefined' || document.visibilityState === 'visible';

export function installTts(): () => void {
  if (installed) return () => undefined;
  installed = true;
  const spoken = new Set<string>();
  const offOrch = orchStore.subscribe(() => {
    const st = orchStore.get();
    for (const c of Object.values(st.confirmations)) {
      if (c.state !== 'pending' || spoken.has(c.id) || (!st.pending[c.commandId] && Date.now() - lastSent > 15_000)) continue;
      spoken.add(c.id);
      const line = speakable(c.prompt);
      // the hub asks in English: an English voice says it
      if (line) void claimLine(`c.${c.id}`, visible()).then((mine) => mine && speak(line, sayIn(getVoicePrefs().lang, 'en')));
    }
  });

  const burst: Record<'finished' | 'needsYou', { ids: string[]; names: string[] }> = { finished: { ids: [], names: [] }, needsYou: { ids: [], names: [] } };
  let timer = 0;
  const flush = () => {
    timer = 0;
    const lang = lineLang(getVoicePrefs().lang);
    // who needs you first: it is the one to act on
    for (const kind of ['needsYou', 'finished'] as const) {
      const b = burst[kind];
      if (!b.ids.length) continue;
      burst[kind] = { ids: [], names: [] };
      // each event is claimed on its own: two tabs that grouped a burst a little differently
      // still never say the same agent twice
      const shown = visible();
      void Promise.all(b.ids.map((id) => claimLine(`a.${id}`, shown))).then((mine) => {
        const names = b.names.filter((_, i) => mine[i]);
        if (names.length) speak(attentionLine(kind, names, lang), sayIn(getVoicePrefs().lang, lang));
      });
    }
  };
  const offEv = store.onEvent((ev) => {
    if (ev.type !== 'attention') return;
    const a = ev.attention;
    if ((a.kind !== 'finished' && a.kind !== 'needsYou') || spoken.has(a.id)) return;
    spoken.add(a.id);
    if (spoken.size > 500) spoken.clear();
    burst[a.kind].ids.push(a.id);
    burst[a.kind].names.push(a.handle || a.title || '');
    if (!timer) timer = window.setTimeout(flush, BURST_MS);
  });
  return () => {
    offOrch();
    offEv();
    if (timer) window.clearTimeout(timer);
    installed = false;
  };
}
