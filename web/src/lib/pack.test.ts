import { describe, expect, it } from 'vitest';
import { PACK_FROM, packUpload, worthPacking } from './pack';

const unpack = async (b: Blob) => new Uint8Array(await new Response(b.stream().pipeThrough(new DecompressionStream('gzip'))).arrayBuffer());

describe('packUpload', () => {
  it('gzips text, and what unpacks is the file', async () => {
    const text = 'func main() { println("hello") }\n'.repeat(5000);
    const file = new File([text], 'main.go', { type: 'text/plain' });
    const p = await packUpload(file);
    expect(p.encoding).toBe('gzip');
    expect(p.size).toBe(file.size);
    expect(p.body.size).toBeLessThan(file.size / 10);
    expect(new TextDecoder().decode(await unpack(p.body))).toBe(text);
  });

  it('sends small files, compressed formats and what does not shrink as they are', async () => {
    const small = new File(['x'.repeat(PACK_FROM - 1)], 'a.txt');
    expect((await packUpload(small)).encoding).toBeUndefined();

    const big = 'a'.repeat(PACK_FROM * 4);
    for (const f of [new File([big], 'shot.PNG'), new File([big], 'x', { type: 'image/jpeg' }), new File([big], 'v.mp4'), new File([big], 'a.zip'), new File([big], 'f.woff2')]) {
      expect(worthPacking(f)).toBe(false);
      expect((await packUpload(f)).body).toBe(f);
    }
    expect(worthPacking(new File([big], 'icon.svg', { type: 'image/svg+xml' }))).toBe(true);
    expect(worthPacking(new File([big], 'dump.sql'))).toBe(true);

    const noise = new Uint8Array(PACK_FROM * 4);
    crypto.getRandomValues(noise);
    const random = new File([noise], 'blob.bin');
    const p = await packUpload(random);
    expect(p.encoding).toBeUndefined();
    expect(p.body).toBe(random);
  });
});
