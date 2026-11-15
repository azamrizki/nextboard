import { describe, expect, it } from 'vitest';
import { clickSelect, NO_SELECTION, pickedInOrder, pruneSelection, toggleAllVisible, visibleState, type Selection } from './selection';

const order = ['a', 'b', 'c', 'd', 'e', 'f'];
const sel = (ids: string[], anchor: string | null = null): Selection => ({ ids: new Set(ids), anchor });
const ids = (s: Selection) => [...s.ids].sort();

describe('picking several things in a list', () => {
  it('a click toggles one and makes it the anchor', () => {
    let s = clickSelect(NO_SELECTION, 'b', order);
    expect(ids(s)).toEqual(['b']);
    expect(s.anchor).toBe('b');
    s = clickSelect(s, 'd', order);
    expect(ids(s)).toEqual(['b', 'd']);
    s = clickSelect(s, 'b', order);
    expect(ids(s)).toEqual(['d']);
    expect(s.anchor).toBe('b');
  });

  it('Ctrl or ⌘ toggles one, as a click does, and leaves the others', () => {
    const s = clickSelect(sel(['a', 'c'], 'a'), 'c', order, { toggle: true });
    expect(ids(s)).toEqual(['a']);
    expect(s.anchor).toBe('c');
  });

  it('Shift adds the run from the anchor, downwards and upwards, keeping the anchor', () => {
    let s = clickSelect(NO_SELECTION, 'b', order);
    s = clickSelect(s, 'e', order, { shift: true });
    expect(ids(s)).toEqual(['b', 'c', 'd', 'e']);
    expect(s.anchor).toBe('b');
    // the other way, from the same anchor: added to what is there
    s = clickSelect(s, 'a', order, { shift: true });
    expect(ids(s)).toEqual(['a', 'b', 'c', 'd', 'e']);
  });

  it('Shift adds to what was picked before, it does not replace it', () => {
    const s = clickSelect(sel(['f', 'b'], 'b'), 'c', order, { shift: true });
    expect(ids(s)).toEqual(['b', 'c', 'f']);
  });

  it('Shift without an anchor, or with one no longer shown, toggles just the one', () => {
    expect(ids(clickSelect(NO_SELECTION, 'c', order, { shift: true }))).toEqual(['c']);
    const s = clickSelect(sel(['z'], 'z'), 'c', order, { shift: true });
    expect(ids(s)).toEqual(['c', 'z']);
    expect(s.anchor).toBe('c');
  });

  it('skips what may not be picked, in a click, a run and select-all', () => {
    const open = (id: string) => id !== 'c';
    expect(clickSelect(NO_SELECTION, 'c', order, {}, open)).toBe(NO_SELECTION);
    const run = clickSelect(sel(['a'], 'a'), 'e', order, { shift: true }, open);
    expect(ids(run)).toEqual(['a', 'b', 'd', 'e']);
    expect(ids(toggleAllVisible(NO_SELECTION, order, open))).toEqual(['a', 'b', 'd', 'e', 'f']);
    expect(visibleState(sel(['a', 'b', 'd', 'e', 'f']), order, open)).toBe('all');
  });
});

describe('select all of what is shown', () => {
  it('takes every one shown, and only those', () => {
    const shown = ['b', 'd'];
    const s = toggleAllVisible(NO_SELECTION, shown);
    expect(ids(s)).toEqual(['b', 'd']);
    expect(visibleState(s, shown)).toBe('all');
    expect(visibleState(s, order)).toBe('some');
  });

  it('keeps what was picked outside the filter, and a second press clears only the shown ones', () => {
    const shown = ['b', 'd'];
    let s = toggleAllVisible(sel(['f']), shown);
    expect(ids(s)).toEqual(['b', 'd', 'f']);
    s = toggleAllVisible(s, shown);
    expect(ids(s)).toEqual(['f']);
  });

  it('with some of them picked, picks the rest', () => {
    const s = toggleAllVisible(sel(['a']), ['a', 'b', 'c']);
    expect(ids(s)).toEqual(['a', 'b', 'c']);
    expect(visibleState(NO_SELECTION, ['a'])).toBe('none');
  });
});

describe('keeping the selection true to the list', () => {
  it('drops what is gone or no longer pickable, and the anchor with it', () => {
    const s = pruneSelection(sel(['a', 'b', 'x'], 'x'), ['a', 'b', 'c'], (id) => id !== 'b');
    expect(ids(s)).toEqual(['a']);
    expect(s.anchor).toBeNull();
  });

  it('gives back the same selection when nothing changed, so nothing re-renders', () => {
    const s = sel(['a'], 'a');
    expect(pruneSelection(s, order)).toBe(s);
  });

  it('lists what is picked in the order it is shown', () => {
    expect(pickedInOrder(sel(['e', 'a', 'c']), order)).toEqual(['a', 'c', 'e']);
  });
});
