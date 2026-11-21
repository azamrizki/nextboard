import { describe, expect, it } from 'vitest';
import { baseOf, compatible, providerModelList, providerModels, type ApiProvider } from './providers';

const provider = (over: Partial<ApiProvider>): ApiProvider => ({
  id: 'ap_x',
  name: 'X',
  kind: 'anthropic',
  baseUrl: 'https://api.example.com',
  model: 'm-1',
  hasKey: true,
  createdAt: '',
  updatedAt: '',
  ...over,
});

const anthropic = provider({ id: 'ap_glm', kind: 'anthropic', model: 'glm-4.6', fastModel: 'glm-4.5-air', models: ['glm-4.6', 'glm-z1'] });
const chat = provider({ id: 'ap_ds', kind: 'openai', api: 'chat', model: 'deepseek-chat' });
const responses = provider({ id: 'ap_oai', kind: 'openai', api: 'responses', model: 'gpt-x' });
const all = [anthropic, chat, responses];

const claude = { id: 'claude', base: '' };
const codex = { id: 'codex', base: '' };
const opencode = { id: 'opencode', base: '' };
const ncClaude = { id: 'nextcode', base: 'claude', engine: 'claude' };
const ncCodex = { id: 'nextcode-codex', base: 'codex', engine: 'codex' };
const ncApi = { id: 'nextcode-api', base: '', engine: 'api' };

const fits = (a: Parameters<typeof compatible>[0]) => all.filter((p) => compatible(a, p)).map((p) => p.id);

describe('which agents a provider can run', () => {
  it('Claude Code on Anthropic’s API, Codex on the Responses API, OpenCode on any', () => {
    expect(fits(claude)).toEqual(['ap_glm']);
    expect(fits(codex)).toEqual(['ap_oai']);
    expect(fits(opencode)).toEqual(['ap_glm', 'ap_ds', 'ap_oai']);
    // NextCode's Claude and Codex engines follow their CLI
    expect(fits(ncClaude)).toEqual(['ap_glm']);
    expect(fits(ncCodex)).toEqual(['ap_oai']);
  });

  it('NextCode’s API engine on every provider: Anthropic, OpenAI Chat Completions and Responses', () => {
    expect(fits(ncApi)).toEqual(['ap_glm', 'ap_ds', 'ap_oai']);
    // by its id, before the catalog says its engine
    expect(fits({ id: 'nextcode-api', base: '' })).toEqual(['ap_glm', 'ap_ds', 'ap_oai']);
  });

  it('nothing for an agent that is not known, or not given', () => {
    expect(fits({ id: 'gemini', base: '' })).toEqual([]);
    expect(fits(undefined)).toEqual([]);
  });

  it('counts an agent with no base as its own CLI', () => {
    expect(baseOf(claude)).toBe('claude');
    expect(baseOf(ncClaude)).toBe('claude');
    expect(baseOf(ncApi)).toBe('nextcode-api');
  });
});

describe('a provider’s model list', () => {
  it('offers its model, its fast model and what it listed, once each', () => {
    expect(providerModels(anthropic)).toEqual(['glm-4.6', 'glm-4.5-air', 'glm-z1']);
  });

  it('has the provider’s model as the default, with the efforts the agent’s list gives', () => {
    const l = providerModelList(anthropic, ['low', 'high'], ncClaude);
    expect(l).toMatchObject({ current: 'glm-4.6', source: 'provider', efforts: ['low', 'high'] });
    expect(l.models.map((m) => m.id)).toEqual(['glm-4.6', 'glm-4.5-air', 'glm-z1']);
  });

  it('gives the API engine low to max when the hub lists no efforts', () => {
    expect(providerModelList(chat, undefined, ncApi).efforts).toEqual(['low', 'medium', 'high', 'xhigh', 'max']);
    expect(providerModelList(chat, [], ncApi).efforts).toEqual(['low', 'medium', 'high', 'xhigh', 'max']);
    // the hub's list wins
    expect(providerModelList(chat, ['high'], ncApi).efforts).toEqual(['high']);
    // a CLI that lists none has none
    expect(providerModelList(anthropic, undefined, claude).efforts).toEqual([]);
  });
});
