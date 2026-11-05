// Spoken replies (D12): short hub lines only — a confirmation asked for one of this
// client's orders, "Ash finished", "Ash needs you". Agent replies are never read out.
// speechSynthesis runs locally; off unless voice is on and "Confirm out loud" is on.

import { orchStore } from '../orch/store';
import { store } from '../store';
import { speak } from './voice';

let installed = false;
let lastSent = 0;

/** The command bar sent an order: a confirmation in the next moments is ours. */
export function noteOrderSent() {
  lastSent = Date.now();
}

export function installTts(): () => void {
  if (installed) return () => undefined;
  installed = true;
  const spoken = new Set<string>();
  const offOrch = orchStore.subscribe(() => {
    const st = orchStore.get();
    for (const c of Object.values(st.confirmations)) {
      if (c.state !== 'pending' || spoken.has(c.id) || (!st.pending[c.commandId] && Date.now() - lastSent > 15_000)) continue;
      spoken.add(c.id);
      speak(c.prompt);
    }
  });
  const offEv = store.onEvent((ev) => {
    if (ev.type !== 'attention') return;
    const a = ev.attention;
    const who = a.handle ? a.handle : a.title || 'An agent';
    if (a.kind === 'finished') speak(`${who} finished.`);
    else if (a.kind === 'needsYou') speak(`${who} needs you.`);
  });
  return () => {
    offOrch();
    offEv();
    installed = false;
  };
}
