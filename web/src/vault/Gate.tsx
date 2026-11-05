// What a Credentials card shows until the vault is open.

import { useState, type FormEvent } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Checkbox } from '../components/form';
import { Icon } from '../components/Icon';
import { plainHttp, WrongPassword } from './crypto';
import { MasterFields, PinInput, SecretInput, useMasterChoice } from './fields';
import { createVault, PinGone, pinEnabled, PIN_LENGTH, retryVault, unlockVault, unlockWithPin, WrongPin, type VaultState } from './store';

/** Said where the password is typed, once, and without getting in the way. */
function PlainHttpNote() {
  if (!plainHttp()) return null;
  return (
    <p className="vault-note vault-http-note">
      <Icon name="shieldAlert" />
      <span>Plain HTTP connection. Fine on a network you trust, but anyone who can alter traffic on the way could capture this password.</span>
    </p>
  );
}

function CreateVault() {
  const m = useMasterChoice();
  const [understood, setUnderstood] = useState(false);
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setTried(true);
    setError('');
    if (m.problem) return;
    if (!understood) {
      setError('Tick the box first: this one cannot be undone later.');
      return;
    }
    setBusy(true);
    try {
      await createVault(m.password);
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };
  return (
    <form className="vault-gate nowheel" onSubmit={(e) => void submit(e)}>
      <h3>Create your vault</h3>
      <p>Credentials are encrypted in this browser with your master password. The hub stores only ciphertext, so neither it nor the agents on this machine can read them.</p>
      <MasterFields m={m} label="Master password" showProblem={tried} />
      <PlainHttpNote />
      <div className="alert warning" role="note">
        <Icon name="alert" />
        <span>Nothing can recover this password. Forget it, and everything in the vault is gone. Write it down somewhere safe.</span>
      </div>
      <Checkbox checked={understood} onChange={setUnderstood} label="I understand it cannot be recovered" />
      {error && (
        <div className="alert" role="alert">
          {error}
        </div>
      )}
      <Button type="submit" variant="primary" block loading={busy}>
        Create vault
      </Button>
    </form>
  );
}

function PasswordUnlock({ note, onForgot, onPin }: { note: string; onForgot: () => void; onPin?: () => void }) {
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!password || busy) return;
    setBusy(true);
    setError('');
    try {
      await unlockVault(password);
    } catch (err) {
      setError(err instanceof WrongPassword ? 'That is not the master password.' : errorMessage(err));
      setBusy(false);
    }
  };
  return (
    <form className="vault-gate nowheel" onSubmit={(e) => void submit(e)}>
      <Icon name="lock" className="vault-gate-icon" />
      <h3>Vault is locked</h3>
      {note && (
        <div className="alert warning" role="alert">
          <Icon name="alert" />
          <span>{note}</span>
        </div>
      )}
      <SecretInput label="Master password" value={password} onChange={setPassword} error={error} autoFocus />
      <PlainHttpNote />
      <Button type="submit" variant="primary" block loading={busy}>
        Unlock
      </Button>
      {onPin && (
        <button type="button" className="vault-link" onClick={onPin}>
          Use PIN
        </button>
      )}
      <button type="button" className="vault-link" onClick={onForgot}>
        Forgot it?
      </button>
    </form>
  );
}

function PinUnlock({ onPassword, onGone }: { onPassword: () => void; onGone: () => void }) {
  const [pin, setPin] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const attempt = async (value: string) => {
    setBusy(true);
    setError('');
    try {
      await unlockWithPin(value);
    } catch (err) {
      setBusy(false);
      setPin('');
      if (err instanceof WrongPin) setError(`Wrong PIN. ${err.left === 1 ? '1 try' : `${err.left} tries`} left, then only the master password opens it.`);
      else if (err instanceof PinGone) onGone();
      else setError(errorMessage(err));
    }
  };
  return (
    <form
      className="vault-gate nowheel"
      onSubmit={(e) => {
        e.preventDefault();
        if (pin.length === PIN_LENGTH && !busy) void attempt(pin);
      }}
    >
      <Icon name="lock" className="vault-gate-icon" />
      <h3>Vault is locked</h3>
      <PinInput
        label="PIN"
        value={pin}
        error={error}
        autoFocus
        onChange={(v) => {
          setPin(v);
          // The last digit is the Enter: nothing else can come after it.
          if (v.length === PIN_LENGTH && !busy) void attempt(v);
        }}
      />
      <PlainHttpNote />
      <Button type="submit" variant="primary" block loading={busy} disabled={pin.length !== PIN_LENGTH}>
        Unlock
      </Button>
      <button type="button" className="vault-link" onClick={onPassword}>
        Use master password
      </button>
    </form>
  );
}

function Unlock({ onForgot }: { onForgot: () => void }) {
  const [withPin, setWithPin] = useState(pinEnabled);
  const [note, setNote] = useState('');
  if (withPin) {
    return (
      <PinUnlock
        onPassword={() => setWithPin(false)}
        onGone={() => {
          setNote('The PIN no longer works: too many wrong tries, or the master password changed. Use the master password, and set a new PIN after that.');
          setWithPin(false);
        }}
      />
    );
  }
  return <PasswordUnlock note={note} onForgot={onForgot} onPin={pinEnabled() ? () => setWithPin(true) : undefined} />;
}

export function VaultGate({ v, onReset }: { v: VaultState; onReset: () => void }) {
  if (v.status === 'absent') return <CreateVault />;
  if (v.status === 'locked') return <Unlock onForgot={onReset} />;
  if (v.status === 'unsupported') {
    return (
      <div className="vault-gate nowheel">
        <Icon name="shieldAlert" className="vault-gate-icon" />
        <h3>This browser cannot run the vault</h3>
        <p>It needs Web Workers and WebAssembly to encrypt your credentials here, in the browser. Try a current version of Chrome, Edge, Firefox or Safari.</p>
      </div>
    );
  }
  if (v.status === 'error') {
    return (
      <div className="vault-gate nowheel">
        <Icon name="alertCircle" className="vault-gate-icon" />
        <h3>Could not load the vault</h3>
        <p>{v.error}</p>
        <Button size="sm" icon="refresh" onClick={() => void retryVault()}>
          Try again
        </Button>
        <button type="button" className="vault-link" onClick={onReset}>
          Reset the vault…
        </button>
      </div>
    );
  }
  return null;
}
