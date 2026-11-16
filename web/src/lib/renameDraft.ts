// A rename field over a name that can change elsewhere while it is open (another tab, another
// device, the hub), as PartRow in home/PartsDialog.tsx does it: until it is typed in, the field
// shows the live name and leaving it saves nothing. Without that, F2 on "Main", a rename to "API"
// in another tab, then Enter here would save "Main" again.

import { useRef, useState } from 'react';

/** What leaving a rename field saves: nothing when it was not typed in, is empty, or says the live name. */
export function renameTo(typed: string | null, live: string, max = Infinity): string | null {
  if (typed === null) return null;
  const v = typed.trim().slice(0, max);
  return v && v !== live ? v : null;
}

export interface RenameDraft {
  /** What the field shows: what was typed, else the live name. */
  value: string;
  set: (v: string) => void;
  /** What was typed, null when the field was not typed in. Read now, not at the last render. */
  typed: () => string | null;
  /** Follow the live name again (the field opens, closes, or was saved). */
  reset: () => void;
}

export function useRenameDraft(live: string): RenameDraft {
  const [typed, setTyped] = useState<string | null>(null);
  // Enter, then the blur it causes: the second must see that the first took the draft
  const now = useRef<string | null>(null);
  const put = (v: string | null) => {
    now.current = v;
    setTyped(v);
  };
  return { value: typed ?? live, set: put, typed: () => now.current, reset: () => put(null) };
}
