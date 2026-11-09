// Passkeys of other sites (docs/API-vault.md): the dialog a site in a browser tile waits on, and the
// settings that turn them on. The owner answers each request. The key signs here, in this browser.

import { useEffect, useMemo, useState } from 'react';
import { errorMessage, isCode } from '../api';
import { Button } from '../components/Button';
import { Checkbox } from '../components/form';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { store } from '../store';
import { ResetDialog } from './Dialogs';
import { copyValue } from './fields';
import { VaultGate } from './Gate';
import { emptyItem, type StoredPasskey, type VaultItem } from './model';
import { createPasskey, passkeyApi, signWithPasskey, type PasskeyAskCreate, type PasskeyAskGet, type PasskeyRequest, type PasskeyStatus } from './passkeys';
import { saveItem, touchVault, useVault } from './store';

const hostOf = (origin: string) => {
  try {
    return new URL(origin).host;
  } catch {
    return origin;
  }
};

interface Held {
  item: VaultItem;
  key: StoredPasskey;
}

/** The passkeys of this site a request can use. A list from the page narrows them, never widens. */
function held(items: VaultItem[], req: PasskeyRequest): Held[] {
  const allowed = (req.options as PasskeyAskGet).allowCredentials?.map((c) => c.id) ?? [];
  const out: Held[] = [];
  for (const item of items) {
    if (item.deletedAt) continue;
    for (const key of item.passkeys ?? []) {
      if (key.rpId === req.rpId && (!allowed.length || allowed.includes(key.credentialId))) out.push({ item, key });
    }
  }
  return out;
}

/** The login a new passkey belongs to: the one for this account on this site, or a new one. */
function loginFor(items: VaultItem[], req: PasskeyRequest): VaultItem {
  const o = req.options as PasskeyAskCreate;
  const site = (u: string) => {
    try {
      const h = new URL(u).hostname;
      return h === req.rpId || h.endsWith(`.${req.rpId}`);
    } catch {
      return false;
    }
  };
  const same = items.find((it) => !it.deletedAt && it.type === 'login' && it.values.username === o.user.name && it.uris.some(site));
  if (same) return same;
  const it = emptyItem('login', '');
  it.name = o.rp.name || req.rpId;
  it.values = { username: o.user.name, password: '', totp: '' };
  it.uris = [`https://${req.rpId}`];
  return it;
}

function RequestDialog({ request: r }: { request: PasskeyRequest }) {
  const v = useVault();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [reset, setReset] = useState(false);
  const unlocked = v.status === 'unlocked';
  const creating = r.kind === 'create';
  const site = hostOf(r.origin);
  const keys = useMemo(() => (unlocked && !creating ? held(v.items, r) : []), [unlocked, creating, v.items, r]);
  const o = r.options as PasskeyAskCreate;
  const already = useMemo(() => {
    if (!unlocked || !creating) return false;
    const ids = (o.excludeCredentials ?? []).map((c) => c.id);
    return v.items.some((it) => !it.deletedAt && it.passkeys?.some((k) => ids.includes(k.credentialId)));
  }, [unlocked, creating, v.items, o]);

  const say = async (body: Parameters<typeof passkeyApi.answer>[1]) => {
    setBusy(true);
    setError('');
    try {
      await passkeyApi.answer(r.id, body);
    } catch (err) {
      setError(isCode(err, 'request_gone') ? 'The site stopped waiting for this.' : errorMessage(err));
      setBusy(false);
    }
  };

  const save = async () => {
    setBusy(true);
    setError('');
    let saved: VaultItem | null = null;
    let before: StoredPasskey[] = [];
    try {
      const made = createPasskey(r);
      const item = loginFor(v.items, r);
      before = item.passkeys ?? [];
      saved = await saveItem({ ...item, passkeys: [...before, { ...made.stored, createdAt: new Date().toISOString() }] });
      await passkeyApi.answer(r.id, { result: made.result });
      toast.success(`The passkey for ${site} is in the vault.`);
    } catch (err) {
      // The site never got it, so a key in the vault would be one nothing knows.
      if (saved) await saveItem({ ...saved, passkeys: before.length ? before : undefined }).catch(() => undefined);
      setError(isCode(err, 'request_gone') ? 'The site stopped waiting for this. Nothing was saved.' : errorMessage(err));
      setBusy(false);
    }
  };

  const useKey = async (h: Held) => {
    setBusy(true);
    setError('');
    try {
      await passkeyApi.answer(r.id, { result: signWithPasskey(r, h.key) });
    } catch (err) {
      setError(isCode(err, 'request_gone') ? 'The site stopped waiting for this.' : errorMessage(err));
      setBusy(false);
    }
  };

  const title = creating ? `Save a passkey for ${site}?` : `Sign in to ${site} with a passkey?`;
  return (
    <Modal
      title={title}
      size="sm"
      onClose={() => void say({ fallback: true })}
      footer={
        <>
          <Button variant="danger-ghost" disabled={busy} onClick={() => void say({ refuse: true })}>
            Refuse
          </Button>
          <Button disabled={busy} onClick={() => void say({ fallback: true })}>
            Use the browser’s own
          </Button>
          {creating && unlocked && (
            <Button variant="primary" loading={busy} disabled={already} onClick={() => void save()}>
              Save in vault
            </Button>
          )}
        </>
      }
    >
      <div className="vault-approve" onPointerDownCapture={touchVault} onKeyDownCapture={touchVault} data-testid="passkey-request">
        <dl className="vault-approve-facts">
          <dt>Site</dt>
          <dd className="mono">{r.origin}</dd>
          {creating && (
            <>
              <dt>Account</dt>
              <dd>{o.user.name || 'No name given'}</dd>
            </>
          )}
        </dl>
        {!unlocked && <VaultGate v={v} onReset={() => setReset(true)} />}
        {already && <div className="alert">The vault already holds a passkey this site knows, so it asked not to make another.</div>}
        {unlocked && !creating && !keys.length && <div className="alert">The vault has no passkey for this site.</div>}
        {keys.map((h) => (
          <Button key={h.key.credentialId} variant="primary" loading={busy} onClick={() => void useKey(h)}>
            Sign in as {h.key.userName || h.item.name}
          </Button>
        ))}
        {error && (
          <div className="alert" role="alert">
            {error}
          </div>
        )}
        <p className="vault-note">Only approve this if you just did something on that site. The name above comes from the browser, not from the page.</p>
      </div>
      {reset && <ResetDialog count={null} onClose={() => setReset(false)} />}
    </Modal>
  );
}

/** Mounted once while logged in. A site is waiting, so its request opens by itself. */
export function PasskeyRequests() {
  const [items, setItems] = useState<PasskeyRequest[]>([]);
  useEffect(() => {
    let alive = true;
    const load = () =>
      passkeyApi
        .status()
        .then((s) => alive && setItems(s.requests ?? []))
        .catch(() => undefined);
    void load();
    const off = store.onEvent((ev) => {
      if (ev.type === 'passkeyRequest' || ev.type === 'snapshot') void load();
    });
    return () => {
      alive = false;
      off();
    };
  }, []);
  const current = items[0];
  return current ? <RequestDialog key={current.id} request={current} /> : null;
}

export function SitePasskeysDialog({ onClose }: { onClose: () => void }) {
  const [s, setS] = useState<PasskeyStatus | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const load = () =>
    passkeyApi
      .status()
      .then(setS)
      .catch((err: unknown) => setError(errorMessage(err)));

  useEffect(() => {
    void load();
    return store.onEvent((ev) => {
      if (ev.type === 'passkeyRequest') void load();
    });
  }, []);

  const toggle = async (on: boolean) => {
    setBusy(true);
    setError('');
    try {
      await passkeyApi.setEnabled(on);
      await load();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal title="Passkeys for sites" size="md" onClose={onClose}>
      <div className="vault-dialog" data-testid="site-passkeys">
        <p className="vault-note">Lets sites in a browser tile keep their passkeys in this vault. Each use asks you here, and the key signs in this browser: the hub only passes the answer on.</p>
        <Checkbox checked={!!s?.enabled} disabled={busy || !s} onChange={(on: boolean) => void toggle(on)} label="Keep passkeys of sites in the vault" />
        {s?.enabled && s.active && <div className="alert" role="status">The browser’s extension is running.</div>}
        {s?.enabled && !s.active && (
          <div className="stack-sm">
            <p className="vault-note">Chromium, and the browsers built on it that still allow this, take the extension by themselves the next time they start. Google Chrome only takes it by hand, once:</p>
            <ol className="vault-steps">
              <li>
                In the browser tile, open a new tab and go to <code className="mono">chrome://extensions</code>.
              </li>
              <li>Turn on Developer mode, top right.</li>
              <li>Choose Load unpacked, press Ctrl+L in the file dialog, and paste this folder:</li>
            </ol>
            <div className="row-gap">
              <code className="truncate mono fs-xs">{s.path}</code>
              <Button size="sm" icon="copy" onClick={() => void copyValue(s.path ?? '', 'Folder', false)}>
                Copy
              </Button>
            </div>
          </div>
        )}
        <p className="vault-note">
          Works in a browser tile of the Chromium family, not in Firefox or in your own browser. Sites that offer passkeys only through autofill stay with the browser’s own. Programs on This PC can read the extension’s token and send a request that names any site, as they can with the rest of this hub, so
          approve only what you just did.
        </p>
        {error && (
          <div className="alert" role="alert">
            {error}
          </div>
        )}
      </div>
    </Modal>
  );
}
