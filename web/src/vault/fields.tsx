// Pieces the vault's forms share: a masked input, copying, and choosing a master password.

import { useEffect, useState, type InputHTMLAttributes, type ReactNode } from 'react';
import { Button } from '../components/Button';
import { Field } from '../components/form';
import { toast } from '../components/Toast';
import { copyText } from '../lib/clipboard';
import { generatePassphrase, MASTER_MIN_LENGTH, passwordStrength, WEAK_BELOW_SCORE, type Strength } from './generator';
import { copySecret, PIN_LENGTH } from './store';

/**
 * Keeps the browser's and other password managers' autofill out of it: a saved master password
 * would defeat the master password, and the hub's login must not land in a credential's fields.
 * A password field also takes autoComplete="new-password": Chrome ignores "off" there.
 */
export const NO_AUTOFILL = {
  autoComplete: 'off',
  autoCapitalize: 'off',
  autoCorrect: 'off',
  spellCheck: false,
  'data-lpignore': 'true',
  'data-1p-ignore': 'true',
  'data-bwignore': 'true',
} as const;

/** Copy something, with the toast that says what happened. Secrets leave the clipboard again. */
export function copyValue(value: string, label: string, secret: boolean) {
  if (!secret) {
    void copyText(value).then((ok) => (ok ? toast.success(`${label} copied.`) : toast.error('Could not copy.')));
    return;
  }
  void copySecret(value).then(({ ok, clearsIn, atNextInput }) => {
    if (!ok) toast.error('The browser would not let this page copy. Click into the page and try again.');
    else if (!clearsIn) toast.success(`${label} copied.`);
    else if (atNextInput) toast.success(`${label} copied. It leaves the clipboard at your first click in this page after ${clearsIn} s.`);
    else toast.success(`${label} copied. It leaves the clipboard in ${clearsIn} s.`);
  });
}

export function CopyButton({ value, label, secret }: { value: string; label: string; secret: boolean }) {
  return <Button size="sm" variant="ghost" iconOnly icon="copy" aria-label={`Copy ${label.toLowerCase()}`} title={`Copy ${label.toLowerCase()}`} onClick={() => copyValue(value, label, secret)} />;
}

interface SecretInputProps extends Omit<InputHTMLAttributes<HTMLInputElement>, 'id' | 'onChange' | 'value' | 'type'> {
  label: ReactNode;
  value: string;
  onChange: (v: string) => void;
  hint?: ReactNode;
  error?: string;
  /** Buttons after the eye. */
  actions?: ReactNode;
  /** Shown from the start, for what was just generated. */
  revealed?: boolean;
  onReveal?: (shown: boolean) => void;
  fieldClassName?: string;
}

export function SecretInput({ label, value, onChange, hint, error, actions, revealed, onReveal, fieldClassName, ...input }: SecretInputProps) {
  const [own, setOwn] = useState(false);
  const shown = revealed ?? own;
  return (
    <Field label={label} hint={hint} error={error} className={fieldClassName}>
      {({ labelId: _l, ...a11y }) => (
        <div className="vault-input-row">
          <input className="control mono" {...NO_AUTOFILL} autoComplete="new-password" {...a11y} {...input} type={shown ? 'text' : 'password'} value={value} onChange={(e) => onChange(e.target.value)} />
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon={shown ? 'eyeOff' : 'eye'}
            aria-label={shown ? 'Hide' : 'Show'}
            aria-pressed={shown}
            title={shown ? 'Hide' : 'Show'}
            onClick={() => {
              setOwn(!shown);
              onReveal?.(!shown);
            }}
          />
          {actions}
        </div>
      )}
    </Field>
  );
}

// ---- choosing a master password ----

export interface MasterChoice {
  password: string;
  again: string;
  shown: boolean;
  strength: Strength | null;
  /** Why it cannot be used yet; empty when it can. */
  problem: string;
  setPassword: (v: string) => void;
  setAgain: (v: string) => void;
  setShown: (v: boolean) => void;
  suggest: () => Promise<void>;
}

export function useMasterChoice(): MasterChoice {
  const [password, setPassword] = useState('');
  const [again, setAgain] = useState('');
  const [shown, setShown] = useState(false);
  const [strength, setStrength] = useState<Strength | null>(null);

  useEffect(() => {
    if (!password) {
      setStrength(null);
      return;
    }
    let stop = false;
    // Nothing leaves the browser: the library and its word lists are all local.
    const t = window.setTimeout(() => {
      void passwordStrength(password).then((s) => !stop && setStrength(s));
    }, 120);
    return () => {
      stop = true;
      window.clearTimeout(t);
    };
  }, [password]);

  let problem = '';
  if (password.length < MASTER_MIN_LENGTH) problem = `Use at least ${MASTER_MIN_LENGTH} characters.`;
  else if (password !== again) problem = 'The two passwords are not the same.';

  return {
    password,
    again,
    shown,
    strength,
    problem,
    setPassword,
    setAgain,
    setShown,
    suggest: async () => {
      const phrase = await generatePassphrase({ words: 6, separator: '-', capitalize: false, number: false });
      setPassword(phrase);
      setAgain(phrase);
      setShown(true);
    },
  };
}

function StrengthBar({ strength }: { strength: Strength }) {
  return (
    <div className="vault-strength" data-score={strength.score}>
      <span className="vault-strength-bar" aria-hidden="true">
        {[1, 2, 3, 4].map((n) => (
          <span key={n} className={strength.score >= n ? 'on' : ''} />
        ))}
      </span>
      <span className="vault-strength-label">{strength.label}</span>
      {strength.score < WEAK_BELOW_SCORE && <span className="vault-strength-advice">Easy to guess. {strength.advice || 'Add a few uncommon words.'}</span>}
    </div>
  );
}

/** Six digits, masked, on a number pad where there is one. */
export function PinInput({ label, value, onChange, error, hint, autoFocus }: { label: string; value: string; onChange: (v: string) => void; error?: string; hint?: string; autoFocus?: boolean }) {
  return (
    <Field label={label} error={error} hint={hint}>
      {({ labelId: _l, ...a11y }) => (
        <input
          className="control mono vault-pin"
          {...NO_AUTOFILL}
          autoComplete="new-password"
          {...a11y}
          type="password"
          inputMode="numeric"
          pattern="[0-9]*"
          autoFocus={autoFocus}
          value={value}
          onChange={(e) => onChange(e.target.value.replace(/\D/g, '').slice(0, PIN_LENGTH))}
        />
      )}
    </Field>
  );
}

/** A new master password, twice, with what to do if it is not good enough. */
export function MasterFields({ m, label, showProblem }: { m: MasterChoice; label: string; showProblem: boolean }) {
  return (
    <>
      <SecretInput
        label={label}
        value={m.password}
        onChange={m.setPassword}
        revealed={m.shown}
        onReveal={m.setShown}
        autoFocus
        hint={`At least ${MASTER_MIN_LENGTH} characters. Longer is safer: whoever gets the vault file can try guesses offline.`}
        actions={
          <Button size="sm" variant="ghost" iconOnly icon="sparkle" aria-label="Suggest a passphrase" title="Suggest a passphrase" onClick={() => void m.suggest()} />
        }
      />
      {m.strength && <StrengthBar strength={m.strength} />}
      <SecretInput label="Type it again" value={m.again} onChange={m.setAgain} revealed={m.shown} onReveal={m.setShown} error={showProblem ? m.problem : undefined} />
    </>
  );
}
