// Usage-reset chime (F3, docs/API-studio.md §10.2): the hub sends "usageReset" shortly
// after a window resets (when the Play rule allows); open tabs play a short sound made
// on the fly with Web Audio (NextBoard's own sounds, no files) and show a toast.

import { store } from '../store';
import { studioStore } from '../studio/store';
import { toast } from '../components/Toast';

export type ChimeSound = 'bell' | 'chime' | 'pop' | 'soft';

let ctx: AudioContext | null = null;
function audio(): AudioContext | null {
  try {
    ctx ??= new AudioContext();
    if (ctx.state === 'suspended') void ctx.resume();
    return ctx;
  } catch {
    return null;
  }
}

function tone(ac: AudioContext, freq: number, start: number, dur: number, gain: number, type: OscillatorType = 'sine') {
  const o = ac.createOscillator();
  const g = ac.createGain();
  o.type = type;
  o.frequency.setValueAtTime(freq, ac.currentTime + start);
  g.gain.setValueAtTime(0.0001, ac.currentTime + start);
  g.gain.exponentialRampToValueAtTime(gain, ac.currentTime + start + 0.015);
  g.gain.exponentialRampToValueAtTime(0.0001, ac.currentTime + start + dur);
  o.connect(g).connect(ac.destination);
  o.start(ac.currentTime + start);
  o.stop(ac.currentTime + start + dur + 0.05);
}

/** Play one of the bundled sounds; false when the browser blocks audio. */
export function playChime(sound: ChimeSound | string = 'bell'): boolean {
  const ac = audio();
  if (!ac || ac.state !== 'running') {
    // a suspended context starts after the first click on the page
    if (ac) void ac.resume();
    if (!ac || ac.state !== 'running') return false;
  }
  switch (sound) {
    case 'chime':
      tone(ac, 659.25, 0, 0.7, 0.18);
      tone(ac, 987.77, 0.16, 0.9, 0.16);
      break;
    case 'pop':
      tone(ac, 420, 0, 0.12, 0.25, 'triangle');
      tone(ac, 640, 0.04, 0.1, 0.12, 'sine');
      break;
    case 'soft':
      tone(ac, 392, 0, 1.2, 0.12);
      tone(ac, 523.25, 0.08, 1.2, 0.08);
      break;
    default:
      // bell: fundamental + inharmonic partials, long decay
      tone(ac, 880, 0, 1.6, 0.18);
      tone(ac, 880 * 2.76, 0, 0.9, 0.05);
      tone(ac, 880 * 5.4, 0, 0.5, 0.02);
  }
  return true;
}

let installed = false;
/** Listen for usageReset events (once per login). Unlocks audio on the first click. */
export function installChime(): () => void {
  if (installed) return () => undefined;
  installed = true;
  const unlock = () => audio();
  window.addEventListener('pointerdown', unlock, { once: true, capture: true });
  const onReset = (e: { agent: string; window: string; sound?: string }) => {
    const chime = (store.getState().settings as { usage?: { chime?: { enabled?: boolean; sound?: string } } } | null)?.usage?.chime;
    if (chime?.enabled === false) return;
    playChime(e.sound ?? chime?.sound ?? 'bell');
    const name = e.agent === 'codex' ? 'Codex' : e.agent === 'claude' ? 'Claude' : e.agent;
    toast.notify('success', `${name} usage reset`, `The ${e.window || 'usage'} window started over.`);
  };
  studioStore.onReset.add(onReset);
  return () => {
    studioStore.onReset.delete(onReset);
    window.removeEventListener('pointerdown', unlock, true);
    installed = false;
  };
}
