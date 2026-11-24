// A database tab's part of the Code view's status bar (mockups 1, 2, 4): the connection, how it
// may be used, and what the tab is about. Light: the status bar is in the main chunk.

import { useDbStatus } from './status';

export function DbStatusLeft() {
  const s = useDbStatus();
  if (!s) return null;
  return (
    <span className="wb-status-item db-status-l" title={`${s.conn} · ${s.where} · ${s.mode}`}>
      <span className={`dot ${s.dot === 'ok' ? 'dot-success' : s.dot === 'err' ? 'dot-danger' : 'dot-grey'}`} aria-hidden="true" />
      <b>{s.conn}</b>
      <span className="truncate">· {s.where}</span>
      <span className="db-status-mode">{s.mode}</span>
    </span>
  );
}

export function DbStatusRight() {
  const s = useDbStatus();
  if (!s?.right) return null;
  return <span className="wb-status-item db-status-r">{s.right}</span>;
}
