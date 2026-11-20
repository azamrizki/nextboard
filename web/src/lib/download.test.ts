import { describe, expect, it } from 'vitest';
import { downloadUrl } from './download';

describe('downloadUrl', () => {
  it('asks fs/raw for an attachment, the path escaped', () => {
    expect(downloadUrl('local', '/home/a/laporan akhir.md')).toBe('/api/machines/local/fs/raw?path=%2Fhome%2Fa%2Flaporan%20akhir.md&download=1');
  });
});
