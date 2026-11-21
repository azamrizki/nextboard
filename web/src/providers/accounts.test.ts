import { describe, expect, it } from 'vitest';
import { accountFor, isProviderId, needsProvider, splitAccount } from './accounts';

const claude = { id: 'claude' };
const ncClaude = { id: 'nextcode', engine: 'claude' };
const ncApi = { id: 'nextcode-api', engine: 'api' };

const profiles = [{ id: 'pf_work' }, { id: 'pf_home' }];
const providers = [{ id: 'ap_glm' }, { id: 'ap_kimi' }];

describe('the account a new console starts on', () => {
  it('a login agent: the profile or provider chosen while it is offered, else the default login', () => {
    expect(accountFor(claude, 'pf_work', { profiles, providers })).toBe('pf_work');
    expect(accountFor(claude, 'ap_kimi', { profiles, providers })).toBe('ap_kimi');
    expect(accountFor(claude, 'pf_gone', { profiles, providers })).toBe('');
    expect(accountFor(ncClaude, '', { profiles, providers }, 'ap_kimi')).toBe('');
  });

  it('NextCode’s API engine: a provider always, the one chosen, else Settings › NextCode’s, else the first', () => {
    expect(accountFor(ncApi, 'ap_glm', { profiles: [], providers }, 'ap_kimi')).toBe('ap_glm');
    expect(accountFor(ncApi, '', { profiles: [], providers }, 'ap_kimi')).toBe('ap_kimi');
    expect(accountFor(ncApi, '', { profiles: [], providers }, '')).toBe('ap_glm');
    // a login profile picked for another engine is never sent
    expect(accountFor(ncApi, 'pf_work', { profiles, providers }, '')).toBe('ap_glm');
    expect(accountFor(ncApi, '', { profiles: [], providers: [] }, 'ap_kimi')).toBe('');
  });

  it('holds the API engine back while there is no provider, and nothing else', () => {
    expect(needsProvider(ncApi, [])).toBe(true);
    expect(needsProvider(ncApi, providers)).toBe(false);
    expect(needsProvider(claude, [])).toBe(false);
    expect(needsProvider(undefined, [])).toBe(false);
  });

  it('sends a provider as providerId, a profile as profileId, the default as neither', () => {
    expect(splitAccount('ap_glm')).toEqual({ providerId: 'ap_glm' });
    expect(splitAccount('pf_work')).toEqual({ profileId: 'pf_work' });
    expect(splitAccount('')).toEqual({});
    expect(isProviderId('ap_glm')).toBe(true);
    expect(isProviderId('pf_work')).toBe(false);
    expect(isProviderId(undefined)).toBe(false);
  });
});
