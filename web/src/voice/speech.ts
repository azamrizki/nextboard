// How spoken lines sound (D12): one voice, in the language of what is said, one sentence for a
// burst of events, and only one tab of this browser talking. Before, every open tab spoke every
// line at once, English lines were read by the voice of the dictation language, and five agents
// finishing together made five clipped sentences.

/** The languages the short lines are written in; any other speaks English with an English voice. */
export type LineLang = 'en' | 'id';

export function lineLang(prefLang: string): LineLang {
  return prefLang.toLowerCase().startsWith('id') ? 'id' : 'en';
}

/** The language a line written in `written` is said in: the voice language's own region when it matches. */
export function sayIn(prefLang: string, written: LineLang): string {
  if (prefLang.toLowerCase().startsWith(written)) return prefLang;
  return written === 'id' ? 'id-ID' : 'en-US';
}

const TEXT = {
  en: { finished: (w: string) => `${w} finished.`, needsYou: (w: string, many: boolean) => `${w} ${many ? 'need' : 'needs'} you.`, and: 'and', others: (n: number) => `${n} others`, agent: 'An agent' },
  id: { finished: (w: string) => `${w} selesai.`, needsYou: (w: string) => `${w} butuh kamu.`, and: 'dan', others: (n: number) => `${n} lainnya`, agent: 'Seorang agen' },
} as const;

/** "Arkan", "Arkan and Bintang", "Arkan, Bintang and Cantika", "Arkan, Bintang and 3 others". */
export function joinNames(names: string[], lang: LineLang): string {
  const t = TEXT[lang];
  const list = [...new Set(names.filter(Boolean))];
  if (!list.length) return t.agent;
  if (list.length === 1) return list[0];
  if (list.length > 3) return `${list.slice(0, 2).join(', ')} ${t.and} ${t.others(list.length - 2)}`;
  return `${list.slice(0, -1).join(', ')} ${t.and} ${list[list.length - 1]}`;
}

/** One sentence for everyone who finished, or who needs you, in a burst. */
export function attentionLine(kind: 'finished' | 'needsYou', names: string[], lang: LineLang): string {
  const who = joinNames(names, lang);
  return kind === 'finished' ? TEXT[lang].finished(who) : TEXT[lang].needsYou(who, names.length > 1);
}

/**
 * A hub sentence made for reading, not for the ear: markdown marks, links, ids and paths go, and
 * a long one stops at the end of a sentence.
 */
export function speakable(text: string, max = 180): string {
  let s = text
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/`([^`]*)`/g, '$1')
    .replace(/\[([^\]]+)\]\([^)]*\)/g, '$1')
    .replace(/https?:\/\/\S+/g, 'a link')
    .replace(/\b[a-z]{1,3}_[a-z0-9]{6,}\b/g, '') // hub ids: n_…, s_…, p_…, pt_…
    .replace(/(?:~|\.{0,2})?(?:\/[\w.@+-]+){2,}\/?/g, (p) => p.split('/').filter(Boolean).pop() ?? '') // a path says its last name
    .replace(/[*_#>|]+/g, ' ')
    .replace(/\s*\n+\s*/g, '. ')
    .replace(/\s{2,}/g, ' ')
    .replace(/\s+([.,;:?!])/g, '$1')
    .replace(/([.?!])\.+/g, '$1')
    .trim();
  if (s.length <= max) return s;
  const cut = s.slice(0, max);
  const end = Math.max(cut.lastIndexOf('. '), cut.lastIndexOf('? '), cut.lastIndexOf('! '));
  s = end > max / 3 ? cut.slice(0, end + 1) : cut.slice(0, cut.lastIndexOf(' ')) + '…';
  return s;
}

/** What pickVoice needs of a SpeechSynthesisVoice. */
export interface VoiceLike {
  voiceURI: string;
  name: string;
  lang: string;
  localService: boolean;
  default?: boolean;
}

const NATURAL = /natural|neural|premium|enhanced|siri/i;

/**
 * The voice for a line in `lang` (BCP-47 or "en"): the one picked in Settings when it speaks that
 * language, else the smoothest there is: a voice of this device (a network voice of the browser
 * stalls between words), a natural one first, the exact region before the language alone.
 */
export function pickVoice<V extends VoiceLike>(voices: V[], lang: string, chosenURI: string): V | undefined {
  const base = lang.slice(0, 2).toLowerCase();
  const of = voices.filter((v) => v.lang.replace('_', '-').slice(0, 2).toLowerCase() === base);
  const chosen = of.find((v) => v.voiceURI === chosenURI);
  if (chosen) return chosen;
  const score = (v: V) =>
    (v.localService ? 8 : 0) + (NATURAL.test(v.name) ? 4 : 0) + (v.lang.replace('_', '-').toLowerCase() === lang.toLowerCase() ? 2 : 0) + (v.default ? 1 : 0);
  return [...of].sort((a, b) => score(b) - score(a))[0];
}

/**
 * Only one tab of this browser says a line. A visible tab claims it at once, a hidden one a moment
 * later and only when no tab did; the claim is kept in localStorage, which every tab of the origin
 * shares. Without storage every tab may talk, as before.
 */
export async function claimLine(key: string, visible: boolean, wait = (ms: number) => new Promise((r) => setTimeout(r, ms))): Promise<boolean> {
  const slot = `nb.tts.${key}`;
  const me = Math.random().toString(36).slice(2);
  try {
    if (!visible) await wait(350);
    const held = window.localStorage.getItem(slot);
    if (held && Date.now() - Number(held.split(' ')[1] ?? 0) < 15_000) return false;
    window.localStorage.setItem(slot, `${me} ${Date.now()}`);
    await wait(40); // a tab that wrote in the same moment: the last write wins
    const ok = window.localStorage.getItem(slot)?.startsWith(me + ' ') ?? true;
    sweepClaims();
    return ok;
  } catch {
    return true;
  }
}

function sweepClaims() {
  try {
    const now = Date.now();
    for (let i = window.localStorage.length - 1; i >= 0; i--) {
      const k = window.localStorage.key(i);
      if (!k?.startsWith('nb.tts.')) continue;
      const at = Number(window.localStorage.getItem(k)?.split(' ')[1] ?? 0);
      if (now - at > 60_000) window.localStorage.removeItem(k);
    }
  } catch {
    /* storage off */
  }
}
