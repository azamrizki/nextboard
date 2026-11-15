import { describe, expect, it } from 'vitest';
import { profileChip } from './state';

const home = { id: 'pf_home', name: 'NextBoard', agent: 'claude', machineId: 'local' };
const work = { id: 'pf_work', name: 'Work', agent: 'claude', machineId: 'local' };
const defaults = { local: { claude: 'pf_home', codex: 'pf_codexhome' } };

describe('profileChip', () => {
  it('shows nothing for the default login, NextBoard’s own folder', () => {
    expect(profileChip({ profileId: home.id, agent: 'claude', machineId: 'local' }, home, defaults)).toBeNull();
  });

  it('shows nothing for a custom agent on Claude Code’s base that runs on its default', () => {
    // a custom agent has no default of its own: it gets its base agent's
    expect(profileChip({ profileId: home.id, agent: 'myclaude', machineId: 'local' }, home, defaults)).toBeNull();
  });

  it('names any other profile, readably', () => {
    expect(profileChip({ profileId: work.id, agent: 'claude', machineId: 'local' }, work, defaults)).toBe('Work');
    expect(profileChip({ profileId: work.id, agent: 'myclaude', machineId: 'local' }, work, defaults)).toBe('Work');
  });

  it('names NextBoard’s folder when the default was turned off or is another profile', () => {
    expect(profileChip({ profileId: home.id, agent: 'claude', machineId: 'local' }, home, { local: { claude: '' } })).toBe('NextBoard');
    expect(profileChip({ profileId: home.id, agent: 'claude', machineId: 'local' }, home, { local: { claude: work.id } })).toBe('NextBoard');
    expect(profileChip({ profileId: home.id, agent: 'claude', machineId: 'local' }, home, {})).toBe('NextBoard');
  });

  it('follows the agent’s own default before its base’s', () => {
    const own = { local: { claude: home.id, myclaude: work.id } };
    expect(profileChip({ profileId: work.id, agent: 'myclaude', machineId: 'local' }, work, own)).toBeNull();
    expect(profileChip({ profileId: home.id, agent: 'myclaude', machineId: 'local' }, home, own)).toBe('NextBoard');
  });

  it('shows nothing without a profile, or before the settings say which one is the default', () => {
    expect(profileChip({ profileId: undefined, agent: 'claude', machineId: 'local' }, undefined, defaults)).toBeNull();
    expect(profileChip({ profileId: work.id, agent: 'claude', machineId: 'local' }, work, null)).toBeNull();
  });

  it('waits for a profile it does not know yet, unless it is the default', () => {
    expect(profileChip({ profileId: 'pf_new', agent: 'claude', machineId: 'local' }, undefined, defaults)).toBe('…');
    expect(profileChip({ profileId: home.id, agent: 'claude', machineId: 'local' }, undefined, defaults)).toBeNull();
  });
});
