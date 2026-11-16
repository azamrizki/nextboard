import { describe, expect, it } from 'vitest';
import { archiveTarget } from './archive';

const projects = [
  { id: 'p_first', name: 'Docs' },
  { id: 'p_shop', name: 'Storefront' },
  { id: 'p_other', name: 'Shop' },
];

describe('the project a board restore offers', () => {
  it('is the board’s own project by id, even renamed and with another taking its old name', () => {
    expect(archiveTarget({ projectId: 'p_shop', projectName: 'Shop' }, projects)).toBe('p_shop');
  });

  it('is one of the same name for archives from before they kept the id, or when the project is gone', () => {
    expect(archiveTarget({ projectName: 'Shop' }, projects)).toBe('p_other');
    expect(archiveTarget({ projectId: 'p_gone', projectName: 'Shop' }, projects)).toBe('p_other');
  });

  it('is the first project when nothing matches, and none without projects', () => {
    expect(archiveTarget({ projectId: 'p_gone', projectName: 'Gone' }, projects)).toBe('p_first');
    expect(archiveTarget({ projectName: 'Gone' }, [])).toBe('');
  });
});
