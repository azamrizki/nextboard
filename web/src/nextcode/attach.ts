// Files in a NextCode message (docs/API-studio.md §7.9): each is saved on This PC by POST
// /api/sessions/{id}/attach, and the message carries where they are on a final line, which Claude
// Code and Codex read by path. The chat shows that line as chips again. Pure, so tests can read it.

const LINE = 'Attached: ';

/** The message the agent gets: what was typed, then a final line with the files' paths. */
export function attachedMessage(text: string, paths: string[]): string {
  const t = text.trim();
  if (!paths.length) return t;
  const line = `${LINE}${paths.join(', ')}`;
  return t ? `${t}\n\n${line}` : line;
}

export interface AttachedPath {
  path: string;
  name: string;
}

/** Where the hub saves a console's files: its attachments folder on This PC, a paste folder in /tmp on a server. */
const SAVED = /\/attachments\/|\/nb-paste-[^/]+\//;

/**
 * A message's text without its final "Attached:" line, and the files it names. Only a line of
 * paths in an attachments folder counts, so a sentence that starts that way stays text.
 */
export function splitAttachedLine(text: string): { text: string; files: AttachedPath[] } {
  const at = text.lastIndexOf(LINE);
  if (at < 0 || (at > 0 && text[at - 1] !== '\n')) return { text, files: [] };
  const rest = text.slice(at + LINE.length).trim();
  // the line is the last one
  if (rest.includes('\n')) return { text, files: [] };
  const paths = rest.split(', ').map((p) => p.trim());
  if (!paths.length || paths.some((p) => !p.startsWith('/') || !SAVED.test(p))) return { text, files: [] };
  return { text: text.slice(0, at).trimEnd(), files: paths.map((path) => ({ path, name: path.slice(path.lastIndexOf('/') + 1) })) };
}
