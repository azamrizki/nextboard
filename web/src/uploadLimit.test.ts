import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from './api';

// An upload a proxy in front of the hub refuses (nginx: 1 MB unless client_max_body_size says
// more) comes back as a 413 page, or as a broken connection when the proxy cuts it off: either
// way the message names the limit, not "Cannot reach the hub".

const file = (bytes: number) => new File([new Uint8Array(bytes)], 'telegram-cloud-photo-size-5-1-y.jpg', { type: 'image/jpeg' });

afterEach(() => vi.unstubAllGlobals());

describe('an upload a proxy refuses', () => {
  it('a connection cut off during a big upload names the proxy limit', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Load failed')));
    const err = await api.attachToSession('s_1', file(1_600_000)).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).code).toBe('upload_cut');
    expect((err as ApiError).message).toMatch(/1\.5 MB upload.*client_max_body_size 25m/);
  });

  it("a proxy's 413 page names it too; the hub's own 413 says the hub's words", async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response('<html>413 Request Entity Too Large</html>', { status: 413, statusText: 'Request Entity Too Large' })));
    const err = (await api.attachToSession('s_1', file(1_600_000)).catch((e: unknown) => e)) as ApiError;
    expect(err.status).toBe(413);
    expect(err.message).toMatch(/A proxy in front of the hub refused this 1\.5 MB upload/);
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: 'the file is larger than 20 MB' }), { status: 413 })));
    const own = (await api.attachToSession('s_1', file(1_600_000)).catch((e: unknown) => e)) as ApiError;
    expect(own.message).toBe('the file is larger than 20 MB');
  });

  it('a file the browser may not read says so, not network or proxy', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
    const unread = (fail: string, bytes: number) => {
      const f = file(bytes);
      f.slice = () => ({ arrayBuffer: () => Promise.reject(new DOMException('The requested file could not be read', fail)) }) as unknown as Blob;
      return f;
    };
    for (const bytes of [120_000, 1_600_000]) {
      const err = (await api.attachToSession('s_1', unread('NotReadableError', bytes)).catch((e: unknown) => e)) as ApiError;
      expect(err.code).toBe('upload_unreadable');
      expect(err.message).toMatch(/not able to read this file \(NotReadableError\).*Save it from that app first/);
    }
    const gone = (await api.attachToSession('s_1', unread('NotFoundError', 120_000)).catch((e: unknown) => e)) as ApiError;
    expect(gone.message).toMatch(/no longer there \(NotFoundError\)/);
  });

  it('a small upload, or no upload, is still a network error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
    expect(((await api.attachToSession('s_1', file(900_000)).catch((e: unknown) => e)) as ApiError).message).toBe('Cannot reach the hub (network error)');
    expect(((await api.hub().catch((e: unknown) => e)) as ApiError).message).toBe('Cannot reach the hub (network error)');
  });
});
