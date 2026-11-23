// New and edit connection, in DBeaver's flow (the owner knows it; docs/API-db.md): a wizard
// Select your database › Connection settings (Main, SSH, SSL, Driver properties) › Details; and
// Edit connection with the same pages in a left navigation. Secrets go to the hub once and are
// sealed there; the hub never sends them back.

import { Children, cloneElement, isValidElement, useEffect, useId, useMemo, useState, type ReactNode } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { Modal } from '../components/Modal';
import { toast } from '../components/Toast';
import type { SSHConfigHost } from '../lib/types';
import { useMachines, useProject } from '../store';
import { dbApi } from './api';
import { DriverMark, EnvTag } from './Confirm';
import { rememberConnection } from './store';
import {
  blankConnection,
  defaultSecurity,
  driverOf,
  DRIVERS,
  ENVS,
  isKV,
  LATER_DRIVERS,
  type Activity,
  type AgentAccess,
  type Candidate,
  type Connection,
  type Driver,
  type Env,
  type Hop,
  type Secrets,
  type TestResult,
} from './types';

type Page = 'main' | 'ssh' | 'ssl' | 'props' | 'general' | 'security' | 'agents' | 'activity';

interface Form {
  c: Connection;
  /** the password typed; undefined keeps the one kept */
  password?: string;
  /** per hop: a password or a key's passphrase typed, an uploaded key */
  hopSecrets: (string | null)[];
  hopKeys: (string | null)[];
  detectRef?: string;
}

function secretsOf(f: Form): Secrets {
  const s: Secrets = {};
  if (f.password !== undefined) s.password = f.password;
  if (f.hopSecrets.some((x) => x !== null)) s.hopSecrets = f.hopSecrets;
  if (f.hopKeys.some((x) => x !== null)) s.hopKeys = f.hopKeys;
  return s;
}

const clone = <T,>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

// ---- the wizard --------------------------------------------------------------------------------

export function NewConnection({ projectId, onClose, onDone }: { projectId: string; onClose: () => void; onDone?: (c: Connection) => void }) {
  const [step, setStep] = useState(1);
  const [f, setF] = useState<Form>({ c: blankConnection(projectId), hopSecrets: [], hopKeys: [] });
  const [tab, setTab] = useState<Page>('main');
  const [test, setTest] = useState<TestResult | null>(null);
  const [testing, setTesting] = useState(false);
  const [saving, setSaving] = useState(false);
  const [trust, setTrust] = useState<TestResult['hostKey'] | null>(null);

  const pick = (d: Driver) => {
    const c = { ...f.c, driver: d, port: driverOf(d).port };
    setF({ ...f, c });
  };

  const useCandidate = (cand: Candidate) => {
    const c = { ...clone(cand.connection), id: '', projectId };
    c.security = defaultSecurity(c.env);
    setF({ c, hopSecrets: [], hopKeys: [], detectRef: cand.ref });
    setStep(2);
    setTab('main');
  };

  const runTest = async () => {
    setTesting(true);
    setTest(null);
    try {
      const r = await dbApi.test(f.c, secretsOf(f), undefined, f.detectRef);
      setTest(r);
      if (r.hostKey && !r.hostKey.changed) setTrust(r.hostKey);
    } catch (e) {
      setTest({ ok: false, ms: 0, tables: 0, keys: 0, error: errorMessage(e) });
    } finally {
      setTesting(false);
    }
  };

  const finish = async () => {
    setSaving(true);
    try {
      const c = await dbApi.create(projectId, f.c, secretsOf(f), f.detectRef);
      rememberConnection(c);
      toast.success(`${c.name} added`);
      onDone?.(c);
      onClose();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setSaving(false);
    }
  };

  const title = step === 1 ? 'Select your database' : step === 2 ? 'Connection settings' : 'Connection details';
  const desc = step === 1 ? 'Create a connection of this project. You can change the driver later.' : step === 2 ? `${driverOf(f.c.driver).label} connection settings: the server and how to reach it.` : 'Name, type and what may be done on this connection.';

  return (
    <Modal title={title} icon="database" size="lg" onClose={onClose} className="db-dialog"
      footer={
        <>
          {step > 1 && (
            <Button icon="refresh" loading={testing} onClick={() => void runTest()}>
              Test connection…
            </Button>
          )}
          <TestLine r={test} />
          <span className="db-spacer" />
          <Button disabled={step === 1} onClick={() => setStep(step - 1)}>
            ‹ Back
          </Button>
          <Button variant={step < 3 ? 'primary' : 'secondary'} disabled={step === 3} onClick={() => setStep(step + 1)}>
            Next ›
          </Button>
          <Button variant={step === 3 ? 'primary' : 'secondary'} disabled={step === 1} loading={saving} onClick={() => void finish()}>
            Finish
          </Button>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
        </>
      }>
      <div className="db-wiz-desc text-2 fs-sm">{desc}</div>
      <Steps step={step} />
      {step === 1 && <SelectPage projectId={projectId} driver={f.c.driver} onPick={pick} onCandidate={useCandidate} onNext={() => setStep(2)} />}
      {step === 2 && (
        <>
          <Tabs tab={tab} setTab={setTab} pages={['main', 'ssh', 'ssl', 'props']} f={f} />
          <PageBody page={tab} f={f} setF={setF} projectId={projectId} test={test} testing={testing} onTest={() => void runTest()} />
        </>
      )}
      {step === 3 && <DetailsPages f={f} setF={setF} wizard />}
      {trust && <TrustDialog hk={trust} onClose={() => setTrust(null)} onTrust={() => {
        const hops = [...(f.c.reach.hops ?? [])];
        if (hops[trust.hop]) hops[trust.hop] = { ...hops[trust.hop], hostKey: trust.fingerprint };
        setF({ ...f, c: { ...f.c, reach: { ...f.c.reach, hops } } });
        setTrust(null);
        setTimeout(() => void runTest(), 0);
      }} />}
    </Modal>
  );
}

// ---- edit -----------------------------------------------------------------------------------

export function EditConnection({ conn, onClose, initialPage }: { conn: Connection; onClose: () => void; initialPage?: Page }) {
  const [f, setF] = useState<Form>({ c: clone(conn), hopSecrets: (conn.reach.hops ?? []).map(() => null), hopKeys: (conn.reach.hops ?? []).map(() => null) });
  const [page, setPage] = useState<Page>(initialPage ?? 'main');
  const [test, setTest] = useState<TestResult | null>(null);
  const [testing, setTesting] = useState(false);
  const [saving, setSaving] = useState(false);
  const [trust, setTrust] = useState<TestResult['hostKey'] | null>(null);
  const [removing, setRemoving] = useState(false);

  const runTest = async () => {
    setTesting(true);
    try {
      const r = await dbApi.test(f.c, secretsOf(f), conn.id);
      setTest(r);
      if (r.hostKey && !r.hostKey.changed) setTrust(r.hostKey);
    } catch (e) {
      setTest({ ok: false, ms: 0, tables: 0, keys: 0, error: errorMessage(e) });
    } finally {
      setTesting(false);
    }
  };

  const save = async (close: boolean) => {
    setSaving(true);
    try {
      const c = await dbApi.replace(conn.id, f.c, secretsOf(f));
      rememberConnection(c);
      setF({ ...f, c: clone(c), password: undefined, hopSecrets: (c.reach.hops ?? []).map(() => null), hopKeys: (c.reach.hops ?? []).map(() => null) });
      toast.success('Saved');
      if (close) onClose();
    } catch (e) {
      toast.error(errorMessage(e));
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    try {
      await dbApi.remove(conn.id);
      toast.success(`${conn.name} removed`);
      onClose();
    } catch (e) {
      toast.error(errorMessage(e));
    }
  };

  const nav: { h?: string; page?: Page; label?: string; icon?: Parameters<typeof Icon>[0]['name']; sub?: boolean }[] = [
    { h: 'Connection settings' },
    { page: 'main', label: 'Main', icon: 'server', sub: true },
    { page: 'ssh', label: 'SSH', icon: 'link', sub: true },
    { page: 'ssl', label: 'SSL', icon: 'lock', sub: true },
    { page: 'props', label: 'Driver properties', icon: 'sliders', sub: true },
    { h: 'Connection' },
    { page: 'general', label: 'General', icon: 'fileText' },
    { page: 'security', label: 'Security', icon: 'shield' },
    { page: 'agents', label: 'Agents & SmartDB', icon: 'sparkle' },
    { page: 'activity', label: 'Activity', icon: 'history' },
  ];

  return (
    <Modal title={`Edit connection · ${conn.name}`} icon="database" size="lg" onClose={onClose} className="db-dialog"
      actions={<EnvTag env={f.c.env} />}
      footer={
        <>
          <Button icon="refresh" loading={testing} onClick={() => void runTest()}>
            Test connection…
          </Button>
          <TestLine r={test} />
          <span className="db-spacer" />
          <Button variant="danger-ghost" icon="trash" onClick={() => setRemoving(true)}>
            Remove
          </Button>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button loading={saving} onClick={() => void save(false)}>
            Apply
          </Button>
          <Button variant="primary" loading={saving} onClick={() => void save(true)}>
            OK
          </Button>
        </>
      }>
      <div className="db-edit">
        <nav className="db-lnav" aria-label="Connection pages">
          {nav.map((n, i) =>
            n.h ? (
              <div key={i} className="db-lnav-h">
                {n.h}
              </div>
            ) : (
              <button key={n.page} type="button" className={`${n.sub ? 'is-sub' : ''} ${page === n.page ? 'is-on' : ''}`} aria-current={page === n.page ? 'page' : undefined} onClick={() => setPage(n.page!)}>
                {n.icon && <Icon name={n.icon} />}
                {n.label}
                {n.page === 'ssh' && f.c.reach.kind === 'ssh' && <span className="dot dot-success" aria-label="on" />}
              </button>
            ),
          )}
        </nav>
        <div className="db-edit-page">
          {page === 'activity' ? (
            <ActivityPage id={conn.id} />
          ) : ['general', 'security', 'agents'].includes(page) ? (
            <DetailsPages f={f} setF={setF} only={page as 'general' | 'security' | 'agents'} />
          ) : (
            <PageBody page={page} f={f} setF={setF} projectId={conn.projectId} test={test} testing={testing} onTest={() => void runTest()} />
          )}
        </div>
      </div>
      {trust && <TrustDialog hk={trust} onClose={() => setTrust(null)} onTrust={() => {
        const hops = [...(f.c.reach.hops ?? [])];
        if (hops[trust.hop]) hops[trust.hop] = { ...hops[trust.hop], hostKey: trust.fingerprint };
        setF({ ...f, c: { ...f.c, reach: { ...f.c.reach, hops } } });
        setTrust(null);
        setTimeout(() => void runTest(), 0);
      }} />}
      {removing && (
        <Modal title={`Remove ${conn.name}?`} icon="trash" tone="danger" size="sm" onClose={() => setRemoving(false)}
          footer={<><Button variant="ghost" onClick={() => setRemoving(false)}>Cancel</Button><Button variant="danger" onClick={() => void remove()}>Remove</Button></>}>
          <p>Only NextBoard forgets the connection, its saved password and its history. The database itself is not touched.</p>
        </Modal>
      )}
    </Modal>
  );
}

// ---- shared pieces ------------------------------------------------------------------------------

function Steps({ step }: { step: number }) {
  const names = ['Database', 'Connection settings', 'Details'];
  return (
    <ol className="db-steps">
      {names.map((n, i) => (
        <li key={n} className={i + 1 < step ? 'is-done' : i + 1 === step ? 'is-on' : ''}>
          <span className="db-step-n">{i + 1 < step ? '✓' : i + 1}</span>
          {n}
        </li>
      ))}
    </ol>
  );
}

function Tabs({ tab, setTab, pages, f }: { tab: Page; setTab: (p: Page) => void; pages: Page[]; f: Form }) {
  const label: Record<string, string> = { main: 'Main', ssh: 'SSH', ssl: 'SSL', props: 'Driver properties' };
  return (
    <div className="db-wtabs" role="tablist">
      {pages.map((p) => (
        <button key={p} type="button" role="tab" aria-selected={tab === p} className={tab === p ? 'is-on' : ''} onClick={() => setTab(p)}>
          {label[p]}
          {p === 'ssh' && f.c.reach.kind === 'ssh' && <span className="db-tag-on">on</span>}
        </button>
      ))}
    </div>
  );
}

function TestLine({ r }: { r: TestResult | null }) {
  if (!r) return null;
  if (r.ok)
    return (
      <span className="db-test-ok" role="status">
        <Icon name="check" /> Connected · {r.version} · {r.ms} ms{r.tables ? ` · ${r.tables} tables` : ''}
        {r.keys ? ` · ${r.keys.toLocaleString()} keys` : ''}
      </span>
    );
  return (
    <span className="db-test-err" role="alert" title={r.error}>
      <Icon name="alertCircle" /> {r.error}
    </span>
  );
}

function TrustDialog({ hk, onTrust, onClose }: { hk: NonNullable<TestResult['hostKey']>; onTrust: () => void; onClose: () => void }) {
  return (
    <Modal title={`Trust ${hk.host}?`} icon="shield" size="sm" onClose={onClose}
      footer={<><Button variant="ghost" onClick={onClose}>Cancel</Button><Button variant="primary" onClick={onTrust}>Trust this key</Button></>}>
      <p className="text-2 fs-sm">This SSH server is not known yet. Check its key with whoever runs it (ssh-keygen -lf on the server), then trust it.</p>
      <dl className="db-kv">
        <dt>Key</dt>
        <dd>{hk.keyType}</dd>
        <dt>Fingerprint</dt>
        <dd className="mono">{hk.fingerprint}</dd>
      </dl>
    </Modal>
  );
}

/**
 * A row of a form: its label names the field (an input, select or textarea first among the
 * children), its hint describes it. `group` is a row of radios: a group named by the label.
 */
function Field({ label, children, hint, group }: { label: string; children: ReactNode; hint?: ReactNode; group?: boolean }) {
  const id = useId();
  const kids = Children.toArray(children);
  const first = kids[0];
  const named = !group && !!label && isValidElement<{ id?: string }>(first) && (first.type === 'input' || first.type === 'select' || first.type === 'textarea');
  const fieldId = named ? (first.props.id ?? id) : undefined;
  return (
    <div className="db-frow" role={group ? 'group' : undefined} aria-label={group ? label : undefined}>
      {named ? (
        <label className="db-frow-label" htmlFor={fieldId}>
          {label}
        </label>
      ) : (
        <span className="db-frow-label">{label}</span>
      )}
      <span className="db-frow-in">
        {named ? [cloneElement(first, { id: fieldId, 'aria-describedby': hint ? `${id}-hint` : undefined } as Record<string, unknown>), ...kids.slice(1)] : children}
        {hint && (
          <span className="db-hint" id={`${id}-hint`}>
            {hint}
          </span>
        )}
      </span>
    </div>
  );
}

function Check({ on, set, children }: { on: boolean; set: (v: boolean) => void; children: ReactNode }) {
  return (
    <label className="db-check">
      <input type="checkbox" checked={on} onChange={(e) => set(e.target.checked)} />
      <span>{children}</span>
    </label>
  );
}

function Fieldset({ legend, children }: { legend: ReactNode; children: ReactNode }) {
  return (
    <fieldset className="db-fieldset">
      <legend>{legend}</legend>
      {children}
    </fieldset>
  );
}

function SelectPage({ projectId, driver, onPick, onCandidate, onNext }: { projectId: string; driver: Driver; onPick: (d: Driver) => void; onCandidate: (c: Candidate) => void; onNext: () => void }) {
  const [q, setQ] = useState('');
  const [cat, setCat] = useState<'all' | 'popular' | 'sql' | 'kv'>('all');
  const [cands, setCands] = useState<Candidate[] | null>(null);
  useEffect(() => {
    let live = true;
    dbApi
      .detect(projectId)
      .then((r) => live && setCands(r.candidates))
      .catch(() => live && setCands([]));
    return () => {
      live = false;
    };
  }, [projectId]);
  const list = DRIVERS.filter((d) => (cat === 'all' || (cat === 'popular' && d.id !== 'valkey' && d.id !== 'mariadb') || (cat === 'sql' && !isKV(d.id)) || (cat === 'kv' && isKV(d.id))) && d.label.toLowerCase().includes(q.toLowerCase()));
  return (
    <div className="db-select">
      <nav className="db-cats" aria-label="Kinds of database">
        {(
          [
            ['all', 'All', 5],
            ['popular', 'Popular', 3],
            ['sql', 'SQL', 3],
            ['kv', 'Key-value', 2],
          ] as const
        ).map(([id, label, n]) => (
          <button key={id} type="button" className={cat === id ? 'is-on' : ''} onClick={() => setCat(id)}>
            {label}
            <span className="text-3">{n}</span>
          </button>
        ))}
        {['Document', 'Analytical', 'Embedded'].map((x) => (
          <span key={x} className="db-cat-later">
            {x}
            <span>later</span>
          </span>
        ))}
      </nav>
      <div className="db-select-main">
        <input className="control db-input" value={q} onChange={(e) => setQ(e.target.value)} placeholder="Type part of a database or driver name to filter" aria-label="Filter drivers" />
        {cands && cands.length > 0 && (
          <div className="db-banner db-banner-info">
            <Icon name="sparkle" />
            <span className="db-grow">
              Found in this project:{' '}
              {cands.map((c, i) => (
                <span key={c.ref}>
                  {i > 0 ? ', ' : ''}
                  <b>
                    {driverOf(c.connection.driver).label} {c.connection.database || ''}
                  </b>{' '}
                  in <span className="mono">{c.source}</span>
                </span>
              ))}
              . Pick one to fill every step for you, the password included.
            </span>
            {cands.slice(0, 4).map((c) => (
              <Button key={c.ref} size="sm" onClick={() => onCandidate(c)}>
                {c.connection.name}
              </Button>
            ))}
          </div>
        )}
        <div className="db-dgrid" role="listbox" aria-label="Drivers">
          {list.map((d) => (
            <button key={d.id} type="button" role="option" aria-selected={driver === d.id} className={`db-dtile ${driver === d.id ? 'is-on' : ''}`} onClick={() => onPick(d.id)} onDoubleClick={() => (onPick(d.id), onNext())}>
              <DriverMark driver={d.id} size="lg" />
              <b>{d.label}</b>
              <small>{d.sub}</small>
            </button>
          ))}
          {cat === 'all' &&
            !q &&
            LATER_DRIVERS.map((d) => (
              <div key={d.label} className="db-dtile is-off" aria-disabled="true">
                <span className="db-later">later</span>
                <span className="db-drv db-drv-lg">{d.mark}</span>
                <b>{d.label}</b>
                <small>{d.sub}</small>
              </div>
            ))}
        </div>
        <p className="db-hint">Every driver is built into the hub: nothing to download. MySQL is also the one for TiDB and PlanetScale; PostgreSQL for Supabase, Neon and CockroachDB.</p>
      </div>
    </div>
  );
}

function PageBody({ page, f, setF, projectId, test, testing, onTest }: { page: Page; f: Form; setF: (f: Form) => void; projectId: string; test: TestResult | null; testing: boolean; onTest: () => void }) {
  if (page === 'main') return <MainPage f={f} setF={setF} />;
  if (page === 'ssh') return <SSHPage f={f} setF={setF} projectId={projectId} test={test} testing={testing} onTest={onTest} />;
  if (page === 'ssl') return <SSLPage f={f} setF={setF} />;
  return <PropsPage f={f} setF={setF} />;
}

function MainPage({ f, setF }: { f: Form; setF: (f: Form) => void }) {
  const c = f.c;
  const set = (patch: Partial<Connection>) => setF({ ...f, c: { ...c, ...patch } });
  const kv = isKV(c.driver);
  const [byUrl, setByUrl] = useState(false);
  const url = `${kv ? 'redis' : c.driver === 'postgres' ? 'postgres' : 'mysql'}://${c.user ? c.user + '@' : ''}${c.host}:${c.port}${kv ? '/' + (c.redisDb ?? 0) : c.database ? '/' + c.database : ''}`;
  const fromURL = (raw: string) => {
    try {
      const u = new URL(raw);
      const patch: Partial<Connection> = { host: u.hostname, port: Number(u.port) || c.port, user: decodeURIComponent(u.username) };
      if (kv) patch.redisDb = Number(u.pathname.slice(1)) || 0;
      else patch.database = decodeURIComponent(u.pathname.slice(1));
      setF({ ...f, c: { ...c, ...patch }, password: u.password ? decodeURIComponent(u.password) : f.password });
    } catch {
      /* not a URL yet */
    }
  };
  return (
    <div className="db-page">
      {f.detectRef && (
        <div className="db-banner db-banner-info">
          <Icon name="sparkle" /> Filled from the project's .env. The password stays on the hub.
        </div>
      )}
      <Fieldset legend="Server">
        <Field label="Connect by" group>
          <span className="db-radios">
            <label>
              <input type="radio" checked={!byUrl} onChange={() => setByUrl(false)} /> Host
            </label>
            <label>
              <input type="radio" checked={byUrl} onChange={() => setByUrl(true)} /> URL
            </label>
          </span>
        </Field>
        {byUrl ? (
          <Field label="URL" hint="mysql://user:password@host:3306/db, postgres://…, redis://…">
            <input className="control db-input mono" defaultValue={url} onChange={(e) => fromURL(e.target.value)} />
          </Field>
        ) : (
          <>
            <div className="db-frow2">
              <Field label="Host">
                <input className="control db-input mono" value={c.host} onChange={(e) => set({ host: e.target.value })} />
              </Field>
              <Field label="Port">
                <input className="control db-input mono" inputMode="numeric" value={c.port || ''} onChange={(e) => set({ port: Number(e.target.value) || 0 })} />
              </Field>
            </div>
            {kv ? (
              <Field label="Database">
                <input className="control db-input mono" inputMode="numeric" value={c.redisDb ?? 0} onChange={(e) => set({ redisDb: Number(e.target.value) || 0 })} />
              </Field>
            ) : (
              <>
                <Field label="Database" hint={c.driver === 'postgres' ? 'postgres when empty' : 'empty: every database'}>
                  <input className="control db-input mono" value={c.database ?? ''} onChange={(e) => set({ database: e.target.value })} />
                </Field>
                {c.driver !== 'postgres' && (
                  <Field label="">
                    <Check on={!!c.showAll} set={(v) => set({ showAll: v })}>
                      Show all databases
                    </Check>
                  </Field>
                )}
              </>
            )}
            <Field label="URL">
              <input className="control db-input mono text-3" value={url} readOnly aria-label="The URL it makes" />
            </Field>
          </>
        )}
      </Fieldset>
      <Fieldset legend="Authentication">
        <Field label="Username" hint={kv ? 'an ACL user; empty for the default one' : undefined}>
          <input className="control db-input mono" value={c.user ?? ''} onChange={(e) => set({ user: e.target.value })} autoComplete="off" />
        </Field>
        <Field label="Password">
          <input
            className="control db-input mono"
            type="password"
            value={f.password ?? ''}
            placeholder={c.hasPassword && f.password === undefined ? '•••••••• kept on the hub' : ''}
            onChange={(e) => setF({ ...f, password: e.target.value })}
            autoComplete="new-password"
          />
        </Field>
        <Field label="" hint="Encrypted on the hub, never sent back to a browser. Off: asked when you connect.">
          <Check on={c.savePassword} set={(v) => set({ savePassword: v })}>
            Save password
          </Check>
        </Field>
      </Fieldset>
      <div className="db-banner db-banner-plain">
        <Icon name="link" /> Host and port are as the hub sees them. A database the hub cannot reach directly goes through the <b>SSH</b> tab: then they are as the SSH server sees them.
      </div>
    </div>
  );
}

function SSHPage({ f, setF, projectId, test, testing, onTest }: { f: Form; setF: (f: Form) => void; projectId: string; test: TestResult | null; testing: boolean; onTest: () => void }) {
  const c = f.c;
  const r = c.reach;
  const project = useProject(projectId);
  const machines = useMachines();
  const [hosts, setHosts] = useState<SSHConfigHost[]>([]);
  useEffect(() => {
    void api
      .sshConfigHosts()
      .then(setHosts)
      .catch(() => undefined);
  }, []);
  const projectMachines = useMemo(() => {
    const ids = new Set([project?.machineId, ...(project?.parts ?? []).map((p) => p.machineId)].filter(Boolean));
    return machines.filter((m) => ids.has(m.id) && m.id !== 'local');
  }, [project, machines]);
  const on = r.kind !== 'direct';
  const through: 'new' | 'machine' | 'config' = r.kind === 'machine' ? 'machine' : r.configHost ? 'config' : 'new';
  const hops = r.hops ?? [];
  const last = hops.length - 1;
  const setReach = (patch: Partial<typeof r>) => setF({ ...f, c: { ...c, reach: { ...r, ...patch } } });
  const setHop = (i: number, patch: Partial<Hop>) => setReach({ hops: hops.map((h, j) => (j === i ? { ...h, ...patch } : h)) });

  const turn = (v: boolean) => {
    if (!v) return setReach({ kind: 'direct' });
    if (hops.length === 0) return setF({ ...f, c: { ...c, reach: { ...r, kind: 'ssh', hops: [{ host: '', port: 22, user: '', auth: 'key', keyPath: '~/.ssh/id_ed25519' }] } }, hopSecrets: [null], hopKeys: [null] });
    setReach({ kind: 'ssh' });
  };
  const setThrough = (t: typeof through) => {
    if (t === 'machine') setReach({ kind: 'machine', machineId: projectMachines[0]?.id ?? '' });
    else if (t === 'config') setReach({ kind: 'ssh', configHost: hosts[0]?.host ?? '' });
    else {
      if (hops.length === 0) return turn(true);
      setReach({ kind: 'ssh', configHost: '' });
    }
  };
  const addJump = () => {
    setF({ ...f, c: { ...c, reach: { ...r, hops: [{ host: '', port: 22, user: '', auth: 'key', keyPath: '~/.ssh/id_ed25519' }, ...hops] } }, hopSecrets: [null, ...f.hopSecrets], hopKeys: [null, ...f.hopKeys] });
  };
  const removeJump = (i: number) => {
    setF({ ...f, c: { ...c, reach: { ...r, hops: hops.filter((_, j) => j !== i) } }, hopSecrets: f.hopSecrets.filter((_, j) => j !== i), hopKeys: f.hopKeys.filter((_, j) => j !== i) });
  };
  const setSecret = (i: number, v: string) => {
    const s = [...f.hopSecrets];
    while (s.length < hops.length) s.push(null);
    s[i] = v;
    setF({ ...f, hopSecrets: s });
  };
  const uploadKey = async (i: number, file: File) => {
    const text = await file.text();
    const k = [...f.hopKeys];
    while (k.length < hops.length) k.push(null);
    k[i] = text;
    setF({ ...f, hopKeys: k, c: { ...c, reach: { ...r, hops: hops.map((h, j) => (j === i ? { ...h, keyPath: '', auth: 'key' } : h)) } } });
  };

  const hopForm = (i: number, title: string) => {
    const h = hops[i];
    return (
      <Fieldset legend={title}>
        <div className="db-frow2">
          <Field label="Host / IP">
            <input className="control db-input mono" value={h.host} onChange={(e) => setHop(i, { host: e.target.value })} />
          </Field>
          <Field label="Port">
            <input className="control db-input mono" inputMode="numeric" value={h.port || ''} onChange={(e) => setHop(i, { port: Number(e.target.value) || 0 })} />
          </Field>
        </div>
        <Field label="User name">
          <input className="control db-input mono" value={h.user} onChange={(e) => setHop(i, { user: e.target.value })} />
        </Field>
        <Field label="Authentication">
          <select className="control db-input" value={h.auth} onChange={(e) => setHop(i, { auth: e.target.value as Hop['auth'] })}>
            <option value="key">Public key</option>
            <option value="password">Password</option>
            <option value="agent">SSH agent of This PC</option>
          </select>
        </Field>
        {h.auth === 'key' && (
          <Field label="Private key" hint={h.hasKey || f.hopKeys[i] ? 'an uploaded key, kept encrypted on the hub' : 'a file on This PC, read by the hub'}>
            <span className="db-inline">
              <input className="control db-input mono" aria-label="Private key file" value={h.keyPath ?? ''} placeholder={h.hasKey || f.hopKeys[i] ? 'uploaded' : '~/.ssh/id_ed25519'} onChange={(e) => setHop(i, { keyPath: e.target.value })} />
              <label className="btn btn-secondary btn-sm db-upload">
                Upload…
                <input type="file" hidden onChange={(e) => e.target.files?.[0] && void uploadKey(i, e.target.files[0])} />
              </label>
            </span>
          </Field>
        )}
        {h.auth !== 'agent' && (
          <Field label={h.auth === 'password' ? 'Password' : 'Passphrase'} hint="Encrypted on the hub">
            <input
              className="control db-input mono"
              type="password"
              value={f.hopSecrets[i] ?? ''}
              placeholder={h.hasSecret && f.hopSecrets[i] == null ? '•••••••• kept on the hub' : h.auth === 'key' ? 'empty when the key has none' : ''}
              onChange={(e) => setSecret(i, e.target.value)}
              autoComplete="new-password"
            />
          </Field>
        )}
        {h.hostKey && (
          <Field label="Host key">
            <span className="mono text-2 fs-xs">
              {h.hostKey} <Icon name="check" />
            </span>
          </Field>
        )}
      </Fieldset>
    );
  };

  const way = (
    <div className="db-route">
      <span className="text-3">The way:</span>
      <b>hub · This PC</b>
      {r.kind === 'machine' && (
        <>
          <span>→</span>
          <b>{machines.find((m) => m.id === r.machineId)?.name ?? r.machineId}</b>
          <span className="text-3">machine</span>
        </>
      )}
      {r.kind === 'ssh' && r.configHost && (
        <>
          <span>→</span>
          <b>{r.configHost}</b>
          <span className="text-3">~/.ssh/config</span>
        </>
      )}
      {r.kind === 'ssh' &&
        !r.configHost &&
        hops.map((h, i) => (
          <span key={i} className="db-route-hop">
            <span>→</span>
            <b>{h.host || '…'}</b>
            <span className="text-3">{i === last ? 'SSH host' : 'jump'}</span>
          </span>
        ))}
      <span>→</span>
      <b className="db-route-db">
        {r.remoteHost || c.host}:{r.remotePort || c.port}
      </b>
      <span className="text-3">{driverOf(c.driver).label}</span>
    </div>
  );

  return (
    <div className="db-page">
      <div className="db-ssh-top">
        <Check on={on} set={turn}>
          <b>Use SSH tunnel</b>
        </Check>
        {on && (
          <span className="db-radios">
            <span className="text-3">Through</span>
            <label>
              <input type="radio" checked={through === 'new'} onChange={() => setThrough('new')} /> A new SSH server
            </label>
            <label>
              <input type="radio" checked={through === 'machine'} disabled={projectMachines.length === 0} onChange={() => setThrough('machine')} /> A machine of this project
              {projectMachines.length > 0 && <span className="text-3"> ({projectMachines.map((m) => m.name).join(', ')})</span>}
            </label>
            <label>
              <input type="radio" checked={through === 'config'} disabled={hosts.length === 0} onChange={() => setThrough('config')} /> A Host of ~/.ssh/config
            </label>
          </span>
        )}
      </div>
      {!on && <p className="db-hint">The hub dials the database itself. Turn the tunnel on when only an SSH server can reach it (a database that listens on the server only, or behind a bastion).</p>}
      {on && way}
      {on && through === 'machine' && (
        <Fieldset legend="Machine">
          <Field label="Machine" hint="Over the SSH connection NextBoard already keeps: nothing to set up, the host and port of Main are as that machine sees them.">
            <select className="control db-input" value={r.machineId} onChange={(e) => setReach({ machineId: e.target.value })}>
              {projectMachines.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.name}
                </option>
              ))}
            </select>
          </Field>
        </Fieldset>
      )}
      {on && through === 'config' && (
        <Fieldset legend="~/.ssh/config">
          <Field label="Host" hint="Its HostName, User, IdentityFile and ProxyJump chain are followed; keys from the SSH agent too.">
            <select className="control db-input" value={r.configHost} onChange={(e) => setReach({ configHost: e.target.value })}>
              {hosts.map((h) => (
                <option key={h.host} value={h.host}>
                  {h.host} ({h.user ? h.user + '@' : ''}
                  {h.hostName || h.host}
                  {h.proxyJump ? ` via ${h.proxyJump}` : ''})
                </option>
              ))}
            </select>
          </Field>
        </Fieldset>
      )}
      {on && through === 'new' && hops.length > 0 && (
        <div className="db-ssh-grid">
          <div className="db-ssh-col">
            {hopForm(last, 'Settings')}
            <Fieldset legend="Jump servers">
              {hops.slice(0, last).map((h, i) => (
                <details key={i} className="db-hop">
                  <summary>
                    <span className="db-hop-n">{i + 1}</span>
                    <b className="mono">
                      {h.user || 'user'}@{h.host || 'host'}:{h.port}
                    </b>
                    <span className="text-3 fs-xs">{h.auth === 'password' ? 'password' : h.auth === 'agent' ? 'SSH agent' : 'public key'}</span>
                    <Button size="sm" variant="ghost" iconOnly icon="trash" aria-label="Remove the jump server" onClick={(e) => (e.preventDefault(), removeJump(i))} />
                  </summary>
                  {hopForm(i, `Jump server ${i + 1}`)}
                </details>
              ))}
              <Button size="sm" variant="ghost" icon="plus" onClick={addJump}>
                Add jump server
              </Button>
            </Fieldset>
          </div>
          <div className="db-ssh-col">
            <Fieldset legend="Advanced">
              <Field label="Remote host" hint="from Main unless set">
                <input className="control db-input mono" value={r.remoteHost ?? ''} placeholder={c.host} onChange={(e) => setReach({ remoteHost: e.target.value })} />
              </Field>
              <Field label="Remote port">
                <input className="control db-input mono" inputMode="numeric" value={r.remotePort || ''} placeholder={String(c.port)} onChange={(e) => setReach({ remotePort: Number(e.target.value) || 0 })} />
              </Field>
              <Field label="Local port" hint="127.0.0.1 on This PC">
                <input className="control db-input mono" inputMode="numeric" value={r.localPort || ''} placeholder={String(10000 + c.port)} onChange={(e) => setReach({ localPort: Number(e.target.value) || 0 })} />
              </Field>
              <Check on={!!r.openLocal} set={(v) => setReach({ openLocal: v, localPort: r.localPort || 10000 + c.port })}>
                Open the local port for agents and tools
              </Check>
              <p className="db-hint">php artisan, prisma or a desktop client on This PC reach the database there while the tunnel is up.</p>
              <Field label="Keep-alive">
                <input className="control db-input mono" inputMode="numeric" value={r.keepAliveSec ?? 30} onChange={(e) => setReach({ keepAliveSec: Number(e.target.value) || 0 })} />
              </Field>
              <Field label="Timeout">
                <input className="control db-input mono" inputMode="numeric" value={r.timeoutSec ?? 15} onChange={(e) => setReach({ timeoutSec: Number(e.target.value) || 0 })} />
              </Field>
              <Check on={r.reconnect !== false} set={(v) => setReach({ reconnect: v })}>
                Reconnect when it drops
              </Check>
            </Fieldset>
          </div>
        </div>
      )}
      {on && (
        <div className="db-tunnel-test">
          <Button icon="link" loading={testing} onClick={onTest}>
            Test tunnel configuration
          </Button>
          {test?.hops?.map((h, i) => (
            <span key={i} className={h.error ? 'db-test-err' : 'db-test-ok'}>
              <Icon name={h.error ? 'alertCircle' : 'check'} /> {h.host} {h.error ? '' : `${h.ms} ms`}
            </span>
          ))}
          {test && test.ok && (
            <span className="db-test-ok">
              <Icon name="check" /> {r.remoteHost || c.host}:{r.remotePort || c.port} open
            </span>
          )}
        </div>
      )}
      {on && <p className="db-hint"><Icon name="shield" /> Host keys are checked against This PC's known_hosts; a new one shows its fingerprint and asks before it is trusted. Your browser never connects to the database: the hub does.</p>}
    </div>
  );
}

function SSLPage({ f, setF }: { f: Form; setF: (f: Form) => void }) {
  const opts: { v: Connection['tls']; label: string; hint: string }[] = [
    { v: 'off', label: 'Off', hint: 'plain TCP (inside an SSH tunnel it is encrypted by the tunnel)' },
    { v: 'preferred', label: 'Preferred', hint: 'TLS when the server offers it' },
    { v: 'required', label: 'Required', hint: 'TLS, without checking the certificate' },
    { v: 'verify-ca', label: 'Verify CA', hint: 'TLS, and the certificate must be valid for the host' },
  ];
  return (
    <div className="db-page">
      <Fieldset legend="SSL / TLS">
        {opts.map((o) => (
          <label key={o.v} className="db-radio-row">
            <input type="radio" checked={f.c.tls === o.v} onChange={() => setF({ ...f, c: { ...f.c, tls: o.v } })} />
            <b>{o.label}</b>
            <span className="text-3 fs-sm">{o.hint}</span>
          </label>
        ))}
      </Fieldset>
    </div>
  );
}

function PropsPage({ f, setF }: { f: Form; setF: (f: Form) => void }) {
  const props = Object.entries(f.c.props ?? {});
  const known = f.c.driver === 'postgres' ? ['search_path', 'timezone', 'statement_timeout', 'client_encoding', 'lock_timeout'] : isKV(f.c.driver) ? [] : ['charset', 'collation', 'time_zone', 'sql_mode', 'max_execution_time'];
  const set = (list: [string, string][]) => setF({ ...f, c: { ...f.c, props: Object.fromEntries(list.filter(([k]) => k)) } });
  return (
    <div className="db-page">
      <Fieldset legend="Driver properties">
        {known.length === 0 && <p className="db-hint">This driver takes no properties.</p>}
        {props.map(([k, v], i) => (
          <div key={i} className="db-frow2">
            <select className="control db-input mono" value={k} onChange={(e) => set(props.map((p, j) => (j === i ? [e.target.value, p[1]] : p)))}>
              {[k, ...known.filter((x) => x !== k)].map((x) => (
                <option key={x}>{x}</option>
              ))}
            </select>
            <span className="db-inline">
              <input className="control db-input mono" value={v} onChange={(e) => set(props.map((p, j) => (j === i ? [p[0], e.target.value] : p)))} />
              <Button size="sm" variant="ghost" iconOnly icon="trash" aria-label="Remove" onClick={() => set(props.filter((_, j) => j !== i))} />
            </span>
          </div>
        ))}
        {known.length > 0 && (
          <Button size="sm" variant="ghost" icon="plus" onClick={() => set([...props, [known.find((k) => !(f.c.props ?? {})[k]) ?? known[0], '']])}>
            Add a property
          </Button>
        )}
      </Fieldset>
    </div>
  );
}

const ACCESS: { v: AgentAccess; label: string; hint: string }[] = [
  { v: 'none', label: 'No access', hint: 'They do not see it.' },
  { v: 'read', label: 'Read', hint: 'Schema and SELECTs.' },
  { v: 'ask', label: 'Read, ask to write', hint: 'A write waits for your approval.' },
  { v: 'write', label: 'Read and write', hint: 'Writes run on their own; DROP, TRUNCATE and no-WHERE still ask you.' },
];

function DetailsPages({ f, setF, only, wizard }: { f: Form; setF: (f: Form) => void; only?: 'general' | 'security' | 'agents'; wizard?: boolean }) {
  const c = f.c;
  const set = (patch: Partial<Connection>) => setF({ ...f, c: { ...c, ...patch } });
  const setEnv = (env: Env) => set({ env, security: wizard ? defaultSecurity(env) : c.security });
  const [mask, setMask] = useState('');
  const general = (
    <>
      <Fieldset legend="General">
        <Field label="Connection name">
          <input className="control db-input" value={c.name} placeholder={c.database || driverOf(c.driver).label} onChange={(e) => set({ name: e.target.value })} />
        </Field>
        <Field label="Description">
          <input className="control db-input" value={c.description ?? ''} onChange={(e) => set({ description: e.target.value })} />
        </Field>
        <Field label="Group">
          <input className="control db-input" value={c.group ?? ''} placeholder="no group" onChange={(e) => set({ group: e.target.value })} />
        </Field>
      </Fieldset>
      <Fieldset legend="Connection type">
        <div className="db-types" role="radiogroup" aria-label="Connection type">
          {ENVS.map((e) => (
            <button key={e.id} type="button" role="radio" aria-checked={c.env === e.id} className={`db-type ${c.env === e.id ? 'is-on' : ''} db-type-${e.id}`} onClick={() => setEnv(e.id)}>
              <span className={`db-swatch db-swatch-${e.id}`} />
              {e.label}
              {c.env === e.id && <span className="text-3 fs-xs db-grow-r">its colour on every tab and card</span>}
            </button>
          ))}
        </div>
        <p className="db-hint">{wizard ? 'A type sets the defaults of Security; you can still change them for this connection.' : 'Changing the type keeps the Security you set.'}</p>
      </Fieldset>
    </>
  );
  const security = (
    <>
      <Fieldset legend="Security">
        <Check on={c.security.readOnly} set={(v) => set({ security: { ...c.security, readOnly: v } })}>
          <b>Read-only connection</b>
        </Check>
        <p className="db-hint">On by itself for Production. Untick for full access.</p>
        <Check on={c.security.confirmWrites} set={(v) => set({ security: { ...c.security, confirmWrites: v } })}>
          Confirm SQL that writes
        </Check>
        <Check on={c.security.confirmEdits} set={(v) => set({ security: { ...c.security, confirmEdits: v } })}>
          Confirm data changes in the grid
        </Check>
        <div className="db-banner db-banner-danger fs-xs">DROP, TRUNCATE and DELETE or UPDATE without WHERE always ask, with the connection's name typed, whatever is ticked here.</div>
      </Fieldset>
      <Fieldset legend="Limits">
        <Field label="Stop a query after" hint="seconds">
          <input className="control db-input mono" inputMode="numeric" value={c.limits.timeoutSec} onChange={(e) => set({ limits: { ...c.limits, timeoutSec: Number(e.target.value) || 0 } })} />
        </Field>
        <Field label="Rows per page">
          <input className="control db-input mono" inputMode="numeric" value={c.limits.pageSize} onChange={(e) => set({ limits: { ...c.limits, pageSize: Number(e.target.value) || 0 } })} />
        </Field>
      </Fieldset>
    </>
  );
  const agents = (
    <>
      <Fieldset legend={<><Icon name="sparkle" /> Agent access</>}>
        <div className="db-access" role="radiogroup" aria-label="Agent access">
          {ACCESS.map((a) => (
            <button key={a.v} type="button" role="radio" aria-checked={c.agents.access === a.v} className={`db-opt ${c.agents.access === a.v ? 'is-on' : ''}`} onClick={() => set({ agents: { ...c.agents, access: a.v } })}>
              <b>{a.label}</b>
              <small>{a.hint}</small>
            </button>
          ))}
        </div>
        <p className="db-hint">Never more than the connection allows: while Security › Read-only is on, agents read only.</p>
      </Fieldset>
      <div className="db-frow2 db-frow2-top">
        <Fieldset legend="Which agents">
          <select className="control db-input" value={c.agents.scope} onChange={(e) => set({ agents: { ...c.agents, scope: e.target.value as 'project' | 'wired' } })}>
            <option value="project">Every agent of this project</option>
            <option value="wired">Only consoles wired to its card</option>
          </select>
        </Fieldset>
        <Fieldset legend="Masked columns">
          <div className="db-masks">
            {c.agents.masked.map((m) => (
              <span key={m} className="db-fchip mono">
                {m}
                <button type="button" aria-label={`Unmask ${m}`} onClick={() => set({ agents: { ...c.agents, masked: c.agents.masked.filter((x) => x !== m) } })}>
                  <Icon name="close" />
                </button>
              </span>
            ))}
            <form onSubmit={(e) => (e.preventDefault(), mask.trim() && (set({ agents: { ...c.agents, masked: [...c.agents.masked, mask.trim()] } }), setMask('')))}>
              <input className="db-mask-in mono" value={mask} onChange={(e) => setMask(e.target.value)} placeholder="users.email" aria-label="A column to mask" />
            </form>
          </div>
          <p className="db-hint">Shown to agents as •••: never sent to their model.</p>
        </Fieldset>
      </div>
      <Fieldset legend="SmartDB">
        <Check on={c.agents.askBar !== false} set={(v) => set({ agents: { ...c.agents, askBar: v } })}>
          Turn words into filters in the Ask bar
        </Check>
        <Check on={c.agents.editorAi !== false} set={(v) => set({ agents: { ...c.agents, editorAi: v } })}>
          Offer Write, Explain, Fix and Optimise in the SQL editor
        </Check>
        <p className="db-hint">SmartDB runs your default agent CLI on This PC (Claude Code or Codex), with your own login. The schema, the SQL and, for the assistant, what its queries return go to its model.</p>
      </Fieldset>
    </>
  );
  if (only === 'general') return <div className="db-page">{general}</div>;
  if (only === 'security') return <div className="db-page">{security}</div>;
  if (only === 'agents') return <div className="db-page">{agents}</div>;
  return (
    <div className="db-page db-details">
      <div>{general}</div>
      <div>
        {security}
        {agents}
      </div>
    </div>
  );
}

function ActivityPage({ id }: { id: string }) {
  const [items, setItems] = useState<Activity[] | null>(null);
  useEffect(() => {
    void dbApi
      .activity(id, 200)
      .then((r) => setItems(r.items))
      .catch(() => setItems([]));
  }, [id]);
  if (!items) return <div className="db-empty text-3">Loading…</div>;
  if (items.length === 0) return <div className="db-empty text-3">No agent has used this connection yet.</div>;
  return (
    <div className="db-page">
      <table className="db-plain">
        <thead>
          <tr>
            <th>Who</th>
            <th>Statement</th>
            <th>Rows</th>
            <th>What</th>
            <th>At</th>
          </tr>
        </thead>
        <tbody>
          {items.map((a, i) => (
            <tr key={i}>
              <td>
                <b>{a.who}</b>
              </td>
              <td className="mono db-act-sql" title={a.sql}>
                {a.sql}
              </td>
              <td className="tabular">{a.rows || ''}</td>
              <td className={`db-act db-act-${a.what}`} title={a.error || a.note}>
                {a.what}
              </td>
              <td className="text-3 tabular">{new Date(a.at).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
