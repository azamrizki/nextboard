// Download a file of a machine through the browser (the Code view's menus): its saved bytes,
// under its own name. The hub's fs/raw with download=1 says "attachment" with the name, so a
// text file is saved, not shown; the session cookie carries the login, as for pictures.

import { fsRawUrl } from '../api';

/** The hub serves at most this much from fs/raw (internal/machine MaxFileBytes). */
export const MAX_DOWNLOAD = 20 * 1024 * 1024;

/** The URL that saves the file instead of showing it. */
export const downloadUrl = (machineId: string, path: string) => `${fsRawUrl(machineId, path)}&download=1`;

export function downloadMachineFile(machineId: string, path: string) {
  const a = document.createElement('a');
  a.href = downloadUrl(machineId, path);
  a.download = path.split('/').pop() || 'download';
  a.rel = 'noopener';
  a.style.display = 'none';
  document.body.appendChild(a);
  a.click();
  a.remove();
}
