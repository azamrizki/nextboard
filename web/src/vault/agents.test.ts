import { describe, expect, it } from 'vitest';
import { agentEntryFor, agentValues, sameEntry } from './agents';
import { emptyItem, readRecord, type VaultItem } from './model';

function login(patch: Partial<VaultItem> = {}): VaultItem {
  return { ...emptyItem('login', 'item-0001'), name: 'Stripe test', values: { username: 'deploy', password: 'sk_test_123', totp: 'JBSWY3DP' }, ...patch };
}

describe('agentValues', () => {
  it('gives a login its username, password and custom fields, never the authenticator key', () => {
    const it = login({ fields: [{ name: 'STRIPE_KEY', value: 'sk_live', hidden: true }, { name: 'empty', value: '', hidden: false }, { name: ' Password ', value: 'dup', hidden: true }] });
    expect(agentValues(it).map((v) => v.name)).toEqual(['username', 'password', 'STRIPE_KEY']);
  });

  it('gives cards and identities nothing of their own', () => {
    const card = { ...emptyItem('card', 'item-0002'), values: { number: '4242424242424242', code: '123' } };
    expect(agentValues(card)).toEqual([]);
    const ssh = { ...emptyItem('ssh', 'item-0003'), values: { privateKey: 'KEY', publicKey: 'PUB' } };
    expect(agentValues(ssh).map((v) => v.name)).toEqual(['privateKey', 'publicKey']);
    const note = { ...emptyItem('note', 'item-0004'), notes: 'token' };
    expect(agentValues(note)).toEqual([{ name: 'note', value: 'token' }]);
  });

  it('leaves out names no catalog may hold', () => {
    const it = login({ values: {}, fields: [{ name: 'a\nb', value: 'x', hidden: true }, { name: 'x'.repeat(101), value: 'x', hidden: true }] });
    expect(agentValues(it)).toEqual([]);
  });
});

describe('agentEntryFor', () => {
  it('is what the hub gets: names, never a value', () => {
    const it = login({ agents: { projects: ['p_b', 'p_a', 'p_b'] } });
    const e = agentEntryFor(it);
    expect(e).toEqual({ id: 'item-0001', name: 'Stripe test', type: 'login', projects: ['p_a', 'p_b'], values: ['username', 'password'] });
    expect(JSON.stringify(e)).not.toContain('sk_test');
  });

  it('is nothing when not shared, in the trash, or with no project left', () => {
    expect(agentEntryFor(login())).toBeNull();
    expect(agentEntryFor(login({ agents: { projects: [] } }))).toBeNull();
    expect(agentEntryFor(login({ agents: { projects: ['p_a'] }, deletedAt: new Date().toISOString() }))).toBeNull();
    expect(agentEntryFor(login({ agents: { projects: ['p_gone'] } }), new Set(['p_a']))).toBeNull();
    expect(agentEntryFor(login({ agents: { projects: ['p_a'] }, values: {} }))).toBeNull();
  });

  it('compares entries by what agents would see', () => {
    const a = agentEntryFor(login({ agents: { projects: ['p_a'] } }))!;
    expect(sameEntry({ ...a, updatedAt: 'x' }, a)).toBe(true);
    expect(sameEntry({ ...a, projects: ['p_b'] }, a)).toBe(false);
    expect(sameEntry(undefined, a)).toBe(false);
  });
});

describe('the share in a blob', () => {
  it('comes back from a blob, and junk does not', () => {
    const raw = { k: 'item', type: 'login', name: 'x', agents: { projects: ['p_a', 3, '', 'p_a'] } };
    expect((readRecord(raw, 'item-0001', 1) as VaultItem).agents).toEqual({ projects: ['p_a'] });
    expect((readRecord({ ...raw, agents: 'all' }, 'item-0001', 1) as VaultItem).agents).toBeUndefined();
    expect((readRecord({ ...raw, agents: undefined }, 'item-0001', 1) as VaultItem).agents).toBeUndefined();
  });
});
