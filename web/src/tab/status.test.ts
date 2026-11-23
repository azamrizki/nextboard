// @vitest-environment jsdom
import { afterEach, describe, expect, it } from 'vitest';
import { setPageTitle, setTitleStatus } from '../lib/title';
import { faviconSvg, statusText, type TabFacts } from './status';

const at = (f: Partial<TabFacts>): TabFacts => ({ needsYou: 0, idle: 0, working: 0, usage: null, offline: false, ...f });

describe('the tab title', () => {
  it('says first what needs you, then the idle agents, then the usage', () => {
    expect(statusText(at({ needsYou: 1, idle: 2, working: 1, usage: { pct: 45, resets: 'resets 3pm' } }), 'all')).toBe('1 needs you · 2 idle · 45%');
    expect(statusText(at({ needsYou: 2 }), 'all')).toBe('2 need you');
  });

  it('names working agents only when none waits for you', () => {
    expect(statusText(at({ working: 3, usage: { pct: 45, resets: '' } }), 'all')).toBe('3 working · 45%');
    expect(statusText(at({ idle: 1, working: 3 }), 'all')).toBe('1 idle');
  });

  it('adds the reset time once the window is used up', () => {
    expect(statusText(at({ idle: 2, usage: { pct: 100, resets: 'resets 1h 20m' } }), 'all')).toBe('2 idle · 100% · resets 1h 20m');
    expect(statusText(at({ idle: 2, usage: { pct: 99, resets: 'resets 1h 20m' } }), 'all')).toBe('2 idle · 99%');
  });

  it('leaves the usage out when asked for the agents only, and says nothing when asked for nothing', () => {
    const f = at({ idle: 2, usage: { pct: 45, resets: '' } });
    expect(statusText(f, 'agents')).toBe('2 idle');
    expect(statusText(f, 'none')).toBe('');
    expect(statusText(at({}), 'all')).toBe('');
  });

  it('says reconnecting, not counts that may be old, while live updates are lost', () => {
    expect(statusText(at({ idle: 2, offline: true }), 'all')).toBe('Reconnecting…');
  });
});

describe('the page title and the status', () => {
  afterEach(() => setTitleStatus(''));

  it('keeps the page part when the status changes and the status when the page does', () => {
    setPageTitle('nextboard — NextBoard');
    setTitleStatus('2 idle · 45%');
    expect(document.title).toBe('2 idle · 45% · nextboard — NextBoard');
    setPageTitle('Settings — NextBoard');
    expect(document.title).toBe('2 idle · 45% · Settings — NextBoard');
    setTitleStatus('');
    expect(document.title).toBe('Settings — NextBoard');
  });
});

describe('the tab icon', () => {
  const block = (svg: string) => /x='17' y='7' width='10' height='8' rx='2' fill='(#[0-9a-f]+)'/.exec(svg)?.[1];
  const bars = (svg: string) => [...svg.matchAll(/x='5' y='17' width='(\d+)' height='8' rx='2' fill='(#[0-9a-f]+)'/g)].map((m) => [Number(m[1]), m[2]]);

  it('lights its top-right block amber for needs you over green for idle over blue for working', () => {
    expect(block(faviconSvg(at({ needsYou: 1, idle: 2, working: 1 })))).toBe('#fbbf24');
    expect(block(faviconSvg(at({ idle: 2, working: 1 })))).toBe('#4ade80');
    expect(block(faviconSvg(at({ working: 1 })))).toBe('#60a5fa');
    expect(block(faviconSvg(at({})))).toBe('#8c8c8c');
  });

  it('fills its bottom bar to the usage, amber from 75% and red from 90%', () => {
    expect(bars(faviconSvg(at({ usage: { pct: 45, resets: '' } })))).toEqual([
      [22, '#3a3a3a'],
      [10, '#f5f5f5'],
    ]);
    expect(bars(faviconSvg(at({ usage: { pct: 82, resets: '' } })))[1]).toEqual([18, '#fbbf24']);
    expect(bars(faviconSvg(at({ usage: { pct: 100, resets: '' } })))[1]).toEqual([22, '#f87171']);
    // a little usage still shows: an empty track would read as no reading
    expect(bars(faviconSvg(at({ usage: { pct: 1, resets: '' } })))[1][0]).toBe(2);
  });

  it('is the plain mark with nothing to say, and fades while live updates are lost', () => {
    expect(bars(faviconSvg(at({})))).toEqual([[22, '#f5f5f5']]);
    expect(faviconSvg(at({ idle: 2, offline: true }))).toContain("<g opacity='.38'>");
  });
});
