// Picks an app installed on This PC for a new tile (docs/API-gui.md §2). Apps that already
// run are marked: choosing one brings its running session to this board.

import { useEffect, useMemo, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { DEFAULT_SIZE } from '../board/kinds';
import type { Project } from '../lib/types';
import { guiApi, type GuiApp, type GuiCatalog, type GuiSession } from './api';
import { AppIcon } from './AppIcon';
import { appLaunchBody } from './model';
import { guiStore, openApp, useGui } from './store';

export function AppPicker({ project, onClose, onOpened }: { project: Project; onClose: () => void; onOpened: (app: GuiApp, session: GuiSession) => void }) {
  const [catalog, setCatalog] = useState<GuiCatalog | null>(null);
  const [query, setQuery] = useState('');
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const sessions = useGui((s) => s.sessions);

  useEffect(() => {
    void guiStore.load();
    guiApi
      .catalog()
      .then(setCatalog)
      .catch((err) => setError(errorMessage(err)));
  }, []);

  const running = useMemo(() => {
    const m = new Map<string, GuiSession>();
    for (const s of Object.values(sessions)) m.set(s.appId, s);
    return m;
  }, [sessions]);

  const apps = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = catalog?.apps ?? [];
    return q ? list.filter((a) => a.name.toLowerCase().includes(q) || a.comment?.toLowerCase().includes(q)) : list;
  }, [catalog, query]);

  const pick = async (app: GuiApp) => {
    setBusy(app.id);
    setError('');
    try {
      const size = DEFAULT_SIZE.app;
      onOpened(app, await openApp(appLaunchBody(app.id, project, app.acceptsFolder, size.w, size.h)));
    } catch (err) {
      setError(errorMessage(err));
      setBusy('');
    }
  };

  const stop = (s: GuiSession) => {
    guiApi.stop(s.id).catch((err) => setError(errorMessage(err)));
  };

  return (
    <Modal
      title="Open a desktop app"
      icon="monitor"
      onClose={onClose}
      help="Runs on This PC on its own display, with a separate NextBoard profile for each app."
      footer={<Button onClick={onClose}>Close</Button>}
    >
      {catalog && !catalog.available ? (
        <div className="app-missing">
          <p>{catalog.missing.length ? `This PC needs ${catalog.missing.join(' and ')} to show desktop apps on a board.` : catalog.hint}</p>
          {catalog.missing.length > 0 && catalog.hint && <code className="mono">{catalog.hint}</code>}
        </div>
      ) : (
        <>
          <div className="app-search">
            <Icon name="search" />
            <input className="control control-sm" placeholder="Search apps" aria-label="Search apps" autoFocus value={query} onChange={(e) => setQuery(e.target.value)} />
          </div>
          {!catalog && !error && <p className="app-note">Looking for apps…</p>}
          {catalog && apps.length === 0 && <p className="app-note">No apps found.</p>}
          <ul className="app-list" aria-label="Desktop apps">
            {apps.map((a) => {
              const s = running.get(a.id);
              return (
                <li key={a.id} className="app-row">
                  <button type="button" className="app-pick" disabled={!!busy} title={a.comment} onClick={() => void pick(a)}>
                    <AppIcon appId={a.id} className="app-icon" />
                    <span className="app-name truncate">{a.name}</span>
                    {a.comment && <span className="app-comment truncate">{a.comment}</span>}
                    {busy === a.id && <span className="app-state">Opening…</span>}
                    {s && busy !== a.id && <span className="app-state">{s.state === 'stopping' ? 'Closing…' : 'Open'}</span>}
                  </button>
                  {s && s.state !== 'stopping' && <Button size="sm" variant="ghost" iconOnly icon="power" aria-label={`Close ${a.name}`} title={`Close ${a.name}`} onClick={() => stop(s)} />}
                </li>
              );
            })}
          </ul>
        </>
      )}
      {error && (
        <p className="app-error" role="alert">
          {error}
        </p>
      )}
    </Modal>
  );
}
