import { beforeEach, describe, expect, it } from 'vitest';
import type { HubEvent, Project } from '../lib/types';
import { store } from '../store';
import { studioStore } from './store';

const status = { branch: 'main', ahead: 1, behind: 0, changed: 2 };
const shop = (order: string[]) =>
  ({
    id: 'p_shop',
    name: 'Shop',
    machineId: 'local',
    path: '/w/api',
    workspaces: [],
    parts: order.map((id) => ({ id, name: id.slice(3), machineId: 'local', path: `/w/${id.slice(3)}` })),
  }) as unknown as Project;
const send = (ev: object) => studioStore.apply(ev as HubEvent);

// v0.9.0-beta.2: every part's review and git state is keyed "<project>~<part>", the first part's too
describe('studio store keys', () => {
  beforeEach(() => {
    studioStore.reset();
    store.upsertProject(shop(['pt_api', 'pt_web']));
  });

  it('keeps every part git status under its part ref', () => {
    send({ type: 'gitPart', projectId: 'p_shop', part: 'pt_api', repo: '', status });
    send({ type: 'gitPart', projectId: 'p_shop', part: 'pt_web', repo: '', status: { ...status, changed: 5 } });
    const s = studioStore.get();
    expect(s.git['p_shop~pt_api']).toEqual(status);
    expect(s.git['p_shop~pt_web'].changed).toBe(5);
    expect(s.gitRev['p_shop~pt_api']).toBe(1);
    expect(s.git.p_shop).toBeUndefined();
  });

  it('keeps the old git event, sent for the first part, under the bare project id', () => {
    send({ type: 'git', projectId: 'p_shop', status });
    expect(studioStore.get().git.p_shop).toEqual(status);
    expect(studioStore.get().git['p_shop~pt_api']).toBeUndefined();
  });

  it('bumps the review of the part the event names, never a bare project id', () => {
    send({ type: 'review', projectId: 'p_shop~pt_api' });
    send({ type: 'review', projectId: 'p_shop~pt_web' });
    let rev = studioStore.get().reviewRev;
    expect(rev).toEqual({ 'p_shop~pt_api': 1, 'p_shop~pt_web': 1 });
    // reordered: the keys of the parts stay
    store.upsertProject(shop(['pt_web', 'pt_api']));
    send({ type: 'review', projectId: 'p_shop~pt_api' });
    send({ type: 'review', projectId: 'p_shop~pt_web' });
    rev = studioStore.get().reviewRev;
    expect(rev).toEqual({ 'p_shop~pt_api': 2, 'p_shop~pt_web': 2 });
  });

  it('a project the tab does not know: only the key the event names', () => {
    send({ type: 'review', projectId: 'p_other~pt_x' });
    expect(studioStore.get().reviewRev).toEqual({ 'p_other~pt_x': 1 });
  });
});
