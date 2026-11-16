import { describe, expect, it } from 'vitest';
import { renameTo } from './renameDraft';

describe('what leaving a rename field saves', () => {
  it('nothing when it was not typed in, even if the name changed elsewhere meanwhile', () => {
    // F2 on "Main", renamed to "API" in another tab, Enter here: "Main" must not come back
    expect(renameTo(null, 'API')).toBeNull();
  });

  it('nothing for an empty name or the name it has now', () => {
    expect(renameTo('   ', 'Main')).toBeNull();
    expect(renameTo(' Main ', 'Main')).toBeNull();
  });

  it('the typed name, trimmed and cut to the longest allowed', () => {
    expect(renameTo('  Backend ', 'Main')).toBe('Backend');
    expect(renameTo('A very long workspace name indeed', 'Main', 24)).toBe('A very long workspace na');
  });

  it('a typed name that is the old one, after a rename elsewhere, is a rename back on purpose', () => {
    expect(renameTo('Main', 'API')).toBe('Main');
  });
});
