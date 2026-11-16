// @vitest-environment jsdom
import { describe, expect, it } from 'vitest';
import { attentionLine, claimLine, joinNames, lineLang, pickVoice, sayIn, speakable, type VoiceLike } from './speech';

describe('spoken lines', () => {
  it('says a burst in one sentence, in English or Indonesian', () => {
    expect(attentionLine('finished', ['Arkan'], 'en')).toBe('Arkan finished.');
    expect(attentionLine('needsYou', ['Arkan'], 'en')).toBe('Arkan needs you.');
    expect(attentionLine('needsYou', ['Arkan', 'Bintang'], 'en')).toBe('Arkan and Bintang need you.');
    expect(attentionLine('finished', ['Arkan', 'Bintang', 'Cantika'], 'id')).toBe('Arkan, Bintang dan Cantika selesai.');
    expect(attentionLine('needsYou', ['Arkan'], 'id')).toBe('Arkan butuh kamu.');
    expect(joinNames(['Arkan', 'Bintang', 'Cantika', 'Daffa', 'Elvan'], 'en')).toBe('Arkan, Bintang and 3 others');
    expect(joinNames(['Arkan', 'Arkan'], 'en')).toBe('Arkan');
    expect(joinNames([], 'id')).toBe('Seorang agen');
  });

  it('picks the line language and the language it is said in', () => {
    expect(lineLang('id-ID')).toBe('id');
    expect(lineLang('de-DE')).toBe('en');
    expect(sayIn('en-GB', 'en')).toBe('en-GB');
    expect(sayIn('de-DE', 'en')).toBe('en-US');
    expect(sayIn('id-ID', 'en')).toBe('en-US');
  });

  it('reads a hub sentence without its marks, ids and paths', () => {
    expect(speakable('Delete from the board: **Notes** n_ab12cd34ef?')).toBe('Delete from the board: Notes?');
    expect(speakable('Run `make test` in /home/someone/src/shop/api?')).toBe('Run make test in api?');
    expect(speakable('See https://example.com/x for more.')).toBe('See a link for more.');
    const first = 'Stop the three consoles in the backend group and start the tests again.';
    expect(speakable(`${first} ${'word '.repeat(60)}`)).toBe(first);
    expect(speakable('word '.repeat(60)).endsWith('…')).toBe(true);
  });

  it('prefers the chosen voice, then a smooth one of this device in that language', () => {
    const voices: VoiceLike[] = [
      { voiceURI: 'g-id', name: 'Google Bahasa Indonesia', lang: 'id-ID', localService: false },
      { voiceURI: 'l-id', name: 'Damayanti', lang: 'id-ID', localService: true },
      { voiceURI: 'l-en', name: 'Samantha', lang: 'en-US', localService: true },
      { voiceURI: 'n-en', name: 'Microsoft Aria Online (Natural)', lang: 'en-US', localService: false },
      { voiceURI: 'l-en2', name: 'Ava (Premium)', lang: 'en-US', localService: true },
    ];
    expect(pickVoice(voices, 'id-ID', '')?.voiceURI).toBe('l-id');
    expect(pickVoice(voices, 'id-ID', 'g-id')?.voiceURI).toBe('g-id');
    // a voice chosen for Indonesian does not read an English line
    expect(pickVoice(voices, 'en-US', 'l-id')?.voiceURI).toBe('l-en2');
    expect(pickVoice(voices, 'ja-JP', '')).toBeUndefined();
  });
});

describe('one tab talks', () => {
  const now = () => Promise.resolve();
  it('lets the first tab claim a line, and no other tab says it again', async () => {
    window.localStorage.clear();
    expect(await claimLine('finished.a1', true, now)).toBe(true);
    expect(await claimLine('finished.a1', false, now)).toBe(false);
    expect(await claimLine('finished.a1', true, now)).toBe(false);
    // another line is free
    expect(await claimLine('needsYou.b2', false, now)).toBe(true);
  });
});
