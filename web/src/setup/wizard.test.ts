import { describe, expect, it } from 'vitest';
import {
  anythingChosen,
  beyondLoopback,
  copiedSummary,
  DEFAULT_COPY,
  importBody,
  markAddresses,
  nextStep,
  portOf,
  preselectSource,
  secretsChosen,
  serviceText,
  setChoice,
  signInPatch,
  wizardSteps,
} from './wizard';

describe('wizardSteps', () => {
  it('keeps the first run of a lone default instance as it was: admin, then agents', () => {
    expect(wizardSteps({ isDefault: true, sources: null })).toEqual(['claim', 'agents']);
    expect(wizardSteps({ isDefault: true, sources: 0 })).toEqual(['claim', 'agents']);
  });

  it('gives a named instance the sign-in step and the summary, and copy only with a source', () => {
    expect(wizardSteps({ isDefault: false, sources: 0 })).toEqual(['claim', 'signin', 'agents', 'summary']);
    expect(wizardSteps({ isDefault: false, sources: 2 })).toEqual(['claim', 'signin', 'copy', 'agents', 'summary']);
  });

  it('shows the copy step before login when the link names a source, and drops it when none is there', () => {
    expect(wizardSteps({ isDefault: false, sources: null, fromHint: true })).toContain('copy');
    expect(wizardSteps({ isDefault: false, sources: 0, fromHint: true })).not.toContain('copy');
  });

  it('gives a default instance with siblings the whole wizard', () => {
    expect(wizardSteps({ isDefault: true, sources: 1 })).toEqual(['claim', 'signin', 'copy', 'agents', 'summary']);
  });

  it('walks the steps in order and ends', () => {
    const steps = wizardSteps({ isDefault: false, sources: 1 });
    expect(nextStep(steps, 'claim')).toBe('signin');
    expect(nextStep(steps, 'copy')).toBe('agents');
    expect(nextStep(steps, 'summary')).toBeNull();
    expect(nextStep(['claim', 'agents'], 'copy')).toBeNull();
  });
});

describe('signInPatch', () => {
  const homes = [
    { agent: 'claude', id: 'pf_home0001' },
    { agent: 'codex', id: 'pf_home0002' },
  ];
  it('puts each agent back on its own NextBoard login', () => {
    expect(signInPatch('separate', homes)).toEqual({ agents: { profiles: { local: { claude: 'pf_home0001', codex: 'pf_home0002' } } } });
  });
  it('uses the CLI logins of the user, even for an agent without a home profile yet', () => {
    expect(signInPatch('own', [])).toEqual({ agents: { profiles: { local: { claude: '', codex: '' } } } });
  });
  it('runs the hidden runs on a provider only when asked', () => {
    expect(signInPatch('provider', homes, 'ap_abcd1234')).toMatchObject({ runner: { provider: 'ap_abcd1234' } });
    expect(signInPatch('provider', [], undefined)).toBeNull();
  });
});

describe('copy choices', () => {
  it('starts with settings, presets and MCP definitions, and no secret', () => {
    expect(DEFAULT_COPY).toMatchObject({ settings: true, presets: true, mcp: true, vault: false, providerKeys: false, machineCredentials: false, mcpSecrets: false });
    expect(secretsChosen(DEFAULT_COPY)).toEqual([]);
  });

  it('ticks what a secret belongs to, and unticks the secret with it', () => {
    let c = setChoice(DEFAULT_COPY, 'providerKeys', true);
    expect(c.providers).toBe(true);
    expect(secretsChosen(c)).toEqual(['API keys']);
    c = setChoice(c, 'providers', false);
    expect(c.providerKeys).toBe(false);
    expect(secretsChosen(c)).toEqual([]);
  });

  it('never sends a secret without what it belongs to', () => {
    const body = importBody('work', 'pw', { ...DEFAULT_COPY, machines: false, machineCredentials: true, mcp: false, mcpSecrets: true });
    expect(body).toMatchObject({ from: 'work', password: 'pw', machineCredentials: false, mcpSecrets: false, settings: true });
  });

  it('knows when nothing is ticked', () => {
    const none = { ...DEFAULT_COPY, settings: false, presets: false, mcp: false };
    expect(anythingChosen(none)).toBe(false);
    expect(anythingChosen(setChoice(none, 'vault', true))).toBe(true);
  });

  it('preselects the source the link names, or the only one', () => {
    const sources = [
      { name: 'default', setUp: true },
      { name: 'work', setUp: true },
      { name: 'fresh', setUp: false },
    ];
    expect(preselectSource(sources, 'work')).toBe('work');
    expect(preselectSource(sources, 'fresh')).toBe('');
    expect(preselectSource(sources.slice(1), '')).toBe('work');
  });

  it('says in one line what came', () => {
    expect(copiedSummary({ settings: true, agents: 2, layouts: 0, mcp: 1, machines: 0, providers: 0, vault: true, vaultItems: 1, secretsTaken: false })).toBe(
      'settings, 2 custom agents, 1 MCP server and the vault (1 item)',
    );
    expect(copiedSummary({ settings: false, agents: 0, layouts: 0, mcp: 0, machines: 0, providers: 0, vault: false, vaultItems: 0, secretsTaken: false })).toBe('nothing new');
  });
});

describe('summary', () => {
  it('puts the address this browser uses first, and marks it', () => {
    const urls = ['http://127.0.0.1:12412', 'http://100.64.0.7:12412'];
    expect(markAddresses(urls, { protocol: 'http:', host: '100.64.0.7:12412', hostname: '100.64.0.7' })).toEqual([
      { url: 'http://100.64.0.7:12412', here: true },
      { url: 'http://127.0.0.1:12412', here: false },
    ]);
    // through a proxy on another port: the same host name still marks it
    expect(markAddresses(urls, { protocol: 'https:', host: '127.0.0.1', hostname: '127.0.0.1' })[0]).toEqual({ url: 'http://127.0.0.1:12412', here: true });
    expect(markAddresses(['not a url'], { protocol: 'http:', host: 'x', hostname: 'x' })).toEqual([{ url: 'not a url', here: false }]);
  });

  it('reads the port and whether the hub is reachable from elsewhere', () => {
    expect(portOf('127.0.0.1:12412')).toBe('12412');
    expect(portOf('[::1]:12512')).toBe('12512');
    expect(beyondLoopback('127.0.0.1:12412')).toBe(false);
    expect(beyondLoopback('[::1]:12412')).toBe(false);
    expect(beyondLoopback('100.64.0.7:12412')).toBe(true);
    expect(beyondLoopback(':12412')).toBe(true);
  });

  it('names the service or says there is none', () => {
    expect(serviceText({ kind: 'systemd', name: 'nextboard-work.service', managed: true })).toContain('nextboard-work.service');
    expect(serviceText({ kind: 'none' })).toMatch(/Not a service/);
    expect(serviceText(undefined)).toBe('Unknown');
  });
});
