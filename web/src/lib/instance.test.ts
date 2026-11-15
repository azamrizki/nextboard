import { describe, expect, it } from 'vitest';
import {
  guardHubStorage,
  HUB_ID_KEY,
  hostLabel,
  instanceNameError,
  pageTitle,
  portError,
  setInstance,
  setupLink,
  tunnelCommand,
} from './instance';

/** localStorage enough for the guard. */
function memoryStorage(init: Record<string, string> = {}, opts: { failSet?: boolean } = {}): Storage {
  const m = new Map(Object.entries(init));
  return {
    get length() {
      return m.size;
    },
    key: (i: number) => [...m.keys()][i] ?? null,
    getItem: (k: string) => m.get(k) ?? null,
    setItem: (k: string, v: string) => {
      if (opts.failSet) throw new Error('quota');
      m.set(k, v);
    },
    removeItem: (k: string) => void m.delete(k),
    clear: () => m.clear(),
  };
}

describe('titles', () => {
  it('end in a named instance’s hub name, and stay as they were for the default one', () => {
    setInstance('work', false, 'devbox · work');
    expect(pageTitle('Settings')).toBe('Settings — devbox · work');
    expect(pageTitle()).toBe('NextBoard — devbox · work');
    setInstance('work', false, '');
    expect(pageTitle('Settings')).toBe('Settings — NextBoard · work');
    expect(pageTitle()).toBe('NextBoard · work');
    setInstance('default', true, 'devbox');
    expect(pageTitle('Settings')).toBe('Settings — NextBoard');
    expect(pageTitle()).toBe('NextBoard');
    setInstance(undefined);
    expect(pageTitle('Board')).toBe('Board — NextBoard');
  });
});

describe('guardHubStorage', () => {
  it('keeps what a browser has when it sees its first hubId (an upgrade)', async () => {
    const s = memoryStorage({ 'nb.theme': 'light' });
    let dropped = 0;
    expect(await guardHubStorage('k7qxm2pa9d', s, async () => void dropped++)).toBe(false);
    expect(s.getItem('nb.theme')).toBe('light');
    expect(s.getItem(HUB_ID_KEY)).toBe('k7qxm2pa9d');
    expect(dropped).toBe(0);
  });

  it('keeps everything for the same hub', async () => {
    const s = memoryStorage({ [HUB_ID_KEY]: 'k7qxm2pa9d', 'nb.vault.pin': 'x' });
    expect(await guardHubStorage('k7qxm2pa9d', s, async () => undefined)).toBe(false);
    expect(s.getItem('nb.vault.pin')).toBe('x');
  });

  it('clears nb.* and the drafts database for another hub on the same origin', async () => {
    const s = memoryStorage({ [HUB_ID_KEY]: 'oldhub0001', 'nb.vault.pin': 'x', 'nb.theme': 'dark', other: 'kept' });
    const dropped: string[] = [];
    expect(await guardHubStorage('newhub0002', s, async (db) => void dropped.push(db))).toBe(true);
    expect(s.getItem('nb.vault.pin')).toBeNull();
    expect(s.getItem('nb.theme')).toBeNull();
    expect(s.getItem('other')).toBe('kept');
    expect(s.getItem(HUB_ID_KEY)).toBe('newhub0002');
    expect(dropped).toEqual(['nextboard']);
  });

  it('does nothing without a hubId, and never asks for a reload it cannot end', async () => {
    const s = memoryStorage({ [HUB_ID_KEY]: 'oldhub0001' });
    expect(await guardHubStorage(undefined, s, async () => undefined)).toBe(false);
    expect(await guardHubStorage('newhub0002', null)).toBe(false);
    const full = memoryStorage({ [HUB_ID_KEY]: 'oldhub0001' }, { failSet: true });
    expect(await guardHubStorage('newhub0002', full, async () => undefined)).toBe(false);
  });
});

describe('New instance checks', () => {
  it('names', () => {
    expect(instanceNameError('personal', ['default', 'work'])).toBe('');
    expect(instanceNameError('', [])).toMatch(/name/);
    expect(instanceNameError('Work', [])).toMatch(/Lowercase/);
    expect(instanceNameError('1st', [])).toMatch(/Lowercase/);
    expect(instanceNameError('a'.repeat(25), [])).toMatch(/at most 24/);
    expect(instanceNameError('default', [])).toMatch(/~\/\.nextboard/);
    expect(instanceNameError('work', ['default', 'work'])).toMatch(/already/);
  });

  it('ports', () => {
    expect(portError('12412')).toBe('');
    expect(portError('80')).not.toBe('');
    expect(portError('70000')).not.toBe('');
    expect(portError('12a')).not.toBe('');
    expect(portError('')).not.toBe('');
  });

  it('host labels', () => {
    expect(hostLabel('0.0.0.0')).toBe('every address, 0.0.0.0');
    expect(hostLabel('100.64.1.2')).toBe('100.64.1.2');
  });
});

describe('setupLink', () => {
  const here = (hostname: string) => ({ protocol: 'http:', hostname });
  const code = 'http://127.0.0.1:12412/setup?code=abc123';

  it('opens the address on this browser’s host, with from= when asked', () => {
    expect(setupLink([code], '127.0.0.1:12412', here('127.0.0.1'))).toBe(code);
    expect(setupLink([code], '127.0.0.1:12412', here('127.0.0.1'), 'work')).toBe(`${code}&from=work`);
    const lan = ['http://100.64.1.2:12412/setup?code=abc123', 'http://192.168.1.5:12412/setup?code=abc123'];
    expect(setupLink(lan, '0.0.0.0:12412', here('192.168.1.5'))).toBe(lan[1]);
  });

  it('makes one on this browser’s host when the new hub listens there too', () => {
    expect(setupLink([code], '127.0.0.1:12412', here('localhost'))).toBe('http://localhost:12412/setup?code=abc123');
    expect(setupLink(['http://100.64.1.2:12412/setup?code=abc123'], '0.0.0.0:12412', here('devbox.example.ts.net'))).toBe(
      'http://devbox.example.ts.net:12412/setup?code=abc123',
    );
  });

  it('says when this browser cannot reach a hub on loopback', () => {
    expect(setupLink([code], '127.0.0.1:12412', here('100.64.1.2'))).toBeNull();
    expect(setupLink([], '127.0.0.1:12412', here('127.0.0.1'))).toBeNull();
  });

  it('the tunnel line', () => {
    expect(tunnelCommand('127.0.0.1:12412', 'devbox')).toBe('ssh -L 12412:127.0.0.1:12412 devbox');
  });
});
