import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { pickLayout } from './layout';

describe('pickLayout', () => {
  it('gives the owner’s devices the layout meant for them', () => {
    // Poco F5, either way up: the screen's short side decides, not the window
    expect(pickLayout(393, 393, true, 'auto')).toBe('phone');
    expect(pickLayout(873, 393, true, 'auto')).toBe('phone');
    // Xiaomi Pad 6 / Galaxy Tab S9, portrait and landscape, and with the keyboard open
    expect(pickLayout(800, 800, true, 'auto')).toBe('tablet');
    expect(pickLayout(1280, 800, true, 'auto')).toBe('tablet');
    // a PC
    expect(pickLayout(1500, 900, false, 'auto')).toBe('desktop');
  });

  it('follows the window on a computer', () => {
    expect(pickLayout(560, 900, false, 'auto')).toBe('phone');
    expect(pickLayout(1000, 900, false, 'auto')).toBe('tablet');
    expect(pickLayout(1200, 900, false, 'auto')).toBe('desktop');
    // a tablet split in half is a phone
    expect(pickLayout(500, 800, true, 'auto')).toBe('phone');
  });

  it('keeps what this browser was told', () => {
    expect(pickLayout(1280, 800, true, 'desktop')).toBe('desktop');
    expect(pickLayout(1500, 900, false, 'phone')).toBe('phone');
  });

  it('uses the same numbers as the script that runs before the first paint', () => {
    const boot = readFileSync(join(__dirname, '../../public/theme-boot.js'), 'utf8');
    expect(boot).toContain("w < 600 || (touch && short < 600) ? 'phone' : w < 1200 || touch ? 'tablet' : 'desktop'");
  });
});
