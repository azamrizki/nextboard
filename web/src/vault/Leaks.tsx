// Checking passwords against known breaches (breach.ts): one login's from its page, or every
// login's at once. Only when asked.

import { useState } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { BREACH_NOTE, pwnedCount } from './breach';
import { byName, type VaultItem } from './model';

const times = (n: number) => `${n.toLocaleString()} ${n === 1 ? 'time' : 'times'}`;

/** On a login's page. */
export function LeakCheck({ password }: { password: string }) {
  const [state, setState] = useState<{ busy?: boolean; count?: number; error?: string }>({});
  const check = async () => {
    setState({ busy: true });
    try {
      setState({ count: await pwnedCount(password) });
    } catch (err) {
      setState({ error: errorMessage(err) });
    }
  };
  return (
    <div className="vault-leak">
      {state.count === undefined && !state.error ? (
        <Button size="sm" icon="shieldAlert" loading={state.busy} title={BREACH_NOTE} onClick={() => void check()}>
          Check for leaks
        </Button>
      ) : state.error ? (
        <p className="vault-note" role="alert">
          Could not ask Have I Been Pwned: {state.error}
        </p>
      ) : state.count ? (
        <p className="vault-leak-bad" role="status">
          <Icon name="alert" /> Seen {times(state.count)} in data breaches. Change this password.
        </p>
      ) : (
        <p className="vault-note" role="status">
          <Icon name="check" /> Not in any known breach.
        </p>
      )}
    </div>
  );
}

/** Every login with a password. Duplicates are asked once. */
export function LeaksDialog({ items, onShow, onClose }: { items: VaultItem[]; onShow: (id: string) => void; onClose: () => void }) {
  const logins = items.filter((it) => it.type === 'login' && !it.deletedAt && it.values.password).sort(byName);
  const [results, setResults] = useState<Map<string, number> | null>(null);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(0);
  const [error, setError] = useState('');

  const run = async () => {
    setBusy(true);
    setError('');
    setDone(0);
    const cache = new Map<string, string>();
    const out = new Map<string, number>();
    try {
      for (const it of logins) {
        out.set(it.id, await pwnedCount(it.values.password, fetch, cache));
        setDone((n) => n + 1);
      }
      setResults(out);
    } catch (err) {
      setError(`Could not ask Have I Been Pwned: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };

  const leaked = results ? logins.filter((it) => (results.get(it.id) ?? 0) > 0) : [];
  return (
    <Modal
      title="Check passwords for leaks"
      icon="shieldAlert"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Close</Button>
          <Button variant="primary" loading={busy} disabled={!logins.length} onClick={() => void run()}>
            {results ? 'Check again' : `Check ${logins.length} ${logins.length === 1 ? 'password' : 'passwords'}`}
          </Button>
        </>
      }
    >
      <div className="vault-form-stack">
        <p className="vault-note">{BREACH_NOTE}</p>
        {busy && (
          <p className="vault-note" role="status">
            Checked {done} of {logins.length}…
          </p>
        )}
        {error && (
          <div className="alert" role="alert">
            {error}
          </div>
        )}
        {results && !leaked.length && (
          <p className="vault-note" role="status">
            <Icon name="check" /> None of them is in a known breach.
          </p>
        )}
        {leaked.length > 0 && (
          <>
            <p className="vault-leak-bad" role="status">
              <Icon name="alert" /> {leaked.length} {leaked.length === 1 ? 'password was' : 'passwords were'} seen in data breaches. Change them.
            </p>
            <ul className="plain vault-fill-list" aria-label="Leaked passwords">
              {leaked.map((it) => (
                <li key={it.id} className="vault-fill-row">
                  <span className="vault-row-text">
                    <span className="vault-row-name truncate">{it.name}</span>
                    <span className="vault-row-sub truncate">Seen {times(results!.get(it.id)!)}</span>
                  </span>
                  <Button
                    size="sm"
                    onClick={() => {
                      onShow(it.id);
                      onClose();
                    }}
                  >
                    Show
                  </Button>
                </li>
              ))}
            </ul>
          </>
        )}
      </div>
    </Modal>
  );
}
