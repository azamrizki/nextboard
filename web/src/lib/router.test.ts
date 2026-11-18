// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest';

// The routes of a project (DESIGN.md §3.19): its board, and its Code view; and the link with no
// view that asks once where to go.

beforeEach(() => {
  vi.resetModules();
});

describe('project routes', () => {
  it('parses the board and the Code view, and makes their paths', async () => {
    const { boardPath, codePath, parseRoute } = await import('./router');
    expect(parseRoute('/p/p_shop1')).toEqual({ page: 'board', projectId: 'p_shop1', workspaceId: '' });
    expect(parseRoute('/p/p_shop1/w/w_main1')).toEqual({ page: 'board', projectId: 'p_shop1', workspaceId: 'w_main1' });
    expect(parseRoute('/p/p_shop1/code')).toEqual({ page: 'code', projectId: 'p_shop1' });
    expect(parseRoute('/p/p_shop1/code/')).toEqual({ page: 'code', projectId: 'p_shop1' });
    expect(parseRoute('/p/p_shop1/code/x')).toEqual({ page: 'unknown' });
    expect(parseRoute('/p/p%20a/code')).toEqual({ page: 'code', projectId: 'p a' });
    expect(codePath('p_shop1')).toBe('/p/p_shop1/code');
    expect(codePath('p a')).toBe('/p/p%20a/code');
    expect(boardPath('p_shop1')).toBe('/p/p_shop1');
    expect(boardPath('p_shop1', 'w_main1')).toBe('/p/p_shop1/w/w_main1');
    // a round trip
    expect(parseRoute(codePath('p_x'))).toEqual({ page: 'code', projectId: 'p_x' });
  });
});

describe('the board an editor binds', () => {
  it('keeps the one pinned while it is there, else the last opened, else the first', async () => {
    const { boardOf } = await import('./router');
    const ws = [{ id: 'w_main' }, { id: 'w_api' }, { id: 'w_web' }];
    expect(boardOf(ws, 'w_api', 'w_web')?.id).toBe('w_api');
    expect(boardOf(ws, 'w_gone', 'w_web')?.id).toBe('w_web');
    expect(boardOf(ws, '', null)?.id).toBe('w_main');
    expect(boardOf(ws, 'w_gone', 'w_gone2')?.id).toBe('w_main');
    expect(boardOf([], 'w_api', 'w_web')).toBeUndefined();
    expect(boardOf(null, 'w_api')).toBeUndefined();
  });
});

describe('the link a tab opened on', () => {
  it('a project with no view is asked about once, for that project only', async () => {
    window.history.replaceState(null, '', '/p/p_shop1');
    const { isEntryLink, takeEntryLink } = await import('./router');
    expect(isEntryLink('p_other')).toBe(false);
    expect(isEntryLink('p_shop1')).toBe(true);
    // the app moving around does not change what the tab opened on
    window.history.replaceState(null, '', '/login');
    expect(isEntryLink('p_shop1')).toBe(true);
    takeEntryLink();
    expect(isEntryLink('p_shop1')).toBe(false);
    // answered once: again is nothing
    takeEntryLink();
    expect(isEntryLink('p_shop1')).toBe(false);
  });

  it('a link that names a view, or carries a query, is not asked about', async () => {
    for (const path of ['/p/p_shop1/w/w_main1', '/p/p_shop1/code', '/p/p_shop1?card=n_ed', '/p/p_shop1?focus=s_1', '/', '/settings']) {
      vi.resetModules();
      window.history.replaceState(null, '', path);
      const { isEntryLink } = await import('./router');
      expect(isEntryLink('p_shop1'), path).toBe(false);
    }
  });
});
