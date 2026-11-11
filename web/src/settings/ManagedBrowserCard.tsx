// Settings › Previews & studios: NextBoard Chromium (docs/API-gui.md §8.3), the browser web tiles
// start first once it is downloaded. Chrome for Testing of the stable channel: Chromium with
// Chrome's media codecs, which still takes the passkey extension by itself. The hub keeps it up
// to date; the owner may have to allow its sandbox once on Ubuntu.

import { useEffect, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Icon } from '../components/Icon';
import { SettingRow, SwitchRow } from '../components/Switch';
import { toast } from '../components/Toast';
import { guiApi, type BrowserStatus } from '../gui/api';
import { timeAgo } from '../lib/format';
import { store } from '../store';

function mb(n: number) {
  return `${Math.round(n / (1 << 20))} MB`;
}

function useBrowserStatus() {
  const [st, setSt] = useState<BrowserStatus | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    let live = true;
    guiApi
      .browser()
      .then((s) => live && setSt(s))
      .catch((err) => live && setError(errorMessage(err)));
    const off = store.onEvent((ev) => {
      const e = ev as { type: string; browser?: BrowserStatus };
      if (e.type === 'guiBrowser' && e.browser) setSt(e.browser);
    });
    return () => {
      live = false;
      off();
    };
  }, []);
  return { st, setSt, error };
}

export function ManagedBrowserCard() {
  const { st, setSt, error } = useBrowserStatus();
  const [removing, setRemoving] = useState(false);
  const run = async (what: string, fn: () => Promise<BrowserStatus>) => {
    try {
      setSt(await fn());
    } catch (err) {
      toast.error(`Could not ${what}: ${errorMessage(err)}`);
    }
  };
  if (error) return null; // a hub without the endpoint
  if (!st) return null;

  const busy = st.state === 'checking' || st.state === 'downloading' || st.state === 'installing';
  const update = st.installed && st.latest && st.latest !== st.installed && newer(st.latest, st.installed);
  let line: string;
  if (st.state === 'downloading') line = st.total ? `Downloading… ${mb(st.done ?? 0)} of ${mb(st.total)}` : `Downloading… ${mb(st.done ?? 0)}`;
  else if (st.state === 'installing') line = 'Unpacking and checking it…';
  else if (st.state === 'checking') line = 'Asking which release is the latest…';
  else if (st.installed) line = `Version ${st.installed}${update ? ` · ${st.latest} is out` : st.checkedAt ? ` · up to date, checked ${timeAgo(st.checkedAt)}` : ''}`;
  else line = 'Not downloaded. Web tiles use the browser installed on This PC.';

  return (
    <section className="card settings-card" aria-labelledby="st-browser">
      <h3 id="st-browser" className="section-title">
        NextBoard Chromium
      </h3>
      <p className="setting-hint">
        A browser for web tiles that NextBoard downloads and keeps up to date: Google’s Chrome for Testing of the stable channel, which is Chromium with Chrome’s video and audio codecs.
        Tiles use it first once it is here, and it loads the passkey extension by itself. It weighs about 170 MB to download and 400 MB on disk; your own Chrome stays as it is.
      </p>
      {!st.supported ? (
        <p className="setting-hint">Only on Linux, on x86-64 or ARM64, where web tiles run.</p>
      ) : (
        <>
          <SettingRow title="Browser" hint={<span role="status">{line}</span>}>
            <div className="inline-controls">
              {!st.installed && (
                <Button size="sm" variant="primary" icon="download" loading={busy} disabled={busy} onClick={() => void run('start the download', guiApi.installBrowser)}>
                  Download
                </Button>
              )}
              {st.installed && update && (
                <Button size="sm" variant="primary" icon="download" loading={busy} disabled={busy} onClick={() => void run('update it', guiApi.installBrowser)}>
                  Update now
                </Button>
              )}
              {st.installed && !update && (
                <Button size="sm" icon="refresh" loading={busy} disabled={busy} onClick={() => void run('check for a new release', guiApi.checkBrowser)}>
                  Check now
                </Button>
              )}
              {st.installed && (
                <Button size="sm" variant="danger-ghost" iconOnly icon="trash" aria-label="Remove NextBoard Chromium" disabled={busy} onClick={() => setRemoving(true)} />
              )}
            </div>
          </SettingRow>
          {st.state === 'downloading' && st.total ? (
            <div className="browser-progress" role="progressbar" aria-label="Download" aria-valuemin={0} aria-valuemax={st.total} aria-valuenow={st.done ?? 0}>
              <span style={{ width: `${Math.min(100, ((st.done ?? 0) / st.total) * 100)}%` }} />
            </div>
          ) : null}
          {st.state === 'error' && st.error && (
            <p className="field-error" role="alert">
              <Icon name="alertCircle" />
              {st.error}
            </p>
          )}
          <SwitchRow
            title="Keep it up to date"
            hint="Checks once a day and downloads a new stable release, as Chrome updates itself. Tiles already open keep their version until you close them."
            checked={st.autoUpdate}
            onChange={(on) => void run('change updates', () => guiApi.setBrowserAutoUpdate(on))}
          />
          {st.sandbox === 'setup' && (
            <div className="alert warning" role="note">
              <Icon name="alert" />
              <div>
                <strong>One step before it can start.</strong> Ubuntu lets a browser make its sandbox only when an AppArmor profile names it, and a downloaded browser has none. NextBoard never
                runs it without the sandbox. Run this once in a terminal on This PC, or install Google Chrome or Chromium from a package, whose sandbox helper it can use:
                <code className="setup-command">{st.setupCommand}</code>
              </div>
            </div>
          )}
          {st.sandbox === 'helper' && st.installed && <p className="setting-hint">Sandboxed with the helper of the browser installed at {st.sandboxHelper?.replace(/\/[^/]+$/, '')}.</p>}
        </>
      )}
      {removing && (
        <ConfirmDialog
          title="Remove NextBoard Chromium?"
          confirmLabel="Remove"
          danger
          onClose={() => setRemoving(false)}
          onConfirm={async () => {
            await run('remove it', guiApi.removeBrowser);
            setRemoving(false);
          }}
        >
          Web tiles go back to the browser installed on This PC. The logins of tiles stay in their profiles, and download it again to use them.
        </ConfirmDialog>
      )}
    </section>
  );
}

function newer(a: string, b: string) {
  const pa = a.split('.').map(Number);
  const pb = b.split('.').map(Number);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    if ((pa[i] ?? 0) !== (pb[i] ?? 0)) return (pa[i] ?? 0) > (pb[i] ?? 0);
  }
  return false;
}
