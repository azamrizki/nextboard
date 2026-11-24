// What the Code view's status bar says while a database tab is in front (as the mockup's bottom
// line): the connection on the left ("● shop_db · MySQL 8.0.36 · This PC", "Local · read and
// write"), and on the right what the tab is about ("Auto-commit off for edits", "SQL · MySQL
// dialect"). The tab in front sets it; nothing heavy is imported, the status bar is in the main chunk.

import { useSyncExternalStore } from 'react';

export interface DbStatusLine {
  /** the tab that set it: only that tab's line shows */
  tab: string;
  dot: 'ok' | 'err' | 'off';
  conn: string;
  where: string;
  mode: string;
  right: string;
}

let line: DbStatusLine | null = null;
const listeners = new Set<() => void>();

export function setDbStatus(next: DbStatusLine | null) {
  if (JSON.stringify(next) === JSON.stringify(line)) return;
  line = next;
  listeners.forEach((l) => l());
}

/** Forget the line a tab set, when it goes. */
export function clearDbStatus(tab: string) {
  if (line?.tab === tab) setDbStatus(null);
}

export function useDbStatus(): DbStatusLine | null {
  return useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => {
        listeners.delete(fn);
      };
    },
    () => line,
  );
}
