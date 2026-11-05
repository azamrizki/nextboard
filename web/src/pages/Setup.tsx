// First-run wizard (DESIGN.md §3.8): (1) setup code + admin password + hub name,
// (2) detected agent CLIs as a small bento grid.

import { useEffect, useState, type CSSProperties, type FormEvent } from 'react';
import { api, ApiError, errorMessage } from '../api';
import { BrandMark } from '../components/AppHeader';
import { Button } from '../components/Button';
import { TextField } from '../components/form';
import { Icon } from '../components/Icon';
import type { Machine, Preset } from '../lib/types';

interface SetupProps {
  /** Wizard finished (already logged in via cookie). */
  onDone: () => void;
}

export function SetupPage({ onDone }: SetupProps) {
  const [step, setStep] = useState<1 | 2>(1);
  return (
    <main className="auth-page">
      <div className="auth-brand">
        <div className="brand-row">
          <BrandMark large />
          <span className="brand-name">NextBoard</span>
        </div>
        <p>One board for every agent terminal on your machines.</p>
      </div>
      <div className="auth-card wide">
        <ol className="stepper" aria-label="Setup progress">
          <li className={step === 1 ? 'active' : 'done'} aria-current={step === 1 ? 'step' : undefined}>
            <span className="step-num">{step === 1 ? '1' : <Icon name="check" label="Done" />}</span>
            Admin
          </li>
          <li className={step === 2 ? 'active' : ''} aria-current={step === 2 ? 'step' : undefined}>
            <span className="step-num">2</span>
            Agents
          </li>
        </ol>
        {step === 1 ? <StepAccount onNext={() => setStep(2)} /> : <StepAgents onDone={onDone} />}
      </div>
    </main>
  );
}

type Errors = Partial<Record<'code' | 'hubName' | 'password' | 'confirm', string>>;

function validate(code: string, hubName: string, password: string, confirm: string): Errors {
  const e: Errors = {};
  if (!code.trim()) e.code = 'Enter the setup code from the link printed by `nextboard install`.';
  if (!hubName.trim()) e.hubName = 'Give this hub a name, for example the machine’s hostname.';
  if (password.length < 8) e.password = 'Use at least 8 characters.';
  if (confirm !== password) e.confirm = 'Passwords don’t match — type the same password twice.';
  return e;
}

function StepAccount({ onNext }: { onNext: () => void }) {
  const [code, setCode] = useState(() => new URLSearchParams(window.location.search).get('code') ?? '');
  const [hubName, setHubName] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState('');
  const [codeError, setCodeError] = useState('');

  const errors = submitted ? validate(code, hubName, password, confirm) : {};

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setSubmitted(true);
    setFormError('');
    setCodeError('');
    if (Object.keys(validate(code, hubName, password, confirm)).length > 0) return;
    setBusy(true);
    try {
      await api.setup(code.trim(), password, hubName.trim());
      // Drop ?code= from the address bar; it's single-use anyway.
      window.history.replaceState(null, '', '/setup');
      onNext();
    } catch (err) {
      if (err instanceof ApiError && err.status === 403) {
        setCodeError('This code is wrong, expired or already used. Run `nextboard setup-link` on the server for a new one.');
      } else if (err instanceof ApiError && err.status === 409) {
        setFormError('This hub is already set up. Reload the page to log in.');
      } else {
        setFormError(errorMessage(err));
      }
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="form" noValidate>
      <div>
        <h2>Create the admin account</h2>
        <p className="lead">You’ll use this password to sign in from any browser.</p>
      </div>
      <fieldset disabled={busy}>
        <TextField
          label="Setup code"
          mono
          value={code}
          onChange={(e) => setCode(e.target.value.toUpperCase())}
          placeholder="K7QX-M2PA"
          autoComplete="one-time-code"
          spellCheck={false}
          autoFocus={!code}
          hint="From the setup link — single use, valid for 30 minutes."
          error={codeError || errors.code}
        />
        <TextField
          label="Hub name"
          value={hubName}
          onChange={(e) => setHubName(e.target.value)}
          placeholder="devbox"
          maxLength={64}
          autoFocus={!!code}
          hint="Shown in the header and on paired devices."
          error={errors.hubName}
        />
        <div className="field-row">
          <TextField
            fieldClassName="span-6"
            label="Admin password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            autoComplete="new-password"
            hint="At least 8 characters."
            error={errors.password}
          />
          <TextField
            fieldClassName="span-6"
            label="Confirm password"
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            autoComplete="new-password"
            error={errors.confirm}
          />
        </div>
      </fieldset>
      {formError && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{formError}</span>
        </div>
      )}
      <Button type="submit" variant="primary" block loading={busy}>
        Create admin account
      </Button>
    </form>
  );
}

function StepAgents({ onDone }: { onDone: () => void }) {
  const [presets, setPresets] = useState<Preset[] | null>(null);
  const [local, setLocal] = useState<Machine | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .machinePresets('local')
      .then(setPresets)
      .catch((err) => setError(errorMessage(err)));
    api
      .machines()
      .then((ms) => setLocal(ms.find((m) => m.id === 'local') ?? null))
      .catch(() => undefined);
  }, []);

  const found = presets?.filter((p) => p.available !== false).length ?? 0;

  return (
    <div className="form">
      <div>
        <h2>Agents on This PC</h2>
        <p className="lead">
          {presets ? `${found} of ${presets.length} presets are ready to run.` : 'Checking which agent CLIs are installed…'}
        </p>
      </div>
      {local?.probe && (
        <div className="system-line">
          <Icon name="monitor" />
          <span>
            {local.probe.os} {local.probe.arch} · <span className="mono">{local.probe.shell}</span>
          </span>
        </div>
      )}
      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
      {presets && (
        <ul className="plain agent-grid" aria-label="Detected agent CLIs">
          {presets.map((p) => {
            const ok = p.available !== false;
            return (
              <li key={p.id} className={`agent-tile ${ok ? '' : 'missing'}`}>
                <div className="agent-tile-head">
                  <span className="swatch" style={{ '--swatch': p.color } as CSSProperties} aria-hidden="true" />
                  <strong className="truncate">{p.name}</strong>
                </div>
                <code className="truncate">{p.command}</code>
                <span className={`badge ${ok ? 'badge-success' : 'badge-danger'}`}>
                  <Icon name={ok ? 'check' : 'close'} />
                  {ok ? 'Found' : 'Not found'}
                </span>
              </li>
            );
          })}
        </ul>
      )}
      <p className="field-hint">
        Missing one? Install and log in to it as the user running NextBoard, then use Machines → Re-probe. Custom
        presets live in <code>~/.nextboard/presets.json</code>.
      </p>
      <Button variant="primary" block icon="grid" onClick={onDone} autoFocus>
        Open board
      </Button>
    </div>
  );
}
