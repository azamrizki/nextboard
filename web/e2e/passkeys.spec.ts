import { createHash, createPublicKey, createVerify } from 'node:crypto';
import { mkdtempSync, rmSync } from 'node:fs';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { join } from 'node:path';
import { chromium } from '@playwright/test';
import { expect, test } from './hub';
import { MASTER, openVault } from './vault';

const buf = (s: string) => Buffer.from(s, 'base64url');
const sha = (b: Buffer) => createHash('sha256').update(b).digest();

// The whole road with real parts: Chromium loads the extension the hub wrote, a page on a secure
// origin calls the real navigator.credentials, the owner answers in another browser, and what the
// page receives is checked the way a website's server checks it.
test('a site keeps its passkey in the vault and signs in with it', async ({ page, hub, browserName }) => {
  test.skip(browserName !== 'chromium');
  test.slow();
  const site = createServer((_, res) => {
    res.setHeader('content-type', 'text/html');
    res.end('<!doctype html><title>site</title><p>site</p>');
  });
  await new Promise<void>((r) => site.listen(0, '127.0.0.1', r));
  const port = (site.address() as AddressInfo).port;
  const origin = `http://localhost:${port}`;

  await page.goto('/');
  await hub.call('PUT', '/api/vault/passkeys', { enabled: true });
  const { path } = await hub.call('GET', '/api/vault/passkeys');
  expect(path).toBeTruthy();

  const profile = mkdtempSync(join(process.env.TMPDIR ?? '/tmp', 'nb-pk-'));
  const browser = await chromium.launchPersistentContext(profile, {
    executablePath: chromium.executablePath(),
    headless: true,
    args: [`--load-extension=${path}`, `--disable-extensions-except=${path}`],
    ignoreDefaultArgs: ['--disable-extensions', '--enable-automation', '--disable-component-extensions-with-background-pages'],
  });
  try {
    const tab = await browser.newPage();
    await tab.goto(`${origin}/`);
    // the extension says it runs
    await expect.poll(async () => (await hub.call('GET', '/api/vault/passkeys')).active, { timeout: 15_000 }).toBe(true);

    const challenge = Buffer.from('a-challenge-from-the-site').toString('base64url');
    const made = tab.evaluate(async (c) => {
      const b = (s: string) => Uint8Array.from(atob(s.replaceAll('-', '+').replaceAll('_', '/')), (x) => x.charCodeAt(0));
      const e = (v: ArrayBuffer | null) => (v ? btoa(String.fromCharCode(...new Uint8Array(v))).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '') : null);
      const cred = (await navigator.credentials.create({
        publicKey: {
          rp: { name: 'Demo site' },
          user: { id: new TextEncoder().encode('user-1'), name: 'alice@example.com', displayName: 'Alice' },
          challenge: b(c),
          pubKeyCredParams: [{ alg: -7, type: 'public-key' }],
        },
      })) as PublicKeyCredential;
      const r = cred.response as AuthenticatorAttestationResponse;
      return {
        isCredential: cred instanceof PublicKeyCredential,
        isAttestation: r instanceof AuthenticatorAttestationResponse,
        id: cred.id,
        rawId: e(cred.rawId),
        type: cred.type,
        clientData: e(r.clientDataJSON),
        attestation: e(r.attestationObject),
        auth: e(r.getAuthenticatorData()),
        publicKey: e(r.getPublicKey()),
        alg: r.getPublicKeyAlgorithm(),
        json: JSON.stringify(cred),
      };
    }, challenge);

    // the owner, in their own browser
    const ask = page.getByTestId('passkey-request');
    await expect(ask).toContainText(origin, { timeout: 15_000 });
    await expect(ask).toContainText('alice@example.com');
    await page.getByRole('heading', { name: /Save a passkey for localhost/ }).waitFor();
    // the vault may need making or opening first, right in this dialog
    const save = page.getByRole('button', { name: 'Save in vault' });
    const create = ask.getByRole('button', { name: 'Create vault' });
    const unlock = ask.getByRole('button', { name: 'Unlock', exact: true });
    await expect(save.or(create).or(unlock)).toBeVisible({ timeout: 15_000 });
    if (!(await save.isVisible())) {
      await ask.getByLabel('Master password', { exact: true }).fill(MASTER);
      if (await create.isVisible()) {
        await ask.getByLabel('Type it again').fill(MASTER);
        await ask.getByLabel('I understand it cannot be recovered').check();
        await create.click();
      } else await unlock.click();
    }
    await page.getByRole('button', { name: 'Save in vault' }).click({ timeout: 30_000 });
    const c = await made;
    await expect(ask).toHaveCount(0, { timeout: 10_000 });

    expect(c.isCredential).toBe(true);
    expect(c.isAttestation).toBe(true);
    expect(c.type).toBe('public-key');
    expect(c.rawId).toBe(c.id);
    expect(c.alg).toBe(-7);
    expect(JSON.parse(c.json).response.publicKey).toBe(c.publicKey);
    const cd = JSON.parse(buf(c.clientData!).toString());
    expect(cd).toMatchObject({ type: 'webauthn.create', challenge, origin, crossOrigin: false });
    const auth = buf(c.auth!);
    expect(auth.subarray(0, 32).equals(sha(Buffer.from('localhost')))).toBe(true);

    // the vault holds it encrypted, and the hub cannot read it
    const raw = await hub.call('GET', '/api/vault');
    expect(JSON.stringify(raw)).not.toContain(c.id);

    // and now a sign-in
    const signChallenge = Buffer.from('another-challenge').toString('base64url');
    const signed = tab.evaluate(
      async ({ c: ch, id }) => {
        const b = (s: string) => Uint8Array.from(atob(s.replaceAll('-', '+').replaceAll('_', '/')), (x) => x.charCodeAt(0));
        const e = (v: ArrayBuffer | null) => (v ? btoa(String.fromCharCode(...new Uint8Array(v))).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '') : null);
        const cred = (await navigator.credentials.get({ publicKey: { challenge: b(ch), allowCredentials: [{ id: b(id), type: 'public-key' }] } })) as PublicKeyCredential;
        const r = cred.response as AuthenticatorAssertionResponse;
        return { isAssertion: r instanceof AuthenticatorAssertionResponse, id: cred.id, clientData: e(r.clientDataJSON), auth: e(r.authenticatorData), signature: e(r.signature), user: e(r.userHandle) };
      },
      { c: signChallenge, id: c.id },
    );
    await page.getByRole('button', { name: 'Sign in as alice@example.com' }).click({ timeout: 15_000 });
    const s = await signed;
    expect(s.isAssertion).toBe(true);
    expect(s.id).toBe(c.id);
    expect(buf(s.user!).toString()).toBe('user-1');
    const cd2 = JSON.parse(buf(s.clientData!).toString());
    expect(cd2).toMatchObject({ type: 'webauthn.get', challenge: signChallenge, origin });

    // what a website's server does with it
    const key = createPublicKey({ key: buf(c.publicKey!), format: 'der', type: 'spki' });
    const good = createVerify('SHA256').update(buf(s.auth!)).update(sha(buf(s.clientData!))).verify(key, buf(s.signature!));
    expect(good).toBe(true);

    // refused: the page gets the error a browser gives
    const refused = tab.evaluate(async () => {
      try {
        await navigator.credentials.get({ publicKey: { challenge: new Uint8Array(16) } });
        return 'no error';
      } catch (e) {
        return (e as DOMException).name;
      }
    });
    await page.getByRole('button', { name: 'Refuse' }).click({ timeout: 15_000 });
    expect(await refused).toBe('NotAllowedError');

    // a page cannot speak for another site: the origin comes from the browser
    const last = await hub.call('GET', '/api/vault/passkeys');
    expect(last.requests ?? []).toEqual([]);
  } finally {
    await browser.close();
    site.close();
    rmSync(profile, { recursive: true, force: true });
    await hub.call('PUT', '/api/vault/passkeys', { enabled: false });
  }
});

// Turning it on says what is left to do, and turning it off ends it.
test('passkeys for sites are turned on from the vault menu', async ({ page, hub }, info) => {
  const p = await hub.project(`Passkeys ${info.project.name}`);
  await page.goto(`/p/${p.id}/w/${p.wid}`);
  await page.getByRole('button', { name: /^Credentials/ }).click();
  const win = page.getByRole('dialog', { name: 'Credentials' });
  await expect(win).toBeVisible();
  await openVault(page);
  await win.getByRole('button', { name: 'Vault actions' }).click();
  await page.getByRole('menuitem', { name: 'Passkeys for sites…' }).click();
  const box = page.getByTestId('site-passkeys');
  await expect(box.getByRole('checkbox')).not.toBeChecked({ timeout: 10_000 });
  // it changes once the hub has answered
  await box.getByRole('checkbox').click();
  await expect(box.getByRole('checkbox')).toBeChecked({ timeout: 10_000 });
  await expect(box.getByText('Developer mode', { exact: false })).toBeVisible({ timeout: 10_000 });
  const status = await hub.call('GET', '/api/vault/passkeys');
  await expect(box).toContainText(status.path);
  expect(status.enabled).toBe(true);
  await box.getByRole('checkbox').click();
  await expect(box.getByText('Developer mode', { exact: false })).toHaveCount(0, { timeout: 10_000 });
  expect((await hub.call('GET', '/api/vault/passkeys')).enabled).toBe(false);
});
