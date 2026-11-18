import { beforeEach, describe, expect, it, vi } from 'vitest';
import type { Project, Session } from '../../lib/types';
import { store } from '../../store';
import { usageUi } from '../../usage/prefs';
import { runPageUiAction, type PageActionEnv } from './pageActions';

// The lead's UI actions in the Code view (DESIGN.md §3.19): what an editor can do it does, the
// rest offers the board.

const project = {
  id: 'p_shop',
  name: 'Shop',
  machineId: 'local',
  path: '/srv/shop',
  parts: [
    { id: 'pt_api', name: 'api', machineId: 'local', path: '/srv/shop/api' },
    { id: 'pt_web', name: 'web', machineId: 'm_harbor', path: '/srv/shop/web' },
  ],
} as unknown as Project;

function env() {
  const e = {
    ws: { open: vi.fn(), openTool: vi.fn(), openPane: vi.fn(), toKey: vi.fn((p: string) => `//local${p}`), changed: vi.fn(), focusTick: 0 },
    project,
    ctl: { node: vi.fn((id: string) => (id === 'n_term1' ? { data: { sessionId: 's_bintang' } } : id === 'n_note1' ? { data: {} } : undefined)) },
    onBoard: vi.fn(),
    showView: vi.fn(),
  };
  return e as typeof e & PageActionEnv;
}

beforeEach(() => {
  store.reset();
  store.seedSessions([{ id: 's_bintang', name: 'Bintang', status: 'running' } as unknown as Session]);
});

describe('runPageUiAction', () => {
  it('opens a file in the editor, on the machine of the part the lead meant', () => {
    const e = env();
    runPageUiAction({ action: 'openFile', path: '/srv/shop/web/index.html', line: 7, part: 'pt_web' }, e);
    expect(e.ws.open).toHaveBeenCalledWith(expect.stringContaining('/srv/shop/web/index.html'), { pin: true, source: true, line: 7 });
    expect(String(e.ws.open.mock.calls[0][0])).not.toContain('local');
    runPageUiAction({ action: 'openFile', path: '/srv/shop/api/app.ts' }, e);
    expect(e.ws.toKey).toHaveBeenCalledWith('/srv/shop/api/app.ts');
    expect(e.ws.open).toHaveBeenLastCalledWith('//local/srv/shop/api/app.ts', { pin: true, source: true, line: undefined });
    runPageUiAction({ action: 'openFile' }, e);
    expect(e.ws.open).toHaveBeenCalledTimes(2);
  });

  it('opens panels as tool tabs, Git as Source Control, and the usage popover', () => {
    const e = env();
    for (const panel of ['memory', 'loops', 'mission', 'chats', 'documents', 'markdown', 'skills', 'diagrams']) runPageUiAction({ action: 'openPanel', panel }, e);
    expect(e.ws.openTool.mock.calls.map((c) => c[0])).toEqual(['memory', 'loops', 'mission', 'chats', 'documents', 'markdown', 'skills', 'diagrams']);
    runPageUiAction({ action: 'openPanel', panel: 'git' }, e);
    expect(e.showView).toHaveBeenCalledWith('scm');
    runPageUiAction({ action: 'openPanel', panel: 'usage' }, e);
    expect(usageUi.get()).toBe(true);
    usageUi.set(false);
    runPageUiAction({ action: 'openPanel', panel: 'code' }, e);
    expect(e.ws.focusTick).toBe(1);
    runPageUiAction({ action: 'openPanel', panel: 'whiteboard' }, e);
    expect(e.onBoard).toHaveBeenCalledWith('Whiteboard');
  });

  it('shows a console as a tab, and sends a card or a board action to the board', () => {
    const e = env();
    runPageUiAction({ action: 'focus', target: 's_bintang' }, e);
    runPageUiAction({ action: 'focus', target: 'n_term1' }, e);
    expect(e.ws.openPane.mock.calls).toEqual([['s_bintang'], ['s_bintang']]);
    runPageUiAction({ action: 'focus', target: 'n_note1' }, e);
    expect(e.onBoard).toHaveBeenLastCalledWith('That card');
    for (const action of ['fit', 'zoomIn', 'zoomOut', 'zoomReset', 'tool', 'undo', 'redo']) runPageUiAction({ action }, e);
    expect(e.onBoard).toHaveBeenCalledTimes(8);
    runPageUiAction({ action: 'closePanel' }, e);
    expect(e.onBoard).toHaveBeenCalledTimes(8);
  });
});
