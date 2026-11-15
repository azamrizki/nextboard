import { describe, expect, it } from 'vitest';
import { buildChannel, buildLabel, bundleScript, commitHash, commitUrl, isDirty, sameVersion, versionTag } from './build';

describe('build info', () => {
  it('labels a release, a beta and a dev build', () => {
    expect(buildLabel({ version: '0.9.0', commit: '3f2a1c9', channel: 'stable' })).toBe('v0.9.0 · 3f2a1c9');
    expect(buildLabel({ version: 'v0.9.0-beta.2', commit: '3f2a1c9', channel: 'beta' })).toBe('v0.9.0-beta.2 · 3f2a1c9');
    expect(buildLabel({ version: 'v0.8.1-7-g10ab3f5-dirty', commit: '10ab3f5-dirty', channel: 'dev' })).toBe('dev · 10ab3f5*');
    // no commit recorded (-buildvcs=false), and a hub too old to send one
    expect(buildLabel({ version: '0.1.0-dev', commit: 'unknown', channel: 'dev' })).toBe('dev');
    expect(buildLabel({ version: '0.8.1' })).toBe('v0.8.1');
    // a long hash is cut to seven
    expect(buildLabel({ version: '0.9.0', commit: '3f2a1c9d8e7f', channel: 'stable' })).toBe('v0.9.0 · 3f2a1c9');
  });

  it('tells the channel when an older hub does not', () => {
    expect(buildChannel({ version: '0.9.0' })).toBe('stable');
    expect(buildChannel({ version: 'v0.9.0-beta.2' })).toBe('beta');
    expect(buildChannel({ version: '0.9.0-rc.1' })).toBe('beta');
    expect(buildChannel({ version: '0.1.0-dev' })).toBe('dev');
    expect(buildChannel({ version: 'v0.9.0-beta.2-3-gabc1234' })).toBe('dev');
    expect(buildChannel({ version: '10ab3f5' })).toBe('dev');
    expect(buildChannel({ version: '0.9.0', channel: 'dev' })).toBe('dev');
  });

  it('links a commit only when it is one', () => {
    expect(commitUrl('example-owner/nextboard', '3f2a1c9')).toBe('https://github.com/example-owner/nextboard/commit/3f2a1c9');
    expect(commitUrl('example-owner/nextboard', '3F2A1C9-dirty')).toBe('https://github.com/example-owner/nextboard/commit/3f2a1c9');
    expect(commitUrl('example-owner/nextboard', 'unknown')).toBeNull();
    expect(commitUrl(undefined, '3f2a1c9')).toBeNull();
    expect(commitUrl('evil.example/x/../y', '3f2a1c9')).toBeNull();
    expect(commitHash('nothex1')).toBe('');
    expect(isDirty('3f2a1c9-dirty')).toBe(true);
    expect(isDirty('3f2a1c9')).toBe(false);
    expect(isDirty('unknown-dirty')).toBe(false);
  });

  it('compares versions with or without the v', () => {
    expect(versionTag('0.9.1')).toBe('v0.9.1');
    expect(versionTag('v0.9.1')).toBe('v0.9.1');
    expect(sameVersion('v0.9.1', '0.9.1')).toBe(true);
    expect(sameVersion('0.9.1', '0.9.10')).toBe(false);
    expect(sameVersion('', '')).toBe(false);
  });

  it('finds the bundle a page loads', () => {
    expect(bundleScript('<head><script type="module" crossorigin src="/assets/index-AbC123.js"></script>')).toBe('/assets/index-AbC123.js');
    expect(bundleScript('<script src="/assets/index-X.js" type="module"></script>')).toBe('/assets/index-X.js');
    expect(bundleScript('<p>no script</p>')).toBe('');
  });
});
