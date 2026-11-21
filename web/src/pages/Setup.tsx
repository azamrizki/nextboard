// First-run wizard (DESIGN.md §3.8, multi-instance design §5). A default instance alone on its
// machine keeps the two steps it always had: (1) setup code + admin password + hub name, (2) the
// agent CLIs found. Every other instance also chooses how its agents sign in, may copy from
// another instance of this machine, and ends on a summary of where it keeps everything. All of it
// is plain requests and the hub's non-Secure cookie, so it works over plain HTTP.

import { lazy, Suspense, useEffect, useState, type CSSProperties, type FormEvent } from 'react';
import { api, ApiError, errorMessage } from '../api';
import { BrandMark } from '../components/AppHeader';
import { Button } from '../components/Button';
import { Checkbox, RadioCards, SelectField, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { copyText } from '../lib/clipboard';
import type { Machine, Preset, Profile, SetupStatus } from '../lib/types';
import type { ApiProvider } from '../providers/providers';
import { setupApi, type ImportResult, type ImportSource } from '../setup/api';
import {
  anythingChosen,
  beyondLoopback,
  copiedSummary,
  DEFAULT_COPY,
  importBody,
  ISOLATION_NOTE,
  markAddresses,
  nextStep,
  portOf,
  preselectSource,
  secretsChosen,
  serviceText,
  setChoice,
  signInPatch,
  STEP_LABELS,
  wizardSteps,
  type CopyChoices,
  type CopyKey,
  type SignInMode,
  type StepId,
  type WizardHub,
} from '../setup/wizard';
import { store } from '../store';

// Both hold what the rest of the wizard never needs (a terminal, the provider form): loaded when
// the step asks for them.
const ProfileSignInDialog = lazy(() => import('../settings/ProfileSignIn').then((m) => ({ default: m.ProfileSignInDialog })));
const ProviderDialog = lazy(() => import('../settings/ProvidersPane').then((m) => ({ default: m.ProviderDialog })));

interface SetupProps {
  /** Wizard finished (already logged in via cookie). */
  onDone: () => void;
}

export function SetupPage({ onDone }: SetupProps) {
  const [status, setStatus] = useState<SetupStatus | null>(null);
  // the link's ?from= preselects the copy step; read now, the claim drops the query
  const [from] = useState(() => new URLSearchParams(window.location.search).get('from') ?? '');
  const [sources, setSources] = useState<ImportSource[] | null>(null);
  const [step, setStep] = useState<StepId>('claim');

  useEffect(() => {
    api
      .setupStatus()
      .then(setStatus)
      .catch(() => undefined); // an older hub: the steps it always had
  }, []);

  const instance = status?.instance ?? 'default';
  const isDefault = status?.defaultInstance ?? instance === 'default';
  const ready = (list: ImportSource[] | null) => (list === null ? null : list.filter((s) => s.setUp).length);
  const steps = wizardSteps({ isDefault, sources: ready(sources), fromHint: !!from });
  const index = Math.max(0, steps.indexOf(step));
  const advance = (list: ImportSource[] | null = sources) => {
    const next = nextStep(wizardSteps({ isDefault, sources: ready(list), fromHint: !!from }), step);
    if (next) setStep(next);
    else onDone();
  };

  const claimed = async () => {
    // signed in now: the other instances can be listed (an older hub has no such endpoint)
    const list = await setupApi.importSources().catch(() => [] as ImportSource[]);
    setSources(list);
    advance(list);
  };

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
        <ol className={`stepper ${steps.length > 2 ? 'steps-many' : ''}`} aria-label="Setup progress">
          {steps.map((id, i) => (
            <li key={id} className={i === index ? 'active' : i < index ? 'done' : ''} aria-current={i === index ? 'step' : undefined}>
              <span className="step-num">{i < index ? <Icon name="check" label="Done" /> : String(i + 1)}</span>
              <span className="step-label">{STEP_LABELS[id]}</span>
            </li>
          ))}
        </ol>
        {step === 'claim' && <StepAccount status={status} onNext={() => void claimed()} />}
        {step === 'signin' && <StepSignIn onNext={() => advance()} />}
        {step === 'copy' && <StepCopy sources={sources ?? []} from={from} onNext={() => advance()} />}
        {step === 'agents' && <StepAgents last={nextStep(steps, 'agents') === null} named={!isDefault} onNext={() => advance()} />}
        {step === 'summary' && <StepSummary instance={instance} siblings={sources ?? []} onDone={onDone} />}
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

function StepAccount({ status, onNext }: { status: SetupStatus | null; onNext: () => void }) {
  const [code, setCode] = useState(() => new URLSearchParams(window.location.search).get('code') ?? '');
  const [hubName, setHubName] = useState('');
  const [touchedName, setTouchedName] = useState(false);
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [submitted, setSubmitted] = useState(false);
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState('');
  const [codeError, setCodeError] = useState('');

  // the hostname (and the instance after it), until a name is typed
  const suggested = status?.suggestedHubName ?? '';
  useEffect(() => {
    if (!touchedName && suggested) setHubName(suggested);
  }, [suggested, touchedName]);

  const errors = submitted ? validate(code, hubName, password, confirm) : {};
  const named = !!status?.instance && status.instance !== 'default' && status.defaultInstance !== true;

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
      {named && (
        <div className="wizard-instance">
          <p>
            Instance <strong className="mono">{status?.instance}</strong>
          </p>
          <p className="field-hint">It was named when it was made, and so were its data folder, service and port. Where they are is shown once you are signed in.</p>
        </div>
      )}
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
          onChange={(e) => {
            setTouchedName(true);
            setHubName(e.target.value);
          }}
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

const AGENT_NAMES: Record<string, string> = { claude: 'Claude Code', codex: 'Codex', nextcode: 'NextCode', 'nextcode-codex': 'NextCode · Codex' };
/** The CLI whose own login a NextCode profile signs in with (it has no terminal). */
const SIGN_IN_CLI: Record<string, string> = { nextcode: 'claude', 'nextcode-codex': 'codex' };
const agentName = (id: string) => AGENT_NAMES[id] ?? id;

function StepSignIn({ onNext }: { onNext: () => void }) {
  const [mode, setMode] = useState<SignInMode>('separate');
  const [homes, setHomes] = useState<Profile[]>([]);
  const [signing, setSigning] = useState<Profile | null>(null);
  const [adding, setAdding] = useState(false);
  const [provider, setProvider] = useState<ApiProvider | null>(null);
  const [forRuns, setForRuns] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    api
      .profiles('local')
      .then((list) => setHomes(list.filter((p) => p.home && p.machineId === 'local')))
      .catch(() => undefined);
  }, []);

  const signIn = (p: Profile) => {
    store.connect(); // the sign-in console's state comes over the events socket (idempotent)
    setSigning(p);
  };

  const next = async () => {
    setError('');
    const patch = signInPatch(
      mode,
      homes.map((h) => ({ agent: h.agent, id: h.id })),
      mode === 'provider' && provider && forRuns ? provider.id : undefined,
    );
    if (patch) {
      setBusy(true);
      try {
        await api.patchSettings(patch);
      } catch (err) {
        setError(errorMessage(err));
        setBusy(false);
        return;
      }
    }
    onNext();
  };

  return (
    <div className="form">
      <div>
        <h2>How this instance’s agents sign in</h2>
        <p className="lead">Claude Code and Codex run on This PC. Pick whose accounts they use in this instance; Settings can change it later.</p>
      </div>
      <RadioCards<SignInMode>
        label="How agents sign in"
        columns={1}
        value={mode}
        onChange={setMode}
        options={[
          {
            value: 'separate',
            title: 'Separate NextBoard logins',
            description: 'Recommended. Each agent gets this instance’s own login, memory and settings, kept in its data folder and apart from every other instance.',
          },
          { value: 'own', title: 'My own CLI logins', description: 'The accounts of ~/.claude and ~/.codex, as when you run the CLI yourself.' },
          { value: 'provider', title: 'An API provider', description: 'An Anthropic- or OpenAI-compatible endpoint whose key this instance keeps, sealed with its own master key.' },
        ]}
      />
      {mode === 'separate' && (
        <div className="wizard-logins">
          {homes.length > 0 && (
            <ul className="plain wizard-login-list" aria-label="NextBoard logins of this instance">
              {homes.map((p) => (
                <li key={p.id} className="wizard-login-row">
                  <span className="grow truncate">
                    <strong>{agentName(p.agent)}</strong>
                    <span className="fs-xs text-3"> · {p.name}</span>
                  </span>
                  <Button size="sm" variant="secondary" icon="user" onClick={() => signIn(p)} aria-label={`Sign in to ${agentName(p.agent)}`}>
                    Sign in
                  </Button>
                </li>
              ))}
            </ul>
          )}
          <p className="field-hint">Sign in now or later: an agent without a login yet asks for one in its console.</p>
        </div>
      )}
      {mode === 'own' && (
        <div className="alert warning" role="note">
          <Icon name="alertCircle" />
          <span>These logins are shared with your terminal and with every instance that makes the same choice: the same accounts, conversations and memory everywhere.</span>
        </div>
      )}
      {mode === 'provider' && (
        <div className="wizard-logins">
          {provider ? (
            <>
              <p className="system-line">
                <Icon name="plug" />
                <span>“{provider.name}” is kept in this instance. Pick it where you start a console.</span>
              </p>
              <Checkbox checked={forRuns} onChange={setForRuns} label="Run the lead and the background runs on it" />
            </>
          ) : (
            <Button variant="secondary" icon="plus" onClick={() => setAdding(true)}>
              Add an API provider
            </Button>
          )}
          <p className="field-hint">Consoles keep this instance’s own logins too, for when you pick no provider.</p>
        </div>
      )}
      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
      <Button variant="primary" block loading={busy} onClick={() => void next()}>
        Continue
      </Button>
      <Suspense fallback={null}>
        {signing && (
          <ProfileSignInDialog
            profile={signing}
            agentName={agentName(signing.agent)}
            // NextCode signs in through its engine's own login (Claude Code's or Codex's)
            cliName={SIGN_IN_CLI[signing.agent] ? agentName(SIGN_IN_CLI[signing.agent]) : undefined}
            onClose={() => setSigning(null)}
          />
        )}
        {adding && <ProviderDialog provider={null} onSaved={setProvider} onClose={() => setAdding(false)} />}
      </Suspense>
    </div>
  );
}

const COPY_BOXES: { key: CopyKey; label: string; hint: string; secret?: { key: CopyKey; label: string } }[] = [
  { key: 'settings', label: 'Settings', hint: 'This instance keeps its own login profiles and preview ports.' },
  { key: 'presets', label: 'Custom agents and layouts', hint: 'Custom agents (presets.json) and saved board layouts.' },
  { key: 'mcp', label: 'MCP servers', hint: 'Without their values, a server that has any comes turned off.', secret: { key: 'mcpSecrets', label: 'Their header and environment values' } },
  { key: 'machines', label: 'SSH machines', hint: 'They come disconnected, with the host keys accepted there.', secret: { key: 'machineCredentials', label: 'Their stored keys and passwords' } },
  { key: 'providers', label: 'API providers', hint: 'Without their keys, each asks for one in Settings.', secret: { key: 'providerKeys', label: 'Their API keys' } },
  { key: 'vault', label: 'Vault', hint: 'As ciphertext: the same master password opens it.' },
];

function StepCopy({ sources, from, onNext }: { sources: ImportSource[]; from: string; onNext: () => void }) {
  const [source, setSource] = useState(() => preselectSource(sources, from));
  const [password, setPassword] = useState('');
  const [choices, setChoices] = useState<CopyChoices>(DEFAULT_COPY);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [result, setResult] = useState<ImportResult | null>(null);
  const secrets = secretsChosen(choices);

  const copy = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    if (!source) return setError('Pick the instance to copy from.');
    if (!password) return setError(`Enter the admin password of ${source}.`);
    if (!anythingChosen(choices)) return setError('Tick at least one thing to copy.');
    setBusy(true);
    try {
      setResult(await setupApi.importFrom(importBody(source, password, choices)));
      setPassword('');
    } catch (err) {
      if (err instanceof ApiError && err.code === 'bad_password') setError(`Wrong admin password of ${source}: it is the one you sign in to ${source} with.`);
      else if (err instanceof ApiError && err.status === 429) setError('Too many wrong passwords from this address. Wait about 10 minutes, then try again.');
      else setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <form className="form" onSubmit={copy} noValidate>
      <div>
        <h2>Copy from another instance</h2>
        <p className="lead">Start from what an instance on this machine has. Its admin password shows it is yours; nothing there changes.</p>
      </div>
      <fieldset disabled={busy}>
        <SelectField label="Copy from" value={source} onChange={(e) => setSource(e.target.value)}>
          <option value="">Pick an instance…</option>
          {sources.map((s) => (
            <option key={s.name} value={s.name} disabled={!s.setUp}>
              {s.name}
              {s.hubName ? ` · ${s.hubName}` : ''}
              {s.setUp ? '' : ' (not set up yet)'}
            </option>
          ))}
        </SelectField>
        <TextField
          label={source ? `Admin password of ${source}` : 'Admin password of that instance'}
          type="password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          autoComplete="off"
          hint="Not this instance’s: the one you sign in to the other instance with."
        />
        <div className="wizard-copy-boxes" role="group" aria-label="What to copy">
          {COPY_BOXES.map((b) => (
            <div key={b.key} className="wizard-copy-box">
              <Checkbox checked={choices[b.key]} onChange={(on) => setChoices((c) => setChoice(c, b.key, on))} label={b.label} />
              <p className="field-hint">{b.hint}</p>
              {b.secret && (
                <div className="wizard-copy-secret">
                  <Checkbox checked={choices[b.secret.key]} onChange={(on) => setChoices((c) => setChoice(c, b.secret!.key, on))} label={b.secret.label} />
                </div>
              )}
            </div>
          ))}
        </div>
        <p className="field-hint">
          {secrets.length > 0
            ? `Also coming: ${secrets.join(', ')}. Keys and passwords are opened with the other instance’s master key and sealed again with this one’s.`
            : 'No secret comes: keys, passwords and the vault stay where they are unless you tick them.'}{' '}
          It never brings its admin, paired devices, consoles, boards, projects, memory or logins.
        </p>
      </fieldset>
      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
      {result && (
        <div className="wizard-copied" role="status">
          <p className="system-line">
            <Icon name="check" />
            <span>
              Copied from {result.from}: {copiedSummary(result.copied)}.
            </span>
          </p>
          {result.notes.length > 0 && (
            <ul className="wizard-notes fs-xs text-2">
              {result.notes.map((n) => (
                <li key={n}>{n}</li>
              ))}
            </ul>
          )}
        </div>
      )}
      <div className="wizard-actions">
        <Button type="submit" variant={result ? 'secondary' : 'primary'} icon="download" loading={busy} disabled={sources.length === 0}>
          Copy
        </Button>
        <Button type="button" variant={result ? 'primary' : 'secondary'} onClick={onNext} disabled={busy}>
          Continue
        </Button>
      </div>
    </form>
  );
}

function StepAgents({ last, named, onNext }: { last: boolean; named: boolean; onNext: () => void }) {
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
        Missing one? Install and log in to it as the user running NextBoard, then use Machines → Re-probe. Custom presets live in{' '}
        {named ? (
          <>
            <code>presets.json</code> in this instance’s data folder.
          </>
        ) : (
          <>
            <code>~/.nextboard/presets.json</code>.
          </>
        )}
      </p>
      {last ? (
        <Button variant="primary" block icon="grid" onClick={onNext} autoFocus>
          Open board
        </Button>
      ) : (
        <Button variant="primary" block onClick={onNext} autoFocus>
          Continue
        </Button>
      )}
    </div>
  );
}

/** A path with a copy button; over plain HTTP the copy falls back to selecting it (lib/clipboard). */
function PathValue({ label, value }: { label: string; value?: string }) {
  const [copied, setCopied] = useState(false);
  if (!value) return <dd className="text-3">—</dd>;
  return (
    <dd className="wizard-path">
      <span className="mono">{value}</span>
      <Button
        size="sm"
        variant="ghost"
        iconOnly
        icon={copied ? 'check' : 'clipboard'}
        aria-label={`Copy the ${label.toLowerCase()} path`}
        onClick={() => void copyText(value).then((ok) => ok && setCopied(true))}
      />
    </dd>
  );
}

function StepSummary({ instance, siblings, onDone }: { instance: string; siblings: ImportSource[]; onDone: () => void }) {
  const [hub, setHub] = useState<WizardHub | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    setupApi
      .hub()
      .then(setHub)
      .catch((err) => setError(errorMessage(err)));
  }, []);

  const addresses = hub ? markAddresses(hub.urls ?? [], window.location) : [];
  const listen = hub?.listen ?? '';
  // two hubs called the same look the same in the header and in the app
  const twin = hub ? siblings.find((x) => x.hubName && x.hubName.trim().toLowerCase() === hub.name.trim().toLowerCase()) : undefined;

  return (
    <div className="form">
      <div>
        <h2>This instance is ready</h2>
        <p className="lead">Where it keeps everything. About this hub shows the same later.</p>
      </div>
      {error && (
        <div className="alert" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
      <dl className="kv wizard-summary">
        <dt>Instance</dt>
        <dd className="mono">{hub?.instance?.name ?? instance}</dd>
        <dt>Hub name</dt>
        <dd>{hub?.name ?? '—'}</dd>
        <dt>Data folder</dt>
        <PathValue label="Data folder" value={hub?.dataDir} />
        <dt>Config file</dt>
        <PathValue label="Config file" value={hub?.configFile} />
        <dt>Service</dt>
        <dd>{hub ? serviceText(hub.service) : '—'}</dd>
        <dt>Port</dt>
        <dd className="mono tabular">
          {portOf(listen) || '—'}
          {listen && <span className="text-3"> · listening on {listen}</span>}
        </dd>
        <dt>Addresses</dt>
        <dd>
          {addresses.length === 0 ? (
            '—'
          ) : (
            <ul className="plain wizard-addresses">
              {addresses.map((a) => (
                <li key={a.url}>
                  <span className="mono">{a.url}</span>
                  {a.here && <span className="badge badge-info">this browser</span>}
                </li>
              ))}
            </ul>
          )}
        </dd>
        <dt>Status page</dt>
        <dd>{hub?.statusListen ? <span className="mono">{hub.statusListen}</span> : 'None'}</dd>
      </dl>
      {listen && beyondLoopback(listen) && (
        <div className="alert warning" role="note">
          <Icon name="alertCircle" />
          <span>This hub listens beyond this machine over plain HTTP, which others on the network path see unencrypted. Put HTTPS in front of it, or reach it through a tunnel.</span>
        </div>
      )}
      {twin && (
        <div className="alert warning" role="note">
          <Icon name="alertCircle" />
          <span>
            The instance {twin.name} has the same hub name, “{twin.hubName}”: give one of them another name so the header and the app tell them apart.
          </span>
        </div>
      )}
      <p className="wizard-isolation">{ISOLATION_NOTE}</p>
      <Button variant="primary" block icon="grid" onClick={onDone} autoFocus>
        Open the board
      </Button>
    </div>
  );
}
