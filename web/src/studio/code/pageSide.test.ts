import { describe, expect, it, vi } from 'vitest';
import { noSideBar, placePaneSide, registerPhoneSheets, showAgents, showLead, showPane } from './pageSide';
import type { Workspace } from './workspace';

// What would show in the right side bar goes, on a phone's Code view, to its sheets or to a tab
// (DESIGN.md §3.19); elsewhere it does what the side bar always did.

function fakeWs(panes: { tabs?: string[]; side?: string[] } = {}) {
  const ws = {
    rightTab: 'pane',
    rightOpen: false,
    rightPanes: panes.side ?? [],
    changed: vi.fn(),
    openPane: vi.fn(),
    placePane: vi.fn(),
    revealPane: vi.fn(() => true),
    paneTabAt: (id: string) => (panes.tabs?.includes(id) ? { gi: 0, tab: { id } } : null),
  };
  return ws as typeof ws & Workspace;
}

describe('the side bar, or what stands for it', () => {
  it('shows the lead and the agents in the side bar where there is one', () => {
    const ws = fakeWs();
    expect(noSideBar(ws)).toBe(false);
    showLead(ws);
    expect([ws.rightTab, ws.rightOpen]).toEqual(['lead', true]);
    showAgents(ws);
    expect(ws.rightTab).toBe('agents');
    expect(showPane(ws, 's_1')).toBe(true);
    expect(ws.revealPane).toHaveBeenCalledWith('s_1');
    placePaneSide(ws, 's_2');
    expect(ws.placePane).toHaveBeenCalledWith('s_2', 'side');
    expect(ws.openPane).not.toHaveBeenCalled();
  });

  it('on a phone: the sheets, and a pane of the side bar as a tab', () => {
    const ws = fakeWs({ tabs: ['s_tab'], side: ['s_side'] });
    const sheets = { lead: vi.fn(), agents: vi.fn() };
    const off = registerPhoneSheets(ws, sheets);
    expect(noSideBar(ws)).toBe(true);
    showLead(ws);
    showAgents(ws);
    expect(sheets.lead).toHaveBeenCalledTimes(1);
    expect(sheets.agents).toHaveBeenCalledTimes(1);
    // the side bar's own state is left alone
    expect([ws.rightTab, ws.rightOpen]).toEqual(['pane', false]);
    expect(showPane(ws, 's_side')).toBe(true);
    expect(showPane(ws, 's_tab')).toBe(true);
    expect(ws.openPane.mock.calls).toEqual([['s_side'], ['s_tab']]);
    expect(showPane(ws, 's_none')).toBe(false);
    expect(ws.revealPane).not.toHaveBeenCalled();
    placePaneSide(ws, 's_new');
    expect(ws.openPane).toHaveBeenLastCalledWith('s_new');
    expect(ws.placePane).not.toHaveBeenCalled();
    // the phone layout gone (a wider screen): the side bar again
    off();
    expect(noSideBar(ws)).toBe(false);
    showLead(ws);
    expect(sheets.lead).toHaveBeenCalledTimes(1);
    expect(ws.rightOpen).toBe(true);
  });
});
