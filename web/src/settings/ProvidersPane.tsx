// Settings › API providers (docs/API-agents.md §10.2): Anthropic- or OpenAI-compatible endpoints
// a console of Claude Code, Codex or OpenCode can run on instead of a subscription login, with
// their prices and a monthly budget. The key is typed here once; the hub never shows it again.

import { useEffect, useState, type FormEvent } from 'react';
import { ApiError, errorMessage } from '../api';
import { Button } from '../components/Button';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Field, SelectField, TextField } from '../components/form';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import {
  money,
  providerModels,
  providersApi,
  providersStore,
  tokensShort,
  useProviders,
  type ApiProvider,
  type ProviderDraft,
  type ProviderPrice,
  type Spend,
  type TestResult,
} from '../providers/providers';

function runsWith(p: ApiProvider): string {
  const clis = p.kind === 'anthropic' ? ['Claude Code', 'OpenCode'] : p.api === 'responses' ? ['Codex', 'OpenCode'] : ['OpenCode'];
  return clis.join(', ');
}

function kindLabel(p: Pick<ApiProvider, 'kind' | 'api'>): string {
  if (p.kind === 'anthropic') return 'Anthropic-compatible';
  return p.api === 'responses' ? 'OpenAI · Responses' : 'OpenAI · Chat Completions';
}

export function ProvidersPane() {
  const list = useProviders();
  const [editing, setEditing] = useState<ApiProvider | 'new' | null>(null);
  const [removing, setRemoving] = useState<ApiProvider | null>(null);
  const [open, setOpen] = useState<string | null>(null);

  return (
    <div className="settings-cards">
      <section className="card settings-card" aria-labelledby="ap-title">
        <div className="settings-card-head">
          <h3 id="ap-title" className="section-title">
            API providers
          </h3>
          <Button size="sm" variant="primary" icon="plus" onClick={() => setEditing('new')}>
            Add provider
          </Button>
        </div>
        <p className="setting-hint">
          Run a console on another model through an API instead of a subscription login: GLM, Kimi, DeepSeek, OpenRouter, a local Ollama… Claude Code needs an Anthropic-compatible
          endpoint, Codex an OpenAI one with the Responses API, and OpenCode takes either. Pick a provider where you pick a login profile when you start a console. The key is sealed on
          the hub and only goes into the environment of the consoles that use it, where those agents can read it.
        </p>
        {list.length === 0 && <div className="list-msg">No providers yet. Consoles use each CLI’s login.</div>}
        <ul className="plain provider-list">
          {list.map((p) => (
            <li key={p.id} className="provider-row">
              <div className="provider-main">
                <div className="row provider-title">
                  <strong className="truncate">{p.name}</strong>
                  <span className="cap-chip">{kindLabel(p)}</span>
                  {!p.hasKey && <span className="badge badge-warning">No key</span>}
                  {!!p.inUse && <span className="badge badge-info">{p.inUse} running</span>}
                </div>
                <div className="mono fs-xs text-3 truncate" title={p.baseUrl}>
                  {p.baseUrl}
                </div>
                <div className="fs-xs text-3">
                  {p.model}
                  {p.fastModel ? ` · fast: ${p.fastModel}` : ''} · for {runsWith(p)}
                </div>
                <BudgetLine p={p} />
              </div>
              <div className="profile-actions">
                <Button size="sm" variant="ghost" onClick={() => setOpen(open === p.id ? null : p.id)} aria-expanded={open === p.id}>
                  This month
                </Button>
                <Button size="sm" variant="ghost" iconOnly icon="pencil" aria-label={`Edit ${p.name}`} onClick={() => setEditing(p)} />
                <Button size="sm" variant="danger-ghost" iconOnly icon="trash" aria-label={`Remove ${p.name}`} onClick={() => setRemoving(p)} />
              </div>
              {open === p.id && <SpendTable id={p.id} />}
            </li>
          ))}
        </ul>
      </section>
      {editing && <ProviderDialog provider={editing === 'new' ? null : editing} onClose={() => setEditing(null)} />}
      {removing && (
        <ConfirmDialog
          title={`Remove “${removing.name}”?`}
          confirmLabel="Remove"
          danger
          onClose={() => setRemoving(null)}
          onConfirm={async () => {
            try {
              await providersApi.remove(removing.id);
              providersStore.remove(removing.id);
              setRemoving(null);
            } catch (err) {
              toast.error(err instanceof ApiError && err.code === 'provider_in_use' ? 'Consoles still run on it: close or restart them on a login first.' : errorMessage(err));
            }
          }}
        >
          Its key is deleted. Consoles that ran on it start on their login the next time.
        </ConfirmDialog>
      )}
    </div>
  );
}

function BudgetLine({ p }: { p: ApiProvider }) {
  const cost = p.monthCost ?? 0;
  if (!p.budget) return <div className="fs-xs text-3">This month: {money(cost)}</div>;
  const share = Math.min(1, cost / p.budget);
  return (
    <div className="provider-budget">
      <div
        className={`provider-budget-bar ${share >= 1 ? 'is-over' : share >= 0.8 ? 'is-near' : ''}`}
        role="meter"
        aria-label={`Budget of ${p.name}`}
        aria-valuemin={0}
        aria-valuemax={p.budget}
        aria-valuenow={cost}
      >
        <span style={{ width: `${share * 100}%` }} />
      </div>
      <span className="fs-xs text-3">
        {money(cost)} of {money(p.budget)} this month
        {share >= 1 ? ' · spent: no new consoles or hidden runs on it' : share >= 0.8 ? ` · ${Math.floor(share * 100)}% used` : ''}
      </span>
    </div>
  );
}

function SpendTable({ id }: { id: string }) {
  const [s, setS] = useState<Spend | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    providersApi
      .spend(id)
      .then(setS)
      .catch((err) => setError(errorMessage(err)));
  }, [id]);
  if (error) return <p className="field-error">{error}</p>;
  if (!s) return <p className="fs-xs text-3">Reading…</p>;
  if (!s.byModel.length) return <p className="fs-xs text-3 provider-spend">Nothing used in {s.month} yet.</p>;
  return (
    <div className="provider-spend">
      <table className="spend-table">
        <thead>
          <tr>
            <th>Model</th>
            <th>In</th>
            <th>Out</th>
            <th>Cache</th>
            <th>Cost</th>
          </tr>
        </thead>
        <tbody>
          {s.byModel.map((m) => (
            <tr key={m.model}>
              <td className="mono">{m.model}</td>
              <td>{tokensShort(m.input)}</td>
              <td>{tokensShort(m.output)}</td>
              <td>{tokensShort(m.cacheRead + m.cacheWrite)}</td>
              <td>{m.priced ? money(m.cost) : 'no price'}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="fs-xs text-3">
        Consoles of Claude Code and Codex and the hidden runs made through it. OpenCode keeps its own records, which are not counted.
        {s.unpriced?.length ? ` Set a price for ${s.unpriced.join(', ')} to count it.` : ''}
      </p>
    </div>
  );
}

const EXAMPLES: Record<string, string> = {
  anthropic: 'For example https://api.z.ai/api/anthropic, https://api.deepseek.com/anthropic or https://api.moonshot.ai/anthropic',
  openai: 'For example https://openrouter.ai/api/v1, https://api.openai.com/v1 or http://localhost:11434/v1 (Ollama)',
};

type PriceRow = { model: string; input: string; output: string; cacheRead: string; cacheWrite: string };

function priceRows(p: ApiProvider | null): PriceRow[] {
  const rows: PriceRow[] = Object.entries(p?.prices ?? {}).map(([model, pr]) => ({
    model,
    input: String(pr.input ?? ''),
    output: String(pr.output ?? ''),
    cacheRead: pr.cacheRead ? String(pr.cacheRead) : '',
    cacheWrite: pr.cacheWrite ? String(pr.cacheWrite) : '',
  }));
  return rows.length ? rows : [{ model: p?.model ?? '', input: '', output: '', cacheRead: '', cacheWrite: '' }];
}

function ProviderDialog({ provider, onClose }: { provider: ApiProvider | null; onClose: () => void }) {
  const [kind, setKind] = useState<ApiProvider['kind']>(provider?.kind ?? 'anthropic');
  const [name, setName] = useState(provider?.name ?? '');
  const [baseUrl, setBaseUrl] = useState(provider?.baseUrl ?? '');
  const [api, setApi] = useState<NonNullable<ApiProvider['api']>>(provider?.api ?? 'chat');
  const [auth, setAuth] = useState<NonNullable<ApiProvider['auth']>>(provider?.auth ?? 'bearer');
  const [key, setKey] = useState('');
  const [model, setModel] = useState(provider?.model ?? '');
  const [fastModel, setFastModel] = useState(provider?.fastModel ?? '');
  const [models, setModels] = useState<string[]>(provider ? providerModels(provider) : []);
  const [prices, setPrices] = useState<PriceRow[]>(priceRows(provider));
  const [budget, setBudget] = useState(provider?.budget ? String(provider.budget) : '');
  const [test, setTest] = useState<TestResult | null>(null);
  const [testing, setTesting] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const draft = (): ProviderDraft => ({
    name: name.trim(),
    kind,
    baseUrl: baseUrl.trim(),
    api: kind === 'openai' ? api : undefined,
    auth: kind === 'anthropic' ? auth : undefined,
    model: model.trim(),
    fastModel: kind === 'anthropic' ? fastModel.trim() : '',
    apiKey: key.trim(),
  });

  const runTest = async () => {
    setTesting(true);
    setTest(null);
    try {
      const r = await providersApi.test({ ...draft(), id: provider?.id });
      setTest(r);
      if (r.ok && r.models?.length) setModels(r.models);
    } catch (err) {
      setTest({ ok: false, error: errorMessage(err) });
    } finally {
      setTesting(false);
    }
  };

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setError('');
    const priced: Record<string, ProviderPrice> = {};
    for (const r of prices) {
      if (!r.model.trim() || (r.input === '' && r.output === '')) continue;
      const n = (v: string) => (v.trim() === '' ? 0 : Number(v));
      const pr = { input: n(r.input), output: n(r.output), cacheRead: n(r.cacheRead), cacheWrite: n(r.cacheWrite) };
      if (Object.values(pr).some((v) => !Number.isFinite(v) || v < 0)) {
        setError(`The prices of ${r.model} are dollars per million tokens.`);
        return;
      }
      priced[r.model.trim()] = pr;
    }
    const b = budget.trim() === '' ? 0 : Number(budget);
    if (!Number.isFinite(b) || b < 0) {
      setError('The budget is dollars per month, or empty for none.');
      return;
    }
    setBusy(true);
    try {
      const body: ProviderDraft = { ...draft(), models, prices: priced, budget: b };
      const saved = provider ? await providersApi.update(provider.id, body) : await providersApi.create(body);
      providersStore.upsert(saved);
      toast.success(provider ? `“${saved.name}” saved.` : `“${saved.name}” added. Pick it where you start a console.`);
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  };

  const setPrice = (i: number, patch: Partial<PriceRow>) => setPrices((rows) => rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  return (
    <Modal
      title={provider ? `Edit “${provider.name}”` : 'Add an API provider'}
      icon="plug"
      size="lg"
      onClose={onClose}
      dismissable={!busy}
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" form="provider-form" variant="primary" loading={busy}>
            {provider ? 'Save' : 'Add provider'}
          </Button>
        </>
      }
    >
      <form id="provider-form" className="form-grid provider-form" onSubmit={submit} noValidate>
        <SelectField label="Type" value={kind} onChange={(e) => setKind(e.target.value as ApiProvider['kind'])}>
          <option value="anthropic">Anthropic-compatible (Claude Code, OpenCode)</option>
          <option value="openai">OpenAI-compatible (Codex, OpenCode)</option>
        </SelectField>
        <TextField label="Name" value={name} maxLength={40} placeholder="GLM" onChange={(e) => setName(e.target.value)} />
        <TextField label="Address" className="mono" value={baseUrl} placeholder={kind === 'anthropic' ? 'https://api.z.ai/api/anthropic' : 'https://openrouter.ai/api/v1'} hint={EXAMPLES[kind]} onChange={(e) => setBaseUrl(e.target.value)} />
        {kind === 'openai' ? (
          <SelectField label="API" value={api} onChange={(e) => setApi(e.target.value as 'chat' | 'responses')} hint="Codex speaks only the Responses API; most services have only Chat Completions, which OpenCode speaks.">
            <option value="chat">Chat Completions</option>
            <option value="responses">Responses API</option>
          </SelectField>
        ) : (
          <SelectField label="Key header" value={auth} onChange={(e) => setAuth(e.target.value as 'bearer' | 'x-api-key')} hint="Services that work with Claude Code take the key as a Bearer token; Anthropic’s own API takes x-api-key.">
            <option value="bearer">Authorization: Bearer (most services)</option>
            <option value="x-api-key">x-api-key (Anthropic)</option>
          </SelectField>
        )}
        <Field label="API key" hint={provider?.hasKey ? 'Saved. Type a new one to replace it.' : 'A server on this machine, such as Ollama, may need none.'}>
          {({ labelId: _l, ...a11y }) => (
            <input
              className="control mono"
              type="password"
              autoComplete="off"
              spellCheck={false}
              placeholder={provider?.hasKey ? '••••••••' : ''}
              value={key}
              onChange={(e) => setKey(e.target.value)}
              {...a11y}
            />
          )}
        </Field>
        <div className="provider-test">
          <Button size="sm" icon="refresh" loading={testing} disabled={!baseUrl.trim()} onClick={() => void runTest()}>
            Test the connection
          </Button>
          {test && (
            <span className={`fs-sm ${test.ok ? 'text-success' : 'field-error'}`} role="status">
              <Icon name={test.ok ? 'check' : 'alertCircle'} />
              {test.ok ? (test.checked === 'models' ? `It answers, with ${test.models?.length ?? 0} models.` : 'It answers.') : test.error}
            </span>
          )}
        </div>
        <TextField label="Model" className="mono" value={model} placeholder="glm-4.6" list="provider-models" onChange={(e) => setModel(e.target.value)} hint="What its consoles run unless you pick another." />
        {kind === 'anthropic' && (
          <TextField label="Fast model" optional className="mono" value={fastModel} list="provider-models" onChange={(e) => setFastModel(e.target.value)} hint="For Claude Code’s small tasks (its “haiku”). Empty = the model above." />
        )}
        <datalist id="provider-models">
          {models.map((m) => (
            <option key={m} value={m} />
          ))}
        </datalist>
        <fieldset className="provider-prices">
          <legend className="field-label">Prices, in dollars per million tokens</legend>
          <div className="price-grid" role="table" aria-label="Prices">
            <span role="columnheader">Model</span>
            <span role="columnheader">Input</span>
            <span role="columnheader">Output</span>
            <span role="columnheader">Cache read</span>
            <span role="columnheader">Cache write</span>
            <span />
            {prices.map((r, i) => (
              <div key={i} role="row" className="price-row">
                <input className="control control-sm mono" aria-label={`Model of price ${i + 1}`} list="provider-models" value={r.model} onChange={(e) => setPrice(i, { model: e.target.value })} />
                {(['input', 'output', 'cacheRead', 'cacheWrite'] as const).map((k) => (
                  <input key={k} className="control control-sm tabular" inputMode="decimal" aria-label={`${k} price of ${r.model || 'the model'}`} value={r[k]} onChange={(e) => setPrice(i, { [k]: e.target.value })} />
                ))}
                <Button size="sm" variant="ghost" iconOnly icon="close" aria-label={`Remove the price of ${r.model || 'this row'}`} onClick={() => setPrices((rows) => rows.filter((_, j) => j !== i))} />
              </div>
            ))}
          </div>
          <Button size="sm" variant="ghost" icon="plus" onClick={() => setPrices((rows) => [...rows, { model: '', input: '', output: '', cacheRead: '', cacheWrite: '' }])}>
            Price for another model
          </Button>
        </fieldset>
        <TextField
          label="Monthly budget"
          optional
          inputMode="decimal"
          value={budget}
          placeholder="Dollars"
          onChange={(e) => setBudget(e.target.value)}
          hint="Once spent, no new console or hidden run starts on it until next month. Running consoles go on."
        />
        {error && (
          <p className="field-error" role="alert">
            <Icon name="alertCircle" />
            {error}
          </p>
        )}
      </form>
    </Modal>
  );
}
