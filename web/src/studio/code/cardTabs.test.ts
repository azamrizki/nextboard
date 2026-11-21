import { describe, expect, it } from 'vitest';
import type { BoardNode } from '../../lib/types';
import { cardMeta, cardTitle, NEW_CARD_KINDS, opensAsTab, rememberCard, rememberCards } from './cardTabs';

const card = (id: string, kind: string, data: Record<string, unknown> = {}) => ({ id, kind, data }) as Pick<BoardNode, 'id' | 'kind' | 'data'>;

describe('board cards as tabs of the Code view', () => {
  it('opens the cards a tab can show, not consoles, groups or editors', () => {
    for (const k of ['kanban', 'checklist', 'calendar', 'orchestrator', 'memory', 'note']) expect(opensAsTab(k)).toBe(true);
    for (const k of ['terminal', 'group', 'editor', 'studio']) expect(opensAsTab(k)).toBe(false);
    for (const k of NEW_CARD_KINDS) expect(opensAsTab(k)).toBe(true);
  });

  it('titles a card by its own title, else what it is', () => {
    expect(cardTitle(card('1', 'kanban', { title: '  Sprint ' }))).toBe('Sprint');
    expect(cardTitle(card('2', 'kanban'))).toBe('Kanban');
    expect(cardTitle(card('3', 'note', { text: 'First line\nsecond' }))).toBe('First line');
    expect(cardTitle(card('4', 'mystery'))).toBe('Card');
  });

  it('keeps the titles of the open tabs as the board changes them', () => {
    expect(cardMeta('k1')).toEqual({ title: 'Card', icon: 'grid' });
    expect(rememberCard(card('k1', 'kanban'))).toBe(true);
    expect(rememberCard(card('k1', 'kanban'))).toBe(false);
    expect(cardMeta('k1')).toEqual({ title: 'Kanban', icon: 'kanban' });
    expect(rememberCards([card('k1', 'kanban', { title: 'Sprint' }), card('t1', 'terminal')])).toBe(true);
    expect(cardMeta('k1').title).toBe('Sprint');
    expect(cardMeta('t1').title).toBe('Card');
    expect(rememberCards([card('k1', 'kanban', { title: 'Sprint' })])).toBe(false);
  });
});
