import { beforeEach, describe, expect, it, vi } from 'vitest';
import { store } from '../store';
import type { UptimeSettings } from './api';

// a fetch of the project's monitors and settings that answers when the test says so
const answers: ((s: UptimeSettings) => void)[] = [];
vi.mock('./api', () => ({
  uptimeApi: {
    list: () => new Promise((resolve) => answers.push((settings: UptimeSettings) => resolve({ items: [], settings }))),
  },
}));

const { currentUptimeSettings, refreshUptimeSettings, setUptimeSettings } = await import('./store');
const titles = (pid: string) => (currentUptimeSettings(pid).maintenance ?? []).map((w) => w.title);
const win = (title: string) => ({ id: `mw_${title}`, title, mode: 'manual' as const });
const flush = () => new Promise((r) => setTimeout(r, 0));

describe('uptime settings changed elsewhere', () => {
  beforeEach(() => {
    answers.length = 0;
  });

  it('are fetched again on their event, which carries none of them', async () => {
    setUptimeSettings('p_maint', { maintenance: [win('Nightly')] });
    answers.shift()!({ maintenance: [win('Nightly')] }); // the fetch every save starts
    void refreshUptimeSettings('p_maint'); // a dialog opens, and listens from now on
    answers.shift()!({ maintenance: [win('Nightly')] });
    await flush();
    store.apply({ type: 'uptimeSettings', projectId: 'p_maint' });
    expect(answers).toHaveLength(1);
    answers.shift()!({ maintenance: [win('Nightly'), win('Backup')] });
    await flush();
    expect(titles('p_maint')).toEqual(['Nightly', 'Backup']);
    // a project no card here shows is not fetched
    store.apply({ type: 'uptimeSettings', projectId: 'p_elsewhere' });
    expect(answers).toHaveLength(0);
  });

  it('a fetch that left before a save cannot bring the old list back', async () => {
    setUptimeSettings('p_race', { maintenance: [win('Nightly')] });
    answers.shift()!({ maintenance: [win('Nightly')] });
    await flush();
    void refreshUptimeSettings('p_race'); // the dialog opens
    const before = answers.shift()!;
    setUptimeSettings('p_race', { maintenance: [win('Nightly'), win('Deploy')] }); // and saves at once
    const after = answers.shift()!;
    before({ maintenance: [win('Nightly')] });
    await flush();
    expect(titles('p_race')).toEqual(['Nightly', 'Deploy']);
    // the fetch after the save also brings a window another tab added meanwhile
    after({ maintenance: [win('Nightly'), win('Deploy'), win('Backup')] });
    await flush();
    expect(titles('p_race')).toEqual(['Nightly', 'Deploy', 'Backup']);
  });
});
