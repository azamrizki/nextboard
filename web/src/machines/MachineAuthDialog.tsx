// What an SSH connection asks of a person: the server's password, or the passphrase of a key.
// The hub stopped trying and waits for this, so a wrong answer costs one attempt, not a ban.

import { useEffect, useRef, useState, type FormEvent } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { Checkbox, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import type { Machine } from '../lib/types';
import { store } from '../store';

export function MachineAuthDialog({ machine: m, onClose }: { machine: Machine; onClose: () => void }) {
  // While an answer is on its way the machine waits for nothing; the question stays on screen.
  const last = useRef(m.needsAuth);
  if (m.needsAuth) last.current = m.needsAuth;
  const need = last.current;
  const [usePassword, setUsePassword] = useState(false);
  const [value, setValue] = useState('');
  const [remember, setRemember] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);

  // Answered from another device, or connected some other way.
  useEffect(() => {
    if (!busy && !m.needsAuth) onClose();
  }, [busy, m.needsAuth, onClose]);

  if (!need) return null;
  const kind = need.kind === 'passphrase' && usePassword ? 'password' : need.kind;
  const isPassword = kind === 'password';
  const label = isPassword ? 'Password' : 'Passphrase';

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!value) return;
    setBusy(true);
    setError('');
    try {
      const updated = await api.answerMachineAuth(m.id, { kind, value, remember });
      store.upsertMachine(updated);
      setValue('');
      setAttempt((n) => n + 1);
      if (updated.status === 'connected') {
        toast.success(`Connected to ${updated.name}.`);
        onClose();
      } else if (!updated.needsAuth) {
        // The answer was fine; the server is what's missing. The card says the rest.
        setError(updated.error || 'Could not connect.');
      } else if (updated.needsAuth.kind !== need.kind) {
        setUsePassword(false);
      }
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const refused = !usePassword && need.wrong ? `That ${isPassword ? 'password was refused' : 'passphrase doesn’t open this key'}. Try again.` : '';

  return (
    <Modal
      title={`${label} for ${m.name}`}
      icon="key"
      onClose={() => !busy && onClose()}
      dismissable={!busy}
      help="Sent to the hub only, and never shown again."
      footer={
        <>
          {need.kind === 'passphrase' && need.canPassword && (
            <Button
              variant="ghost"
              disabled={busy}
              onClick={() => {
                setUsePassword(!usePassword);
                setValue('');
                setAttempt((n) => n + 1);
              }}
            >
              {usePassword ? 'Use the key' : 'Use a password instead'}
            </Button>
          )}
          <Button onClick={onClose} disabled={busy}>
            Not now
          </Button>
          <Button type="submit" form="machine-auth-form" variant="primary" loading={busy} disabled={!value}>
            Connect
          </Button>
        </>
      }
    >
      <form id="machine-auth-form" className="form" onSubmit={(e) => void submit(e)} noValidate>
        <p className="text-2">
          {isPassword ? (
            <>
              <code>{need.host}</code> asks for a password.
            </>
          ) : need.keyPath ? (
            <>
              The key <code>{need.keyPath}</code> on the hub is locked. <code>{need.host}</code> may take it.
            </>
          ) : (
            <>
              The private key you added for <code>{need.host}</code> is locked.
            </>
          )}
        </p>
        {isPassword && need.prompt && !/^password:?$/i.test(need.prompt) && <p className="text-2">The server asks: {need.prompt}</p>}
        {(refused || error) && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{error || refused}</span>
          </div>
        )}
        <TextField key={`${kind}-${attempt}`} label={label} type="password" autoComplete="new-password" autoFocus value={value} onChange={(e) => setValue(e.target.value)} disabled={busy} />
        <Checkbox checked={remember} onChange={setRemember} disabled={busy} label="Remember on this hub, encrypted, so it can reconnect by itself" />
        {!remember && <p className="setting-hint">Kept in memory only. After a hub restart you will be asked again.</p>}
      </form>
    </Modal>
  );
}
