// The vault's dialogs: generator, folder, settings, master password, import, export, reset.

import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { Checkbox, FilePick, Field, SegmentedControl, SelectField, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import { plainHttp, WrongPassword } from './crypto';
import { copyValue, MasterFields, NO_AUTOFILL, PinInput, SecretInput, useMasterChoice } from './fields';
import {
  DEFAULT_PASSPHRASE,
  DEFAULT_PASSWORD,
  generatePassphrase,
  generatePassword,
  passphraseBits,
  passwordBits,
  type PassphraseOptions,
  type PasswordOptions,
} from './generator';
import { download, parseImport, toBitwardenJson, type Imported } from './importExport';
import type { VaultFolder, VaultItem } from './model';
import { changeMasterPassword, checkPassword, importRecords, MAX_PIN_TRIES, passkeyEnabled, passkeySupported, pinEnabled, pinProblem, PIN_LENGTH, removeAllPins, removePasskey, removePin, resetVault, saveFolder, setupPasskey, setVaultPrefs, setupPin, useVaultPrefs } from './store';

// ---- generator ----

interface GenPrefs {
  mode: 'password' | 'passphrase';
  password: PasswordOptions;
  passphrase: PassphraseOptions;
}

const GEN_KEY = 'nb.vault.generator';

function loadGen(): GenPrefs {
  try {
    const raw = JSON.parse(localStorage.getItem(GEN_KEY) ?? '{}') as Partial<GenPrefs>;
    return {
      mode: raw.mode === 'passphrase' ? 'passphrase' : 'password',
      password: { ...DEFAULT_PASSWORD, ...raw.password },
      passphrase: { ...DEFAULT_PASSPHRASE, ...raw.passphrase },
    };
  } catch {
    return { mode: 'password', password: DEFAULT_PASSWORD, passphrase: DEFAULT_PASSPHRASE };
  }
}

export function GeneratorDialog({ onUse, onClose }: { onUse?: (value: string) => void; onClose: () => void }) {
  const [gen, setGen] = useState(loadGen);
  const [value, setValue] = useState('');

  const roll = useCallback(async () => {
    setValue(gen.mode === 'password' ? generatePassword(gen.password) : await generatePassphrase(gen.passphrase));
  }, [gen]);

  useEffect(() => {
    void roll();
    try {
      localStorage.setItem(GEN_KEY, JSON.stringify(gen));
    } catch {
      /* only a convenience */
    }
  }, [gen, roll]);

  const pw = gen.password;
  const ph = gen.passphrase;
  const setPw = (patch: Partial<PasswordOptions>) => setGen({ ...gen, password: { ...pw, ...patch } });
  const setPh = (patch: Partial<PassphraseOptions>) => setGen({ ...gen, passphrase: { ...ph, ...patch } });
  /** The last set switched on stays on: a password needs something to be made of. */
  const toggle = (k: 'upper' | 'lower' | 'digits' | 'symbols', on: boolean) => {
    const others = (['upper', 'lower', 'digits', 'symbols'] as const).filter((x) => x !== k).some((x) => pw[x]);
    if (on || others) setPw({ [k]: on });
  };
  const bits = gen.mode === 'password' ? passwordBits(pw) : passphraseBits(ph);

  return (
    <Modal
      title="Password generator"
      icon="sparkle"
      size="sm"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>{onUse ? 'Cancel' : 'Close'}</Button>
          {onUse ? (
            <Button variant="primary" disabled={!value} onClick={() => onUse(value)}>
              Use this
            </Button>
          ) : (
            <Button variant="primary" icon="copy" disabled={!value} onClick={() => copyValue(value, 'Password', true)}>
              Copy
            </Button>
          )}
        </>
      }
    >
      <div className="vault-dialog">
        <SegmentedControl
          label="Kind"
          block
          value={gen.mode}
          onChange={(mode) => setGen({ ...gen, mode })}
          options={[
            { value: 'password', label: 'Password' },
            { value: 'passphrase', label: 'Passphrase' },
          ]}
        />
        <div className="vault-gen-out">
          <span className="vault-gen-value" data-testid="vault-generated">
            {value}
          </span>
          <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Generate another" title="Generate another" onClick={() => void roll()} />
          {onUse && <Button size="sm" variant="ghost" iconOnly icon="copy" aria-label="Copy" title="Copy" disabled={!value} onClick={() => copyValue(value, 'Password', true)} />}
        </div>
        <p className="vault-note tabular">About {bits} bits of randomness</p>
        {gen.mode === 'password' ? (
          <>
            <Field label={`Length: ${pw.length}`}>
              {({ labelId: _l, ...a11y }) => <input {...a11y} className="vault-range" type="range" min={8} max={128} value={pw.length} onChange={(e) => setPw({ length: Number(e.target.value) })} />}
            </Field>
            <div className="vault-checks">
              <Checkbox checked={pw.upper} onChange={(on) => toggle('upper', on)} label="A–Z" />
              <Checkbox checked={pw.lower} onChange={(on) => toggle('lower', on)} label="a–z" />
              <Checkbox checked={pw.digits} onChange={(on) => toggle('digits', on)} label="0–9" />
              <Checkbox checked={pw.symbols} onChange={(on) => toggle('symbols', on)} label="!@#$%" />
            </div>
            <Checkbox checked={pw.avoidAmbiguous} onChange={(avoidAmbiguous) => setPw({ avoidAmbiguous })} label="Avoid look-alikes (I, l, 1, O, 0)" />
          </>
        ) : (
          <>
            <Field label={`Words: ${ph.words}`}>
              {({ labelId: _l, ...a11y }) => <input {...a11y} className="vault-range" type="range" min={3} max={12} value={ph.words} onChange={(e) => setPh({ words: Number(e.target.value) })} />}
            </Field>
            <TextField label="Separator" value={ph.separator} maxLength={3} onChange={(e) => setPh({ separator: e.target.value })} {...NO_AUTOFILL} />
            <Checkbox checked={ph.capitalize} onChange={(capitalize) => setPh({ capitalize })} label="Capitalize" />
            <Checkbox checked={ph.number} onChange={(number) => setPh({ number })} label="Include a number" />
          </>
        )}
      </div>
    </Modal>
  );
}

// ---- folder ----

export function FolderDialog({ folder, onSaved, onClose }: { folder?: VaultFolder; onSaved?: (f: VaultFolder) => void; onClose: () => void }) {
  const [name, setName] = useState(folder?.name ?? '');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const v = name.trim();
    if (!v) {
      setError('Give it a name.');
      return;
    }
    setBusy(true);
    try {
      onSaved?.(await saveFolder(v, folder));
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };
  return (
    <Modal
      title={folder ? 'Rename folder' : 'New folder'}
      icon="folder"
      size="sm"
      onClose={onClose}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" type="submit" form="vault-folder-form" loading={busy}>
            Save
          </Button>
        </>
      }
    >
      <form id="vault-folder-form" onSubmit={(e) => void submit(e)}>
        <TextField label="Name" value={name} autoFocus maxLength={80} error={error} onChange={(e) => setName(e.target.value)} {...NO_AUTOFILL} />
      </form>
    </Modal>
  );
}

// ---- settings ----

const LOCK_CHOICES = [1, 5, 15, 30, 60, 240];
const CLIPBOARD_CHOICES = [10, 20, 30, 60, 120, 300, 0];

const minutes = (n: number) => (n === 1 ? '1 minute' : n < 60 ? `${n} minutes` : n === 60 ? '1 hour' : `${n / 60} hours`);
const seconds = (n: number) => (n === 0 ? 'Never' : n < 60 ? `${n} seconds` : n === 60 ? '1 minute' : `${n / 60} minutes`);

export function SettingsDialog({ onClose }: { onClose: () => void }) {
  const prefs = useVaultPrefs();
  const lockAfter = LOCK_CHOICES.includes(prefs.lockAfterMin) ? LOCK_CHOICES : [...LOCK_CHOICES, prefs.lockAfterMin];
  return (
    <Modal title="Vault settings" icon="sliders" size="sm" onClose={onClose} footer={<Button onClick={onClose}>Close</Button>}>
      <div className="vault-dialog">
        <SelectField label="Lock after" hint="Without touching a Credentials card. Reloading the page or logging out always locks." value={prefs.lockAfterMin} onChange={(e) => setVaultPrefs({ lockAfterMin: Number(e.target.value) })}>
          {lockAfter.map((n) => (
            <option key={n} value={n}>
              {minutes(n)}
            </option>
          ))}
        </SelectField>
        <SelectField label="Clear clipboard after" hint={plainHttp() ? 'For passwords and other secrets. On plain HTTP the browser only allows this right after a click or key press, so it can wait for your next one in this page. Whatever else you copied by then goes too.' : 'For passwords and other secrets. Whatever else you copied by then goes too.'} value={prefs.clipboardSec} onChange={(e) => setVaultPrefs({ clipboardSec: Number(e.target.value) })}>
          {CLIPBOARD_CHOICES.map((n) => (
            <option key={n} value={n}>
              {seconds(n)}
            </option>
          ))}
        </SelectField>
        <p className="vault-note">These two are remembered per browser. Encryption: Argon2id (64 MiB, 3 passes) and AES-256-GCM.</p>
      </div>
    </Modal>
  );
}

// ---- master password ----

export function ChangePasswordDialog({ onClose }: { onClose: () => void }) {
  const [current, setCurrent] = useState('');
  const m = useMasterChoice();
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setTried(true);
    setError('');
    if (m.problem) return;
    if (m.password === current) {
      setError('That is the password you already have.');
      return;
    }
    setBusy(true);
    try {
      const hadPin = pinEnabled();
      await changeMasterPassword(current, m.password);
      toast.success(hadPin ? 'Master password changed. PINs are off now: set this one up again.' : 'Master password changed.');
      onClose();
    } catch (err) {
      setError(err instanceof WrongPassword ? 'The current password is not right.' : errorMessage(err));
      setBusy(false);
    }
  };
  return (
    <Modal
      title="Change master password"
      icon="key"
      size="sm"
      onClose={onClose}
      dismissable={!busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" form="vault-password-form" loading={busy}>
            Change password
          </Button>
        </>
      }
    >
      <form id="vault-password-form" className="vault-dialog" onSubmit={(e) => void submit(e)}>
        <SecretInput label="Current password" value={current} onChange={setCurrent} />
        <MasterFields m={m} label="New password" showProblem={tried} />
        <p className="vault-note">The new one cannot be recovered either. Items stay as they are: their key is only wrapped again. PINs on every browser are turned off.</p>
        {error && (
          <div className="alert" role="alert">
            {error}
          </div>
        )}
      </form>
    </Modal>
  );
}

// ---- PIN ----

export function PinDialog({ onClose }: { onClose: () => void }) {
  const has = pinEnabled();
  const [master, setMaster] = useState('');
  const [pin, setPin] = useState('');
  const [again, setAgain] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [pinError, setPinError] = useState('');
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    const problem = pinProblem(pin) || (pin !== again ? 'The two PINs are not the same.' : '');
    setPinError(problem);
    if (problem) return;
    setBusy(true);
    try {
      await setupPin(master, pin);
      toast.success('PIN set for this browser.');
      onClose();
    } catch (err) {
      setError(err instanceof WrongPassword ? 'That is not the master password.' : errorMessage(err));
      setBusy(false);
    }
  };
  const off = async (fn: () => Promise<void>, done: string) => {
    setBusy(true);
    setError('');
    try {
      await fn();
      toast.success(done);
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };
  return (
    <Modal
      title="PIN unlock"
      icon="keyboard"
      size="sm"
      onClose={onClose}
      dismissable={!busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" type="submit" form="vault-pin-form" loading={busy}>
            {has ? 'Change PIN' : 'Set PIN'}
          </Button>
        </>
      }
    >
      <form id="vault-pin-form" className="vault-dialog" onSubmit={(e) => void submit(e)}>
        <p className="vault-note">
          For this browser only, so that a locked vault opens with {PIN_LENGTH} digits. After {MAX_PIN_TRIES} wrong tries the PIN is gone and only the master password opens it. If this browser's files and the hub's folder end up in the same hands, a PIN can be guessed, so use it on devices you trust.
        </p>
        <SecretInput label="Master password" value={master} onChange={setMaster} autoFocus />
        <PinInput label={has ? 'New PIN' : 'PIN'} value={pin} onChange={setPin} />
        <PinInput label="Type it again" value={again} onChange={setAgain} error={pinError} />
        {error && (
          <div className="alert" role="alert">
            {error}
          </div>
        )}
        {has && (
          <div className="vault-dialog-actions">
            <Button size="sm" disabled={busy} onClick={() => void off(removePin, 'PIN turned off on this browser.')}>
              Turn off here
            </Button>
            <Button size="sm" variant="danger-ghost" disabled={busy} onClick={() => void off(removeAllPins, 'PINs turned off on every browser.')}>
              Turn off everywhere
            </Button>
          </div>
        )}
      </form>
    </Modal>
  );
}

// ---- passkey ----

export function PasskeyDialog({ onClose }: { onClose: () => void }) {
  const supported = passkeySupported();
  const [has, setHas] = useState(passkeyEnabled);
  const [master, setMaster] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await setupPasskey(master);
      toast.success('Passkey set for this browser.');
      onClose();
    } catch (err) {
      setError(err instanceof WrongPassword ? 'That is not the master password.' : errorMessage(err));
      setBusy(false);
    }
  };
  return (
    <Modal
      title="Passkey unlock"
      icon="key"
      size="sm"
      onClose={onClose}
      dismissable={!busy}
      footer={
        supported ? (
          <>
            <Button onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button variant="primary" type="submit" form="vault-passkey-form" loading={busy} disabled={!master}>
              {has ? 'Make a new passkey' : 'Make a passkey'}
            </Button>
          </>
        ) : (
          <Button onClick={onClose}>Close</Button>
        )
      }
    >
      {!supported ? (
        <div className="vault-dialog">
          <p className="vault-note">Passkeys only work when the hub is opened by name over HTTPS with a valid certificate, or as localhost; never by IP address or over plain HTTP. This page is not, so the master password and a PIN are what open the vault here.</p>
        </div>
      ) : (
        <form id="vault-passkey-form" className="vault-dialog" onSubmit={(e) => void submit(e)}>
          <p className="vault-note">
            For this browser only: a locked vault opens with your fingerprint, face, or device PIN. The passkey needs PRF, which current phones, Windows Hello, macOS and recent security keys have. Nothing about it goes to the hub. Changing the master password turns it off.
          </p>
          <SecretInput label="Master password" value={master} onChange={setMaster} autoFocus />
          {error && (
            <div className="alert" role="alert">
              {error}
            </div>
          )}
          {has && (
            <div className="vault-dialog-actions">
              <Button
                size="sm"
                disabled={busy}
                onClick={() => {
                  removePasskey();
                  setHas(false);
                  toast.success('Passkey unlock turned off on this browser.');
                  onClose();
                }}
              >
                Turn off here
              </Button>
            </div>
          )}
        </form>
      )}
    </Modal>
  );
}

// ---- import ----

const MAX_IMPORT_BYTES = 64 << 20;

export function ImportDialog({ onClose }: { onClose: () => void }) {
  const [fileName, setFileName] = useState('');
  const [data, setData] = useState<Imported | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const onFile = async (file: File) => {
    setFileName(file.name);
    setData(null);
    setError('');
    if (file.size > MAX_IMPORT_BYTES) {
      setError('This file is too large.');
      return;
    }
    try {
      setData(parseImport(file.name, await file.text()));
    } catch (err) {
      setError(errorMessage(err));
    }
  };
  const run = async () => {
    if (!data) return;
    setBusy(true);
    setError('');
    try {
      const n = await importRecords(data);
      toast.success(n === 1 ? '1 item imported.' : `${n} items imported.`);
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };
  return (
    <Modal
      title="Import credentials"
      icon="upload"
      size="sm"
      onClose={onClose}
      dismissable={!busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" disabled={!data} loading={busy} onClick={() => void run()}>
            {data ? `Import ${data.items.length}` : 'Import'}
          </Button>
        </>
      }
    >
      <div className="vault-dialog">
        <p className="vault-note">An unencrypted export from Bitwarden (.json or .csv), Chrome or Firefox (.csv). It stays in this browser and is encrypted here before anything goes to the hub.</p>
        <FilePick buttonLabel="Choose file" fileName={fileName} accept=".json,.csv" onFile={(f) => void onFile(f)} />
        {data && (
          <p className="vault-note">
            {data.source}: {data.items.length} {data.items.length === 1 ? 'item' : 'items'}, {data.folders.length} {data.folders.length === 1 ? 'folder' : 'folders'}.
            {data.skipped > 0 && ` ${data.skipped} of a kind that cannot be imported will be left out.`}
          </p>
        )}
        {error && (
          <div className="alert" role="alert">
            {error}
          </div>
        )}
        <div className="alert warning" role="note">
          <Icon name="alert" />
          <span>Delete the export file when you are done: anyone who gets it can read every password in it.</span>
        </div>
      </div>
    </Modal>
  );
}

// ---- export ----

export function ExportDialog({ items, folders, onClose }: { items: VaultItem[]; folders: VaultFolder[]; onClose: () => void }) {
  const live = items.filter((i) => !i.deletedAt);
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await checkPassword(password);
      download(`nextboard-credentials-${new Date().toISOString().slice(0, 10)}.json`, toBitwardenJson(live, folders));
      toast.success('Exported. Delete the file when you are done with it.');
      onClose();
    } catch (err) {
      setError(err instanceof WrongPassword ? 'That is not the master password.' : errorMessage(err));
      setBusy(false);
    }
  };
  return (
    <Modal
      title="Export credentials"
      icon="download"
      tone="danger"
      size="sm"
      onClose={onClose}
      dismissable={!busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="danger" type="submit" form="vault-export-form" loading={busy} disabled={!password}>
            Export {live.length} unencrypted
          </Button>
        </>
      }
    >
      <form id="vault-export-form" className="vault-dialog" onSubmit={(e) => void submit(e)}>
        <div className="alert" role="note">
          <Icon name="alert" />
          <span>This file holds every password in plain text. Keep it off shared folders, and delete it when you are done.</span>
        </div>
        <p className="vault-note">Bitwarden's .json format, which Bitwarden and this vault can import. The trash is left out.</p>
        <SecretInput label="Master password" value={password} onChange={setPassword} autoFocus error={error} />
      </form>
    </Modal>
  );
}

// ---- reset ----

export function ResetDialog({ count, onClose }: { count: number | null; onClose: () => void }) {
  const [word, setWord] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (word !== 'RESET') return;
    setBusy(true);
    setError('');
    try {
      const kept = await resetVault();
      toast.success(kept ? `Vault reset. The old one is kept as ${kept} in the hub's data folder.` : 'Vault reset.');
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };
  return (
    <Modal
      title="Reset the vault?"
      icon="alert"
      tone="danger"
      size="sm"
      onClose={onClose}
      dismissable={!busy}
      footer={
        <>
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="danger" type="submit" form="vault-reset-form" loading={busy} disabled={word !== 'RESET'}>
            Reset vault
          </Button>
        </>
      }
    >
      <form id="vault-reset-form" className="vault-dialog" onSubmit={(e) => void submit(e)}>
        <p>
          {count === null ? 'Every credential' : count === 1 ? 'The 1 item' : `All ${count} items`} will be gone from every card on every board, and you choose a new master password. NextBoard cannot get them back without the old password.
        </p>
        <p className="vault-note">The old encrypted file stays in the hub's data folder as vault.reset-….json, in case the password comes back to you.</p>
        <TextField label='Type "RESET" to confirm' value={word} autoFocus onChange={(e) => setWord(e.target.value)} {...NO_AUTOFILL} />
        {error && (
          <div className="alert" role="alert">
            {error}
          </div>
        )}
      </form>
    </Modal>
  );
}
