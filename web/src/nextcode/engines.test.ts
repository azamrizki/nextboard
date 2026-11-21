import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Agent } from '../lib/types';
import { agentChoiceName, apiProviderChoice, engineChoices, engineLabel, engineOf, engineSwitch, foldEngines, isApiEngine, isNextCodeBuiltin, NEXTCODE_AGENTS, preferredEngine, rememberEngine } from './engines';

const agent = (id: string, over: Partial<Agent> = {}) => ({ id, name: id, kind: 'agent', ...over }) as Agent;

const claude = agent('claude', { name: 'Claude Code' });
const codex = agent('codex', { name: 'Codex' });
const ncClaude = agent('nextcode', { name: 'NextCode', engine: 'claude', base: 'claude' });
const ncCodex = agent('nextcode-codex', { name: 'NextCode', engine: 'codex', base: 'codex' });
const ncApi = agent('nextcode-api', { name: 'NextCode', engine: 'api', base: '' });
const gemini = agent('gemini', { name: 'Gemini CLI' });
const mine = agent('mine', { name: 'Mine', engine: 'claude', source: 'custom' });

describe('NextCode engines', () => {
  it('knows the three built-ins, and nothing else, as NextCode', () => {
    expect(NEXTCODE_AGENTS).toEqual({ claude: 'nextcode', codex: 'nextcode-codex', api: 'nextcode-api' });
    expect(['nextcode', 'nextcode-codex', 'nextcode-api'].every(isNextCodeBuiltin)).toBe(true);
    expect(['claude', 'codex', 'mine', 'api', '', undefined].some(isNextCodeBuiltin)).toBe(false);
  });

  it('names engines for the chat and says which one an agent runs on', () => {
    expect(engineLabel('claude')).toBe('Claude');
    expect(engineLabel('codex')).toBe('Codex');
    expect(engineLabel('api')).toBe('API');
    expect(engineLabel('gemini')).toBe('Gemini');
    expect(engineLabel('')).toBe('');
    expect(engineOf(ncCodex)).toBe('codex');
    expect(engineOf(mine)).toBe('claude');
    // before the catalog says: the built-in's id
    expect(engineOf({ id: 'nextcode-codex' })).toBe('codex');
    expect(engineOf({ id: 'nextcode' })).toBe('claude');
    expect(engineOf({ id: 'nextcode-api' })).toBe('api');
    expect(engineOf(ncApi)).toBe('api');
    expect(engineOf(claude)).toBe('');
    expect(engineOf(undefined)).toBe('');
  });

  it('tells the API engine, which has no CLI and no login, from the others', () => {
    expect(isApiEngine(ncApi)).toBe(true);
    expect(isApiEngine({ id: 'nextcode-api' })).toBe(true);
    expect([ncClaude, ncCodex, claude, mine, undefined].some(isApiEngine)).toBe(false);
  });

  it('a browser with nothing stored prefers Claude', () => {
    expect(preferredEngine()).toBe('claude');
  });
});

describe('the engine picked last', () => {
  afterEach(() => {
    rememberEngine('claude');
    vi.unstubAllGlobals();
  });

  it('remembers the API engine as well as the others, and reads it back', () => {
    const kept = new Map<string, string>();
    vi.stubGlobal('window', { localStorage: { getItem: (k: string) => kept.get(k) ?? null, setItem: (k: string, v: string) => void kept.set(k, v) } });
    rememberEngine('api');
    expect(preferredEngine()).toBe('api');
    expect(kept.get('nb.nextcode.engine')).toBe('api');
    rememberEngine('codex');
    expect(preferredEngine()).toBe('codex');
  });
});

describe('engineChoices', () => {
  it('offers Claude and Codex, each mapped to its agent', () => {
    const c = engineChoices([claude, codex, ncClaude, ncCodex]);
    expect(c.map((x) => [x.value, x.label, x.agentId, x.disabled])).toEqual([
      ['claude', 'Claude', 'nextcode', false],
      ['codex', 'Codex', 'nextcode-codex', false],
    ]);
  });

  it('offers the API engine third, with a title of its own', () => {
    const c = engineChoices([claude, codex, ncClaude, ncCodex, ncApi], { providers: 2 });
    expect(c.map((x) => [x.value, x.label, x.agentId, x.disabled])).toEqual([
      ['claude', 'Claude', 'nextcode', false],
      ['codex', 'Codex', 'nextcode-codex', false],
      ['api', 'API', 'nextcode-api', false],
    ]);
    expect(c[2].title).toBe('NextCode’s own engine: no CLI, it runs on an API provider');
    expect(c[0].title).toMatch(/Claude Code, with its own logins/);
    expect(c[1].title).toMatch(/Codex, with its own logins/);
  });

  it('disables the API engine while there is no API provider, and says where to add one', () => {
    const c = engineChoices([ncClaude, ncCodex, ncApi], { providers: 0 });
    expect(c.map((x) => x.disabled)).toEqual([false, false, true]);
    expect(c[2].title).toBe('Add an API provider in Settings › API providers first');
    // the count not known yet (the list still loading): not held back
    expect(engineChoices([ncClaude, ncApi]).map((x) => x.disabled)).toEqual([false, false]);
  });

  it('offers any two engines the hub has, and disables one the hub says cannot run', () => {
    expect(engineChoices([ncClaude, ncApi], { providers: 1 }).map((x) => x.value)).toEqual(['claude', 'api']);
    expect(engineChoices([ncCodex, ncApi], { providers: 1 }).map((x) => x.value)).toEqual(['codex', 'api']);
    const off = engineChoices([ncClaude, { ...ncApi, available: false }], { where: 'This PC', providers: 1 });
    expect(off[1]).toMatchObject({ disabled: true, title: 'The API engine does not run on This PC' });
  });

  it('disables an engine whose CLI is not installed, and says why', () => {
    const c = engineChoices([ncClaude, { ...ncCodex, available: false }], 'This PC');
    expect(c.map((x) => x.disabled)).toEqual([false, true]);
    expect(c[1].title).toMatch(/codex CLI.*This PC/);
  });

  it('offers no control when the hub has one engine only (an older hub, or the others turned off)', () => {
    expect(engineChoices([claude, ncClaude])).toEqual([]);
    expect(engineChoices([codex, ncCodex])).toEqual([]);
    expect(engineChoices([ncApi], { providers: 3 })).toEqual([]);
    expect(engineChoices([])).toEqual([]);
  });

  it('maps an engine to the agent to switch to', () => {
    expect(engineSwitch([ncClaude, ncCodex], 'codex')).toBe('nextcode-codex');
    expect(engineSwitch([ncClaude, ncCodex], 'claude')).toBe('nextcode');
    expect(engineSwitch([ncClaude, ncCodex, ncApi], 'api')).toBe('nextcode-api');
    expect(engineSwitch([ncClaude], 'codex')).toBe('');
  });
});

describe('foldEngines', () => {
  const list = [claude, codex, ncClaude, ncCodex, gemini, mine];
  const ids = (l: Agent[]) => l.map((a) => a.id);

  it('lists NextCode once, in the place of the first, on the engine chosen', () => {
    expect(ids(foldEngines(list, 'nextcode-codex', 'claude'))).toEqual(['claude', 'codex', 'nextcode-codex', 'gemini', 'mine']);
    expect(ids(foldEngines(list, 'nextcode', 'codex'))).toEqual(['claude', 'codex', 'nextcode', 'gemini', 'mine']);
  });

  it('with another agent chosen, takes the engine preferred, if it can run here', () => {
    expect(ids(foldEngines(list, 'claude', 'codex'))).toEqual(['claude', 'codex', 'nextcode-codex', 'gemini', 'mine']);
    expect(ids(foldEngines(list, 'claude', 'claude'))).toEqual(['claude', 'codex', 'nextcode', 'gemini', 'mine']);
    const noCodexCli = [ncClaude, { ...ncCodex, available: false }];
    expect(ids(foldEngines(noCodexCli, '', 'codex'))).toEqual(['nextcode']);
    const noClaudeCli = [{ ...ncClaude, available: false }, ncCodex];
    expect(ids(foldEngines(noClaudeCli, '', 'claude'))).toEqual(['nextcode-codex']);
  });

  it('leaves a list with one NextCode, and custom chat agents, as they are', () => {
    expect(ids(foldEngines([claude, ncClaude, mine], 'claude'))).toEqual(['claude', 'nextcode', 'mine']);
    expect(ids(foldEngines([ncCodex, gemini]))).toEqual(['nextcode-codex', 'gemini']);
    expect(ids(foldEngines([ncApi, gemini]))).toEqual(['nextcode-api', 'gemini']);
  });

  it('folds all three engines into one NextCode, in the place of the first', () => {
    const three = [claude, ncClaude, ncCodex, gemini, ncApi];
    expect(ids(foldEngines(three, 'nextcode-api', 'claude'))).toEqual(['claude', 'nextcode-api', 'gemini']);
    expect(ids(foldEngines(three, 'gemini', 'api'))).toEqual(['claude', 'nextcode-api', 'gemini']);
    expect(ids(foldEngines(three, 'gemini', 'codex'))).toEqual(['claude', 'nextcode-codex', 'gemini']);
    // two of them are enough to fold
    expect(ids(foldEngines([ncClaude, ncApi], '', 'api'))).toEqual(['nextcode-api']);
    expect(ids(foldEngines([ncClaude, { ...ncApi, available: false }], '', 'api'))).toEqual(['nextcode']);
  });
});

describe('agentChoiceName', () => {
  it('tells the two engines apart where both are listed', () => {
    const both = [ncClaude, ncCodex];
    expect(agentChoiceName(ncClaude, both)).toBe('NextCode · Claude');
    expect(agentChoiceName(ncCodex, both)).toBe('NextCode · Codex');
    expect(agentChoiceName(claude, both)).toBe('Claude Code');
  });

  it('names the API engine "NextCode · API" next to another engine', () => {
    expect(agentChoiceName(ncApi, [ncClaude, ncCodex, ncApi])).toBe('NextCode · API');
    expect(agentChoiceName(ncApi, [ncClaude, ncApi])).toBe('NextCode · API');
    expect(agentChoiceName(ncClaude, [ncClaude, ncApi])).toBe('NextCode · Claude');
    expect(agentChoiceName(ncCodex, [ncCodex, ncApi])).toBe('NextCode · Codex');
  });

  it('keeps the name where only one is listed', () => {
    expect(agentChoiceName(ncClaude, [claude, ncClaude])).toBe('NextCode');
    expect(agentChoiceName(ncCodex, [ncCodex])).toBe('NextCode');
    expect(agentChoiceName(ncApi, [claude, ncApi])).toBe('NextCode');
    expect(agentChoiceName(mine, [mine, ncClaude, ncCodex])).toBe('Mine');
  });
});

describe('apiProviderChoice', () => {
  const providers = [{ id: 'ap_glm' }, { id: 'ap_kimi' }, { id: 'ap_local' }];

  it('takes the provider chosen, while it is still there', () => {
    expect(apiProviderChoice('ap_local', providers, 'ap_kimi')).toBe('ap_local');
  });

  it('else Settings › NextCode’s default, else the first', () => {
    expect(apiProviderChoice('', providers, 'ap_kimi')).toBe('ap_kimi');
    expect(apiProviderChoice('ap_gone', providers, 'ap_kimi')).toBe('ap_kimi');
    expect(apiProviderChoice('pf_login', providers, '')).toBe('ap_glm');
    expect(apiProviderChoice(undefined, providers, 'ap_removed')).toBe('ap_glm');
  });

  it('has nothing to run on without a provider', () => {
    expect(apiProviderChoice('ap_glm', [], 'ap_glm')).toBe('');
  });
});
