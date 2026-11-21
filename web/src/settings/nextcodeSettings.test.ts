import { describe, expect, it } from 'vitest';
import { API_AUTO, idleMinutes, NEXTCODE_DEFAULTS, nextcodeIntro, nextcodeOf, nextcodeProvider, PERMISSION_MODES } from './nextcodeSettings';
import { groupByAgent } from './profileGroups';

describe('nextcodeOf', () => {
  it('takes what the hub has', () => {
    expect(nextcodeOf({ nextcode: { permissionMode: 'plan', idleExitMin: 90 } })).toEqual({ permissionMode: 'plan', idleExitMin: 90 });
  });

  it('falls back to the hub’s defaults on an older hub or a value it would not take', () => {
    expect(nextcodeOf({})).toEqual(NEXTCODE_DEFAULTS);
    expect(NEXTCODE_DEFAULTS).toEqual({ permissionMode: 'default', idleExitMin: 30 });
    expect(nextcodeOf({ nextcode: { permissionMode: 'yolo' as never, idleExitMin: 0 } })).toEqual(NEXTCODE_DEFAULTS);
    expect(nextcodeOf({ nextcode: { permissionMode: 'auto', idleExitMin: 2000 } })).toEqual({ permissionMode: 'auto', idleExitMin: 30 });
  });

  it('offers the five modes the hub takes, Ask first and Bypass last', () => {
    expect(PERMISSION_MODES.map((m) => m.value)).toEqual(['default', 'acceptEdits', 'plan', 'auto', 'bypassPermissions']);
    expect(PERMISSION_MODES.map((m) => m.label)).toEqual(['Ask', 'Accept edits', 'Plan', 'Auto', 'Bypass']);
  });

  it('says what Auto means on the API engine, where no Claude classifier checks', () => {
    const auto = PERMISSION_MODES.find((m) => m.value === 'auto')!;
    expect(auto.title).toMatch(/Claude checks each action/);
    expect(auto.title).toMatch(/On the API engine: edits in the project go ahead, read-only commands run, other commands ask/);
    expect(API_AUTO).toMatch(/edits in the project go ahead, read-only commands run, and other commands ask/);
  });
});

describe('the API engine’s default provider', () => {
  it('is settings.nextcode.provider, and none when it is empty, absent or not a string', () => {
    expect(nextcodeProvider({ nextcode: { permissionMode: 'default', idleExitMin: 30, provider: 'ap_glm' } })).toBe('ap_glm');
    expect(nextcodeProvider({ nextcode: { permissionMode: 'default', idleExitMin: 30, provider: '' } })).toBe('');
    expect(nextcodeProvider({ nextcode: { permissionMode: 'default', idleExitMin: 30 } })).toBe('');
    expect(nextcodeProvider({ nextcode: { provider: 7 } as never })).toBe('');
    expect(nextcodeProvider({})).toBe('');
    expect(nextcodeProvider(null)).toBe('');
  });

  it('does not change what nextcodeOf gives', () => {
    expect(nextcodeOf({ nextcode: { permissionMode: 'plan', idleExitMin: 90, provider: 'ap_glm' } })).toEqual({ permissionMode: 'plan', idleExitMin: 90 });
  });
});

describe('nextcodeIntro', () => {
  it('says NextCode is one agent on the accounts added, apart from Claude Code and Codex', () => {
    expect(nextcodeIntro()).toMatch(/^NextCode is one agent: add the accounts it may run on/);
    expect(nextcodeIntro()).toMatch(/apart from Claude Code and Codex\.$/);
  });
});

describe('idleMinutes', () => {
  it('rounds and keeps it within 1–1440 minutes', () => {
    expect(idleMinutes('45')).toBe(45);
    expect(idleMinutes(' 12.6 ')).toBe(13);
    expect(idleMinutes('0')).toBe(1);
    expect(idleMinutes('-5')).toBe(1);
    expect(idleMinutes('5000')).toBe(1440);
  });

  it('gives nothing for what is not a number, so the field goes back', () => {
    expect(idleMinutes('')).toBeNull();
    expect(idleMinutes('  ')).toBeNull();
    expect(idleMinutes('soon')).toBeNull();
  });
});

describe('groupByAgent', () => {
  const agents = [
    { id: 'claude', name: 'Claude Code' },
    { id: 'codex', name: 'Codex' },
    { id: 'nextcode', name: 'NextCode' },
  ];

  it('puts NextCode’s profiles in a group of their own, never among Claude Code’s', () => {
    const groups = groupByAgent(
      [
        { id: 'pf_nc', agent: 'nextcode' },
        { id: 'pf_cc', agent: 'claude' },
        { id: 'pf_cx', agent: 'codex' },
        { id: 'pf_cc2', agent: 'claude' },
      ],
      agents,
    );
    expect(groups.map((g) => [g.name, g.profiles.map((p) => p.id)])).toEqual([
      ['Claude Code', ['pf_cc', 'pf_cc2']],
      ['Codex', ['pf_cx']],
      ['NextCode', ['pf_nc']],
    ]);
  });

  it('keeps a login profile group for the login engines only, named apart from the API engine', () => {
    const three = [...agents, { id: 'nextcode-api', name: 'NextCode', engine: 'api' }];
    const groups = groupByAgent([{ id: 'pf_nc', agent: 'nextcode' }], three);
    expect(groups.map((g) => g.name)).toEqual(['NextCode · Claude']);
  });

  it('tells NextCode’s two engines apart, Claude first', () => {
    const both = [...agents, { id: 'nextcode-codex', name: 'NextCode', engine: 'codex' }];
    const groups = groupByAgent(
      [
        { id: 'pf_ncx', agent: 'nextcode-codex' },
        { id: 'pf_nc', agent: 'nextcode' },
        { id: 'pf_cx', agent: 'codex' },
      ],
      both,
    );
    expect(groups.map((g) => [g.name, g.profiles.map((p) => p.id)])).toEqual([
      ['Codex', ['pf_cx']],
      ['NextCode · Claude', ['pf_nc']],
      ['NextCode · Codex', ['pf_ncx']],
    ]);
  });

  it('names an agent the catalog does not know by its id, after the known ones', () => {
    const groups = groupByAgent(
      [
        { id: 'pf_x', agent: 'mystery' },
        { id: 'pf_nc', agent: 'nextcode' },
      ],
      agents,
    );
    expect(groups.map((g) => g.name)).toEqual(['NextCode', 'mystery']);
  });
});
