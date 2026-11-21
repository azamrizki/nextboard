// Settings › NextCode (docs/API-agents.md §30): NextCode runs on one of three engines, Claude Code
// ("nextcode"), Codex ("nextcode-codex") or its own engine on an API provider ("nextcode-api").
// Claude and Codex have their own login profiles (never those of the CLI they run on); the API
// engine has no login, and a default API provider (settings.nextcode.provider) instead. Each engine
// has its own default model and effort (agents.models.<agent>). The permission mode a new console
// starts in and how long its CLI may sit idle are shared (settings.nextcode).

import { useEffect, useState } from 'react';
import { errorMessage } from '../api';
import { agentProfiles, useAgentCatalog } from '../agents/catalog';
import { ModelPicker } from '../agents/ModelPicker';
import { NO_CHOICE } from '../agents/models';
import { defaultProfileOf, patchSettings, useEffectiveSettings } from '../agents/settings';
import { Button } from '../components/Button';
import { SegmentedControl } from '../components/form';
import { Icon } from '../components/Icon';
import { SettingRow } from '../components/Switch';
import { toast } from '../components/Toast';
import { Link } from '../lib/router';
import type { Agent, NextCodePermissionMode, Settings } from '../lib/types';
import { ENGINES, PROVIDERS_PATH, agentOfEngine, apiProviderChoice, type Engine } from '../nextcode/engines';
import { compatible, providersStore, useProviders } from '../providers/providers';
import { useMachines, useProfiles, useSettings } from '../store';
import { patchOf } from './ModelDefaults';
import { API_AUTO, idleMinutes, IDLE_LIMITS, nextcodeIntro, nextcodeOf, nextcodeProvider, PERMISSION_MODES } from './nextcodeSettings';
import { ProfileDialog, ProfileList } from './ProfilesPane';

async function save(patch: Record<string, unknown>, what: string) {
  try {
    await patchSettings(patch);
  } catch (err) {
    toast.error(`Could not save ${what}: ${errorMessage(err)}`);
  }
}

/**
 * What each engine says about its login: the account it signs in to, the CLI it is kept apart
 * from, and the providers that can run it. The API engine has neither CLI nor login.
 */
const ENGINE_TEXT: Record<Engine, { cli: string; account: string; home: string; provider: string }> = {
  claude: { cli: 'Claude Code', account: 'a Claude account', home: '~/.claude', provider: 'Anthropic-compatible' },
  codex: { cli: 'Codex', account: 'a ChatGPT or OpenAI account', home: '~/.codex', provider: 'OpenAI Responses' },
  api: { cli: '', account: '', home: '', provider: 'Anthropic- or OpenAI-compatible' },
};

export function NextCodePane() {
  const settings = useEffectiveSettings();
  const catalog = useAgentCatalog();
  const machines = useMachines();
  const nc = nextcodeOf(settings);
  const engines = ENGINES.flatMap((e) => {
    const agent = agentOfEngine(catalog.agents, e.value);
    return agent ? [{ ...e, agent }] : [];
  });
  const [adding, setAdding] = useState<Agent | null>(null);
  const allOff = engines.length > 0 && engines.every((e) => settings.agents.disabled?.[e.agent.id]);
  // the login sections are Claude's and Codex's; the API engine has a section of its own
  const logins = engines.filter((e) => e.value !== 'api');
  const apiEngine = engines.find((e) => e.value === 'api');

  if (catalog.ready && !engines.length)
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
      <p className="settings-intro">{nextcodeIntro(engines.map((e) => e.value))}</p>
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
      {logins.map((e) => (
        <EngineCard key={e.value} engine={e.value} label={e.label} agent={e.agent} settings={settings} single={engines.length === 1} off={!allOff && !!settings.agents.disabled?.[e.agent.id]} onAdd={() => setAdding(e.agent)} />
      ))}
      {apiEngine && <ApiEngineCard agent={apiEngine.agent} settings={settings} off={!allOff && !!settings.agents.disabled?.[apiEngine.agent.id]} />}

      <section className="card settings-card" aria-labelledby="nc-defaults">
        <h3 id="nc-defaults" className="section-title">
          Defaults
        </h3>
        <p className="setting-hint">What a new NextCode console starts with{engines.length > 2 ? ', on any engine' : engines.length > 1 ? ', on either engine' : ''}. A console keeps its own when it restarts.</p>
        <SettingRow title="Permission mode" hint="How a new console asks before it acts. Each chat can change its own mode.">
          <SegmentedControl<NextCodePermissionMode>
            label="Permission mode of new NextCode consoles"
            size="sm"
            className="nextcode-modes"
            value={nc.permissionMode}
            onChange={(v) => void save({ nextcode: { permissionMode: v } }, 'the permission mode')}
            options={PERMISSION_MODES}
          />
        </SettingRow>
        {nc.permissionMode === 'auto' && apiEngine && <p className="setting-hint">{API_AUTO}</p>}
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

      {adding && <ProfileDialog machines={machines} agents={[adding]} onClose={() => setAdding(null)} />}
    </div>
  );
}

interface EngineCardProps {
  engine: Engine;
  label: string;
  agent: Agent;
  settings: Settings;
  /** The hub has this engine only: the card is NextCode's login and model. */
  single: boolean;
  /** Turned off in Settings › Agents (the other engine is not). */
  off: boolean;
  onAdd: () => void;
}

/** One engine: its logins and the model and effort a new console on it starts with. */
function EngineCard({ engine, label, agent, settings, single, off, onAdd }: EngineCardProps) {
  const profiles = agentProfiles(useProfiles(), 'local', agent);
  const providers = useProviders().filter((p) => compatible(agent, p));
  const loaded = !!useSettings();
  const defaultId = defaultProfileOf(settings, 'local', agent.id);
  const text = ENGINE_TEXT[engine];
  const id = `nc-engine-${engine}`;
  const who = single ? 'NextCode' : `NextCode on ${text.cli}`;
  return (
    <section className="card settings-card" aria-labelledby={id}>
      <div className="settings-card-head">
        <h3 id={id} className="section-title">
          {single ? 'Login' : `${label} engine`}
        </h3>
        <Button size="sm" variant="primary" icon="plus" onClick={onAdd}>
          Add login
        </Button>
      </div>
      {off && (
        <div className="alert warning" role="status">
          <Icon name="info" />
          <span>
            The {label} engine is turned off in <Link to="/settings/agents">Settings › Agents</Link>, so no list offers it. What you set here stays.
          </span>
        </div>
      )}
      {!single && agent.available === false && (
        <p className="setting-hint">{text.cli} is not installed on This PC, so this engine cannot start yet.</p>
      )}
      <p className="setting-hint">
        {who} signs in to {text.account} of its own, kept in NextBoard’s data folder. {text.cli}’s logins are never used for it, and its logins are not offered to {text.cli}. Sign
        in opens {text.cli}’s own login in a terminal, with NextCode’s folder.
      </p>
      {loaded && profiles.length > 0 && !profiles.some((p) => p.id === defaultId) && (
        <div className="alert warning" role="note">
          <Icon name="alert" />
          <span>
            {who} has no default login, so new consoles on it use your own {text.cli} login ({text.home}) and its memory. Choose Use by default on one below to keep it apart.
          </span>
        </div>
      )}
      {profiles.length === 0 ? (
        <div className="list-msg">No login for {single ? 'NextCode' : `the ${label} engine`} yet. Add one, then sign in to {text.account}.</div>
      ) : (
        <ProfileList profiles={profiles} />
      )}
      <p className="setting-hint">
        {providers.length > 0
          ? `An API provider can run a console instead of a login: pick it in the Login profile list when you start one. ${providers.length === 1 ? 'One of yours can' : `${providers.length} of yours can`} (${text.provider}). `
          : `An API provider with an ${text.provider} endpoint can run a console instead of a login, picked per console when you start one. `}
        <Link to="/settings/providers">API providers</Link>
      </p>
      <SettingRow title="Model and effort" hint={`What a new console on ${single ? 'NextCode' : `the ${label} engine`} starts with, apart from ${text.cli}’s own default. Spawn forms show it first.`}>
        {agent.models ? (
          <ModelPicker agent={agent} value={settings.agents.models?.[agent.id] ?? NO_CHOICE} onChange={(c) => void save({ agents: { models: { [agent.id]: patchOf(c) } } }, 'the default model')} />
        ) : (
          <span className="fs-xs text-3">This engine cannot be told a model.</span>
        )}
      </SettingRow>
    </section>
  );
}

/**
 * NextCode's own engine: no CLI and no login, it talks to an API provider. Its default provider
 * (settings.nextcode.provider) is what a new console runs on unless another is picked, and its
 * model list is that provider's.
 */
function ApiEngineCard({ agent, settings, off }: { agent: Agent; settings: Settings; off: boolean }) {
  const providers = useProviders();
  const loaded = providersStore.isLoaded();
  const saved = nextcodeProvider(settings);
  const known = !saved || providers.some((p) => p.id === saved);
  // what a console starts on when none is picked: the default, else the first (as the start forms offer it)
  const startsOn = apiProviderChoice('', providers, saved);
  const id = 'nc-engine-api';
  return (
    <section className="card settings-card" aria-labelledby={id}>
      <div className="settings-card-head">
        <h3 id={id} className="section-title">
          API engine
        </h3>
      </div>
      {off && (
        <div className="alert warning" role="status">
          <Icon name="info" />
          <span>
            The API engine is turned off in <Link to="/settings/agents">Settings › Agents</Link>, so no list offers it. What you set here stays.
          </span>
        </div>
      )}
      <p className="setting-hint">
        NextCode’s own engine: no Claude Code or Codex underneath, it talks to an API provider directly, with the same chat, tools and permission modes. Any provider works, Anthropic- or
        OpenAI-compatible.
      </p>
      <p className="setting-hint">It needs no login: the provider’s key is all it uses. It keeps its conversations in NextBoard’s data folder, and runs on This PC.</p>
      {loaded && providers.length === 0 && (
        <div className="alert warning" role="note">
          <Icon name="alert" />
          <span>
            There is no API provider yet, so this engine cannot start. Add one in <Link to={PROVIDERS_PATH}>Settings › API providers</Link>.
          </span>
        </div>
      )}
      <SettingRow
        title="Default API provider"
        htmlFor="nc-api-provider"
        hint={saved ? 'What a new console on the API engine runs on, unless you pick another when you start it.' : 'None: the start forms offer the first provider. Pick one to have new consoles start on it.'}
      >
        <select id="nc-api-provider" className="control control-sm" value={saved} onChange={(e) => void save({ nextcode: { provider: e.target.value } }, 'the default API provider')}>
          <option value="">None</option>
          {providers.map((p) => (
            <option key={p.id} value={p.id}>
              {p.name}
            </option>
          ))}
          {!known && <option value={saved}>A removed provider</option>}
        </select>
      </SettingRow>
      <p className="setting-hint">
        Add a provider, or change its key and models, in <Link to={PROVIDERS_PATH}>Settings › API providers</Link>.
      </p>
      <SettingRow title="Model and effort" hint="What a new console on the API engine starts with. The models are the provider’s; Default is the provider’s own model.">
        {startsOn ? (
          <ModelPicker agent={agent} profileId={startsOn} value={settings.agents.models?.[agent.id] ?? NO_CHOICE} onChange={(c) => void save({ agents: { models: { [agent.id]: patchOf(c) } } }, 'the default model')} />
        ) : (
          <span className="fs-xs text-3">The models come from the API provider.</span>
        )}
      </SettingRow>
    </section>
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
