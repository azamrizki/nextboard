import { describe, expect, it } from 'vitest';
import { LEAD_LANGUAGES, leadLanguageOptions } from './lead';

describe('the lead’s language', () => {
  it('offers automatic first, then Bahasa Indonesia and English', () => {
    expect(LEAD_LANGUAGES.map((o) => o.value)).toEqual(['', 'id', 'en']);
    expect(LEAD_LANGUAGES[0].label).toBe('Automatic (follow my messages)');
    expect(leadLanguageOptions(undefined)).toBe(LEAD_LANGUAGES);
    expect(leadLanguageOptions('id')).toBe(LEAD_LANGUAGES);
  });

  it('keeps a value set elsewhere that the list lacks, so the select does not lie', () => {
    const opts = leadLanguageOptions('fr');
    expect(opts.map((o) => o.value)).toEqual(['', 'id', 'en', 'fr']);
    expect(opts[3].label).toBe('Other (fr)');
    expect(leadLanguageOptions('  ')).toBe(LEAD_LANGUAGES);
  });
});
