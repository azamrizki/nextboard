import { describe, expect, it } from 'vitest';
import type { Profile, Settings } from '../lib/types';
import { accountIds, addableLogins, menuAccounts, ncAccounts, sessionAccount, type AccountSources } from './accounts';

const profile = (id: string, agent: string, over: Partial<Profile> = {}): Profile => ({ id, agent, machineId: 'local', name: id, dir: `/d/${id}`, createdByHub: true, createdAt: '', exists: true, ...over });

const claudeHome = profile('pf_claude', 'nextcode', { home: true, name: 'NextCode' });
const codexHome = profile('pf_codex', 'nextcode-codex', { home: true, name: 'NextCode' });
const work = profile('pf_work', 'nextcode', { name: 'Work' });
const codeLogin = profile('pf_cc', 'claude', { home: true }); // Claude Code's own: never NextCode's
const deep = { id: 'ap_deep', name: 'DeepSeek', kind: 'openai' as const, api: 'chat' as const };
const anth = { id: 'ap_anth', name: 'Anthropic', kind: 'anthropic' as const };

const settings = (nextcode: Partial<NonNullable<Settings['nextcode']>> = {}, profiles: Record<string, string> = {}) =>
  ({ agents: { profiles: { local: profiles } }, nextcode: { permissionMode: 'default', idleExitMin: 30, ...nextcode } }) as unknown as Settings;

const src = (over: Partial<AccountSources> = {}): AccountSources => ({
  settings: settings(),
  profiles: [claudeHome, codexHome, work, codeLogin],
  providers: [deep, anth],
  agents: [
    { id: 'nextcode', available: true },
    { id: 'nextcode-codex', available: true },
    { id: 'nextcode-api', available: true },
  ],
  ...over,
});

describe('NextCode’s accounts', () => {
  it('by default: its Claude and Codex logins where their CLI is installed, and the default provider', () => {
    expect(accountIds(src())).toEqual(['pf_claude', 'pf_codex']);
    expect(accountIds(src({ settings: settings({ provider: 'ap_deep' }) }))).toEqual(['pf_claude', 'pf_codex', 'ap_deep']);
    expect(accountIds(src({ agents: [{ id: 'nextcode', available: true }, { id: 'nextcode-codex', available: false }] }))).toEqual(['pf_claude']);
    // the default login of Settings › Profiles wins over the home folder
    expect(accountIds(src({ settings: settings({}, { nextcode: 'pf_work' }) }))[0]).toBe('pf_work');
  });

  it('as the owner chose them, in their order, with names and engines', () => {
    const s = src({ settings: settings({ accounts: ['ap_deep', 'pf_claude', 'pf_codex', 'pf_cc'] }) });
    expect(ncAccounts(s)).toEqual([
      { id: 'ap_deep', kind: 'provider', engine: 'api', agentId: 'nextcode-api', name: 'DeepSeek', detail: 'API provider · OpenAI-compatible API' },
      { id: 'pf_claude', kind: 'login', engine: 'claude', agentId: 'nextcode', name: 'Claude', detail: 'Claude account, through Claude Code' },
      { id: 'pf_codex', kind: 'login', engine: 'codex', agentId: 'nextcode-codex', name: 'Codex', detail: 'ChatGPT account, through Codex' },
    ]);
    // none at all is a choice too
    expect(ncAccounts(src({ settings: settings({ accounts: [] }) }))).toEqual([]);
  });

  it('tells two logins of one engine apart, and keeps a removed provider as one that cannot run', () => {
    const names = ncAccounts(src({ settings: settings({ accounts: ['pf_claude', 'pf_work', 'ap_gone'] }) })).map((a) => [a.name, !!a.missing]);
    expect(names).toEqual([
      ['Claude · NextCode', false],
      ['Claude · Work', false],
      ['A removed provider', true],
    ]);
  });

  it('lists the console’s own account first when it is not among them', () => {
    const s = src({ settings: settings({ accounts: ['pf_claude'] }) });
    expect(menuAccounts('ap_anth', s).map((a) => a.id)).toEqual(['ap_anth', 'pf_claude']);
    expect(menuAccounts('pf_claude', s).map((a) => a.id)).toEqual(['pf_claude']);
    expect(menuAccounts('', s).map((a) => a.id)).toEqual(['pf_claude']);
  });

  it('knows the account a console runs on, its agent’s default login when it names none', () => {
    expect(sessionAccount({ providerId: 'ap_deep', profileId: 'pf_claude' }, src())).toBe('ap_deep');
    expect(sessionAccount({ profileId: 'pf_work' }, src())).toBe('pf_work');
    expect(sessionAccount({ agent: 'nextcode-codex' }, src())).toBe('pf_codex');
    expect(sessionAccount(undefined, src())).toBe('');
  });

  it('offers the logins of an engine not added yet', () => {
    const s = src({ settings: settings({ accounts: ['pf_claude'] }) });
    expect(addableLogins('claude', s).map((p) => p.id)).toEqual(['pf_work']);
    expect(addableLogins('codex', s).map((p) => p.id)).toEqual(['pf_codex']);
  });
});
