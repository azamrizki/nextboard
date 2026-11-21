// Settings › NextCode (docs/API-agents.md §30, One NextCode): NextCode is one agent, and what runs
// it is its own business. Here the owner adds the accounts it may run on, in the order its chat's
// model menu lists them (settings.nextcode.accounts): its own Claude login (Claude Code underneath),
// its own ChatGPT login (Codex underneath) or an API provider (its own engine). A new console starts
// on one account's model (nextcode.account, .model, .effort); its chat picks a model of any of them.
// The permission mode a new console starts in and how long its CLI may sit idle are shared.

import { lazy, Suspense, useEffect, useRef, useState } from 'react';
import { errorMessage } from '../api';
import { useAgentCatalog } from '../agents/catalog';
import { useModelList } from '../agents/ModelPicker';
import { modelLabel } from '../agents/models';
import { patchSettings, useEffectiveSettings } from '../agents/settings';
import { Button } from '../components/Button';
import { SegmentedControl } from '../components/form';
import { Icon } from '../components/Icon';
import { Menu, type MenuEntry } from '../components/Menu';
import { SettingRow } from '../components/Switch';
import { toast } from '../components/Toast';
import { Link, navigate } from '../lib/router';
import type { Agent, NextCodePermissionMode, Profile } from '../lib/types';
import { accountIds, addableLogins, ncAccounts, type NcAccount } from '../nextcode/accounts';
import { NEXTCODE_AGENTS, NEXTCODE_EFFORTS, PROVIDERS_PATH } from '../nextcode/engines';
import { useAccountSources } from '../nextcode/ModelMenu';
import { providersStore } from '../providers/providers';
import { useMachines } from '../store';
import { API_AUTO, idleMinutes, IDLE_LIMITS, nextcodeIntro, nextcodeOf, PERMISSION_MODES } from './nextcodeSettings';
import { McpCard, MemoryCard, SkillsCard } from './NextCodeKit';
import { ProfileDialog } from './ProfilesPane';

const ProfileSignInDialog = lazy(() => import('./ProfileSignIn').then((m) => ({ default: m.ProfileSignInDialog })));

async function save(patch: Record<string, unknown>, what: string) {
  try {
    await patchSettings(patch);
  } catch (err) {
    toast.error(`Could not save ${what}: ${errorMessage(err)}`);
  }
}

export function NextCodePane() {
  const settings = useEffectiveSettings();
  const catalog = useAgentCatalog();
  const nc = nextcodeOf(settings);
  const builtins = catalog.agents.filter((a) => Object.values(NEXTCODE_AGENTS).includes(a.id));
  const allOff = builtins.length > 0 && builtins.every((a) => settings.agents.disabled?.[a.id]);

  if (catalog.ready && !builtins.length)
    return (
      <div className="settings-cards">
        <div className="alert warning" role="status">
          <Icon name="alert" />
          <span>This hub has no NextCode yet. Update NextBoard on the hub, then reload this page.</span>
        </div>
      </div>
    );

  return (
    <div className="settings-cards">
      <p className="settings-intro">{nextcodeIntro()}</p>
      {allOff && (
        <div className="alert warning" role="status">
          <Icon name="info" />
          <span>
            NextCode is turned off in <Link to="/settings/agents">Settings › Agents</Link>, so no list offers it. What you set here stays.
          </span>
        </div>
      )}
      {!catalog.ready && (
        <div className="list-msg" role="status">
          <span className="spinner" aria-hidden="true" /> Loading…
        </div>
      )}
      <AccountsCard agents={builtins} />
      <SkillsCard />
      <MemoryCard />
      <McpCard />

      <section className="card settings-card" aria-labelledby="nc-defaults">
        <h3 id="nc-defaults" className="section-title">
          New consoles
        </h3>
        <p className="setting-hint">What a new NextCode console starts with. Each chat picks its own model and mode after that, and keeps them.</p>
        <StartModel />
        <SettingRow title="Permission mode" hint="How a new console asks before it acts, on every account. Each chat can change its own mode.">
          <SegmentedControl<NextCodePermissionMode>
            label="Permission mode of new NextCode consoles"
            size="sm"
            className="nextcode-modes"
            value={nc.permissionMode}
            onChange={(v) => void save({ nextcode: { permissionMode: v } }, 'the permission mode')}
            options={PERMISSION_MODES}
          />
        </SettingRow>
        {nc.permissionMode === 'auto' && <p className="setting-hint">{API_AUTO}</p>}
        {nc.permissionMode === 'bypassPermissions' && (
          <div className="alert warning" role="note">
            <Icon name="alert" />
            <span>Bypass: new NextCode consoles edit files and run any command without asking, with your user’s rights on This PC. Use it only for work you can undo.</span>
          </div>
        )}
        <SettingRow
          title="Idle time"
          htmlFor="nc-idle"
          hint="The CLI stops after this long with nothing to do; your next message starts it again on the same conversation."
        >
          <MinutesInput id="nc-idle" value={nc.idleExitMin} onCommit={(n) => void save({ nextcode: { idleExitMin: n } }, 'the idle time')} />
        </SettingRow>
      </section>
    </div>
  );
}

/** What each kind of login is, in the Add menu. */
const ADD_LOGIN: { engine: 'claude' | 'codex'; label: string; cli: string }[] = [
  { engine: 'claude', label: 'Claude account', cli: 'Claude Code' },
  { engine: 'codex', label: 'ChatGPT account', cli: 'Codex' },
];

/** The accounts NextCode runs on, in its menu's order: add, sign in, move up, remove. */
function AccountsCard({ agents }: { agents: Agent[] }) {
  const src = useAccountSources();
  const machines = useMachines();
  const accounts = ncAccounts(src);
  const ids = accountIds(src);
  const chosen = !!src.settings.nextcode?.accounts;
  const [adding, setAdding] = useState<Agent | null>(null);
  const [signing, setSigning] = useState<{ profile: Profile; cli: string } | null>(null);
  // the logins there were when a new one was being added: the new one joins the list
  const before = useRef<Set<string> | null>(null);

  const put = (next: string[], what: string) => void save({ nextcode: { accounts: next } }, what);
  const add = (id: string) => !ids.includes(id) && put([...ids, id], 'the accounts');
  const remove = (a: NcAccount) => put(ids.filter((x) => x !== a.id), 'the accounts');
  const up = (i: number) => i > 0 && put([...ids.slice(0, i - 1), ids[i], ids[i - 1], ...ids.slice(i + 1)], 'the order');

  useEffect(() => {
    if (adding || !before.current) return;
    const fresh = src.profiles.filter((p) => !before.current!.has(p.id) && Object.values(NEXTCODE_AGENTS).includes(p.agent) && !ids.includes(p.id));
    before.current = null;
    if (fresh.length) put([...ids, ...fresh.map((p) => p.id)], 'the accounts');
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [adding, src.profiles]);

  const providers = src.providers.filter((p) => !ids.includes(p.id));
  const menu: MenuEntry[] = [
    ...ADD_LOGIN.flatMap(({ engine, label, cli }): MenuEntry[] => {
      const agent = agents.find((a) => a.id === NEXTCODE_AGENTS[engine]);
      if (!agent) return [];
      const existing = addableLogins(engine, src);
      return [
        { type: 'heading', label },
        ...existing.map((p): MenuEntry => ({ label: p.home ? `NextCode’s own ${engine === 'claude' ? 'Claude' : 'ChatGPT'} login` : p.name, icon: 'user', onSelect: () => add(p.id) })),
        {
          label: `New ${engine === 'claude' ? 'Claude' : 'ChatGPT'} login…`,
          icon: 'plus',
          disabled: agent.available === false,
          title: agent.available === false ? `${cli} is not installed on This PC` : `Signs in through ${cli}’s own login`,
          onSelect: () => {
            before.current = new Set(src.profiles.map((p) => p.id));
            setAdding(agent);
          },
        },
      ];
    }),
    { type: 'heading', label: 'API provider' },
    ...providers.map((p): MenuEntry => ({ label: p.name, icon: 'key', onSelect: () => add(p.id) })),
    ...(providersStore.isLoaded() && !src.providers.length ? [{ label: 'Add one in Settings › API providers', icon: 'external', onSelect: () => navigate(PROVIDERS_PATH) } as MenuEntry] : []),
  ];

  return (
    <section className="card settings-card" aria-labelledby="nc-accounts">
      <div className="settings-card-head">
        <h3 id="nc-accounts" className="section-title">
          Accounts
        </h3>
        <Menu label="Add an account" icon="plus" triggerLabel="Add account" items={menu} />
      </div>
      <p className="setting-hint">
        What NextCode may run on. Its chat lists the models of each one, and picking a model of another account moves the chat there. Logins are NextCode’s own, kept in NextBoard’s data folder,
        never Claude Code’s or Codex’s; signing in runs their own login, and NextBoard never sees the token.
      </p>
      {!chosen && accounts.length > 0 && <p className="setting-hint">These are NextCode’s defaults until you change the list.</p>}
      {accounts.length === 0 ? (
        <div className="list-msg">No account yet. Add your Claude or ChatGPT login, or an API provider.</div>
      ) : (
        <ul className="nc-accounts" aria-label="NextCode’s accounts">
          {accounts.map((a, i) => {
            const profile = a.kind === 'login' ? src.profiles.find((p) => p.id === a.id) : undefined;
            const cli = a.engine === 'codex' ? 'Codex' : 'Claude Code';
            return (
              <li key={a.id} className="nc-account">
                <Icon name={a.kind === 'login' ? 'user' : 'key'} />
                <span className="nc-account-text">
                  <span className="nc-account-name">{a.name}</span>
                  <span className="nc-account-detail">{a.missing ? (a.kind === 'login' ? 'Its folder is gone' : a.detail) : a.detail}</span>
                </span>
                <span className="nc-account-actions">
                  {profile && (
                    <Button size="sm" variant="secondary" onClick={() => setSigning({ profile, cli })}>
                      Sign in
                    </Button>
                  )}
                  {i > 0 && <Button size="sm" variant="ghost" iconOnly icon="arrowUp" aria-label={`Move ${a.name} up`} onClick={() => up(i)} />}
                  <Button size="sm" variant="danger-ghost" iconOnly icon="close" aria-label={`Remove ${a.name} from NextCode`} title="Remove from NextCode (the login or provider stays)" onClick={() => remove(a)} />
                </span>
              </li>
            );
          })}
        </ul>
      )}
      <p className="setting-hint">
        Rename or delete logins in <Link to="/settings/profiles">Settings › Login profiles</Link>; add a provider, or change its key and models, in{' '}
        <Link to={PROVIDERS_PATH}>Settings › API providers</Link>.
      </p>
      {adding && <ProfileDialog machines={machines} agents={[adding]} onClose={() => setAdding(null)} />}
      {signing && (
        <Suspense fallback={null}>
          <ProfileSignInDialog profile={signing.profile} agentName="NextCode" cliName={signing.cli} onClose={() => setSigning(null)} />
        </Suspense>
      )}
    </section>
  );
}

const SEP = '\u0001';

/** The model a new console starts on: one of an account's, in a list grouped by account; then its effort. */
function StartModel() {
  const src = useAccountSources();
  const accounts = ncAccounts(src);
  const nc = src.settings.nextcode;
  const account = nc?.account ?? '';
  const model = nc?.model ?? '';
  const value = account ? `${account}${SEP}${model}` : '';
  const known = !account || accounts.some((a) => a.id === account);
  const onPick = (v: string) => {
    if (!v) return void save({ nextcode: { account: null, model: null, effort: null } }, 'the model of new consoles');
    const [acc, m] = v.split(SEP);
    void save({ nextcode: { account: acc, model: m || null } }, 'the model of new consoles');
  };
  return (
    <>
      <SettingRow title="Model" htmlFor="nc-start-model" hint="A new console starts on this model, on its account. The first is NextCode’s Claude login, with its own default model.">
        <select id="nc-start-model" className="control control-sm" value={value} onChange={(e) => onPick(e.target.value)}>
          <option value="">NextCode’s default</option>
          {accounts.map((a) => (
            <AccountModels key={a.id} account={a} />
          ))}
          {!known && <option value={value}>An account removed from NextCode</option>}
        </select>
      </SettingRow>
      <StartEffort effort={nc?.effort ?? ''} />
    </>
  );
}

/** One account's models as an option group of the Model list. */
function AccountModels({ account }: { account: NcAccount }) {
  const catalog = useAgentCatalog();
  const agent = catalog.agents.find((a) => a.id === account.agentId);
  const { list, onProvider } = useModelList(agent, account.id);
  const models = list?.models ?? [];
  const cur = list?.current ?? '';
  return (
    <optgroup label={account.name}>
      <option value={`${account.id}${SEP}`}>
        {account.name} · Default{cur ? ` (${modelLabel(cur, models)})` : onProvider ? '' : ''}
      </option>
      {models.map((m) => (
        <option key={m.id} value={`${account.id}${SEP}${m.id}`}>
          {account.name} · {m.name}
        </option>
      ))}
    </optgroup>
  );
}

/**
 * NextCode's effort for new consoles: its own scale, the same on every account and model (the hub
 * fits it to what a model takes). Each chat changes its own in the model menu or with /effort.
 */
function StartEffort({ effort }: { effort: string }) {
  const options = [{ value: '', label: 'Default', title: 'Each model’s own effort' }, ...NEXTCODE_EFFORTS.map((e) => ({ value: e, label: e, title: `${e} on every model: one without it runs at its nearest` }))];
  return (
    <SettingRow title="Effort" hint="How hard NextCode thinks, on every account and model; a model that has no such level runs at its nearest. Each chat changes its own in the model menu, or with /effort.">
      <SegmentedControl<string> label="Effort of new NextCode consoles" size="sm" value={effort} onChange={(v) => void save({ nextcode: { effort: v || null } }, 'the effort of new consoles')} options={options} />
    </SettingRow>
  );
}

/** Whole minutes, saved when the field is left or Enter is pressed; out of range is clamped. */
function MinutesInput({ id, value, onCommit }: { id: string; value: number; onCommit: (n: number) => void }) {
  const [draft, setDraft] = useState(String(value));
  useEffect(() => setDraft(String(value)), [value]);
  const commit = () => {
    const n = idleMinutes(draft);
    if (n === null) return setDraft(String(value));
    if (n !== value) onCommit(n);
    setDraft(String(n));
  };
  return (
    <div className="input-group has-addon min-sec">
      <input
        id={id}
        className="control control-sm tabular"
        type="number"
        min={IDLE_LIMITS.min}
        max={IDLE_LIMITS.max}
        inputMode="numeric"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => e.key === 'Enter' && commit()}
      />
      <span className="ig-addon ig-text">minutes</span>
    </div>
  );
}
