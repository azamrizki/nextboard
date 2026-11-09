// Runs in the page. It stands in for navigator.credentials on the calls a site makes for a passkey,
// and hands the page what a browser would have. Anything else goes to the browser's own.
(() => {
  const creds = navigator.credentials;
  if (!creds || !window.PublicKeyCredential) return;
  const nativeCreate = creds.create.bind(creds);
  const nativeGet = creds.get.bind(creds);
  const waiting = new Map();
  let seq = 0;

  window.addEventListener('message', (e) => {
    if (e.source !== window || e.data?.nbPasskey !== 'answer') return;
    const w = waiting.get(e.data.id);
    if (w) {
      waiting.delete(e.data.id);
      w(e.data.answer);
    }
  });

  const ask = (kind, options) =>
    new Promise((resolve) => {
      const id = ++seq;
      waiting.set(id, resolve);
      window.postMessage({ nbPasskey: 'ask', id, kind, options }, location.origin);
    });

  const toB64 = (v) => {
    const b = v instanceof ArrayBuffer ? new Uint8Array(v) : new Uint8Array(v.buffer, v.byteOffset, v.byteLength);
    let s = '';
    for (const x of b) s += String.fromCharCode(x);
    return btoa(s).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '');
  };
  const fromB64 = (s) => {
    const t = atob(s.replaceAll('-', '+').replaceAll('_', '/'));
    const b = new Uint8Array(t.length);
    for (let i = 0; i < t.length; i++) b[i] = t.charCodeAt(i);
    return b.buffer;
  };
  const list = (l) => (l ?? []).map((c) => ({ id: toB64(c.id), type: c.type, transports: c.transports }));

  const encodeCreate = (p) => ({
    rp: { id: p.rp?.id, name: p.rp?.name },
    user: { id: toB64(p.user.id), name: p.user.name, displayName: p.user.displayName },
    challenge: toB64(p.challenge),
    pubKeyCredParams: p.pubKeyCredParams,
    excludeCredentials: list(p.excludeCredentials),
  });
  const encodeGet = (p) => ({ rpId: p.rpId, challenge: toB64(p.challenge), allowCredentials: list(p.allowCredentials) });

  const define = (o, props) => {
    for (const [k, value] of Object.entries(props)) Object.defineProperty(o, k, { value, enumerable: true });
    return o;
  };

  function credential(r, response, json) {
    const c = Object.create(PublicKeyCredential.prototype);
    define(c, {
      id: r.id,
      rawId: fromB64(r.id),
      type: 'public-key',
      authenticatorAttachment: 'platform',
      response,
      getClientExtensionResults: () => ({}),
      toJSON: () => ({ id: r.id, rawId: r.id, type: 'public-key', authenticatorAttachment: 'platform', clientExtensionResults: {}, response: json }),
    });
    return c;
  }

  function attestation(r) {
    const response = define(Object.create(AuthenticatorAttestationResponse.prototype), {
      clientDataJSON: fromB64(r.clientDataJSON),
      attestationObject: fromB64(r.attestationObject),
      getAuthenticatorData: () => fromB64(r.authenticatorData),
      getPublicKey: () => fromB64(r.publicKey),
      getPublicKeyAlgorithm: () => r.publicKeyAlgorithm,
      getTransports: () => ['internal'],
    });
    return credential(r, response, {
      clientDataJSON: r.clientDataJSON,
      attestationObject: r.attestationObject,
      authenticatorData: r.authenticatorData,
      publicKey: r.publicKey,
      publicKeyAlgorithm: r.publicKeyAlgorithm,
      transports: ['internal'],
    });
  }

  function assertion(r) {
    const response = define(Object.create(AuthenticatorAssertionResponse.prototype), {
      clientDataJSON: fromB64(r.clientDataJSON),
      authenticatorData: fromB64(r.authenticatorData),
      signature: fromB64(r.signature),
      userHandle: r.userHandle ? fromB64(r.userHandle) : null,
    });
    return credential(r, response, {
      clientDataJSON: r.clientDataJSON,
      authenticatorData: r.authenticatorData,
      signature: r.signature,
      userHandle: r.userHandle ?? null,
    });
  }

  const refused = (a) => new DOMException(a.error?.message ?? 'The request was refused.', a.error?.name ?? 'NotAllowedError');

  creds.create = async function (options) {
    if (!options?.publicKey) return nativeCreate(options);
    const a = await ask('create', encodeCreate(options.publicKey));
    if (a.fallback) return nativeCreate(options);
    if (a.error) throw refused(a);
    return attestation(a.result);
  };

  creds.get = async function (options) {
    // Autofill has no way to show here: the browser keeps those, and a person can still pick us.
    if (!options?.publicKey || options.mediation === 'conditional') return nativeGet(options);
    const a = await ask('get', encodeGet(options.publicKey));
    if (a.fallback) return nativeGet(options);
    if (a.error) throw refused(a);
    return assertion(a.result);
  };

  PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable = async () => true;
})();
