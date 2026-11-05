// Filling a login into a browser tile, only ever when the person picks one here. Two ways in, as
// the tiles differ:
//   page      a site through the hub's proxy: the bridge sets the fields, and the hub knows which
//             site it is, so a login saved for another one takes a second click
//   keyboard  the real browser on This PC: pasted into whatever has focus there (gui/viewer.ts).
//             Nothing here can see which site that is, so the person is told to look
// Never on page load: a page that gets filled by itself can collect without anyone noticing.

import { useMemo, useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { ResetDialog } from './Dialogs';
import { VaultGate } from './Gate';
import { byName, hostOf, isFillable, matchesQuery, matchesSite, matchesTitle, type VaultItem } from './model';
import { touchVault, useVault } from './store';
import { parseTotp, totpCode } from './totp';

export interface FillValues {
  username?: string;
  password?: string;
  code?: string;
}

/** Which kinds of field got their value. */
export type FillResult = { username: boolean; password: boolean; code: boolean };

export interface FillSink {
  mode: 'page' | 'keyboard';
  /** Where it goes: a host, or a window's title. */
  where: string;
  /** The site, when the hub knows it. */
  host?: string;
  port?: number;
  send: (values: FillValues) => Promise<FillResult>;
  /** Filled: let the person carry on in there. */
  onFilled?: () => void;
}

const SEARCH_FROM = 8;

function Row({ item, fits, sink, armed, onArm, onDone }: { item: VaultItem; fits: boolean; sink: FillSink; armed: boolean; onArm: () => void; onDone: (r: FillResult, want: FillValues) => void }) {
  const [busy, setBusy] = useState(false);
  const { username, password, totp } = item.values;
  const spec = useMemo(() => parseTotp(totp ?? ''), [totp]);

  const go = async (want: FillValues) => {
    // A login saved for another site, in a page whose site is known: that is how phishing looks.
    if (sink.mode === 'page' && !fits && !armed) {
      onArm();
      return;
    }
    setBusy(true);
    try {
      onDone(await sink.send(want), want);
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const saved = item.uris[0] ? hostOf(item.uris[0]) : 'no address';
  return (
    <li className={`vault-fill-row ${armed ? 'is-armed' : ''}`}>
      <span className="vault-row-text">
        <span className="vault-row-name truncate">{item.name}</span>
        <span className="vault-row-sub truncate">{[username, sink.mode === 'page' && !fits ? `saved for ${saved}` : ''].filter(Boolean).join(' · ') || ' '}</span>
      </span>
      {armed && <span className="vault-fill-warn">Not saved for {sink.host}. Fill anyway?</span>}
      <span className="vault-fill-actions">
        {spec && (
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon="clock"
            disabled={busy}
            aria-label={`Fill the code of ${item.name}`}
            title="Authenticator code"
            onClick={() => void totpCode(spec, Date.now()).then((code) => go({ code }))}
          />
        )}
        {username && <Button size="sm" variant="ghost" iconOnly icon="user" disabled={busy} aria-label={`Fill the username of ${item.name}`} title="Username only" onClick={() => void go({ username })} />}
        {password && <Button size="sm" variant="ghost" iconOnly icon="key" disabled={busy} aria-label={`Fill the password of ${item.name}`} title="Password only" onClick={() => void go({ password })} />}
        <Button size="sm" variant={armed ? 'danger' : 'primary'} loading={busy} disabled={!username && !password} aria-label={`Fill ${item.name}`} onClick={() => void go({ username, password })}>
          Fill
        </Button>
      </span>
    </li>
  );
}

export function FillDialog({ sink, onClose }: { sink: FillSink; onClose: () => void }) {
  const v = useVault();
  const [query, setQuery] = useState('');
  const [armed, setArmed] = useState('');
  const [reset, setReset] = useState(false);
  const [problem, setProblem] = useState('');

  const logins = useMemo(() => v.items.filter(isFillable), [v.items]);
  const fits = (it: VaultItem) => (sink.mode === 'page' ? matchesSite(it, sink.host ?? '', sink.port ?? 0) : matchesTitle(it, sink.where));
  const shown = logins.filter((it) => matchesQuery(it, query)).sort(byName);
  const first = shown.filter(fits);
  const rest = shown.filter((it) => !fits(it));

  const done = (r: FillResult, want: FillValues) => {
    const missing: string[] = [];
    if (want.username && !r.username) missing.push('username');
    if (want.password && !r.password) missing.push('password');
    if (want.code && !r.code) missing.push('code');
    if (!r.username && !r.password && !r.code) {
      setProblem(`There is no ${missing.join(' or ')} field showing on this page. Open the login form first.`);
      return;
    }
    // Sites that ask for the password on a second page are normal: that is not a failure.
    toast.success(missing.length ? `Filled. No ${missing.join(' or ')} field on this page yet: fill again on the next step.` : 'Filled. Check it, then submit.');
    onClose();
    sink.onFilled?.();
  };

  const row = (it: VaultItem) => <Row key={it.id} item={it} fits={fits(it)} sink={sink} armed={armed === it.id} onArm={() => setArmed(it.id)} onDone={done} />;
  const open = v.status === 'unlocked';

  return (
    <Modal title={sink.mode === 'page' ? `Fill a login on ${sink.where}` : 'Fill a login in the app'} icon="key" size="md" onClose={onClose}>
      <div className="vault-fill" onPointerDownCapture={touchVault} onKeyDownCapture={touchVault}>
        {!open ? (
          <VaultGate v={v} onReset={() => setReset(true)} />
        ) : (
          <>
            {sink.mode === 'keyboard' && (
              <div className="alert warning" role="note">
                <Icon name="alert" />
                <span>
                  Goes to <strong>{sink.where}</strong>. Click the first field in there before you press Fill, and check the address bar: from here nothing can tell which site it is. If it offers to save the password, say no.
                </span>
              </div>
            )}
            {logins.length >= SEARCH_FROM && <input className="control control-sm" type="search" placeholder="Search logins" aria-label="Search logins" autoFocus value={query} onChange={(e) => setQuery(e.target.value)} autoComplete="off" data-lpignore="true" data-1p-ignore="true" data-bwignore="true" />}
            {problem && (
              <div className="alert" role="alert">
                {problem}
              </div>
            )}
            {!logins.length ? (
              <p className="vault-note">There are no logins in the vault yet. Add one in a Credentials card.</p>
            ) : !shown.length ? (
              <p className="vault-note">Nothing matches.</p>
            ) : (
              <>
                {first.length > 0 && <h3 className="vault-fill-title">{sink.mode === 'page' ? 'For this site' : 'Looks like this page'}</h3>}
                {first.length > 0 && <ul className="plain vault-fill-list">{first.map(row)}</ul>}
                {rest.length > 0 && <h3 className="vault-fill-title">{first.length ? 'Other logins' : sink.mode === 'page' ? `Nothing saved for ${sink.host}` : 'Logins'}</h3>}
                {rest.length > 0 && <ul className="plain vault-fill-list">{rest.map(row)}</ul>}
              </>
            )}
          </>
        )}
      </div>
      {reset && <ResetDialog count={null} onClose={() => setReset(false)} />}
    </Modal>
  );
}

/** The key button of a browser tile. */
export function FillButton({ sink, disabled }: { sink: FillSink; disabled?: boolean }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button size="sm" variant="ghost" iconOnly icon="key" className="nodrag" disabled={disabled} aria-label="Fill a login" title="Fill a login from Credentials" onClick={() => setOpen(true)} />
      {open && <FillDialog sink={sink} onClose={() => setOpen(false)} />}
    </>
  );
}
