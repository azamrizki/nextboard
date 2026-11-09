// The trusted side of the extension. sender.origin comes from the browser, not from the page: a
// page cannot ask for a passkey in the name of another site.

async function config() {
  const r = await fetch(chrome.runtime.getURL('config.json'), { cache: 'no-store' });
  return r.json();
}

// The hub says when this extension starts, so that its panel can stop asking to install it.
async function hello() {
  try {
    const c = await config();
    await fetch(`${c.hub}/nb/passkey/hello`, { headers: { Authorization: `Bearer ${c.token}` } });
  } catch {
    /* the hub is not there now */
  }
}
chrome.runtime.onInstalled.addListener(hello);
chrome.runtime.onStartup.addListener(hello);
hello();

chrome.runtime.onMessage.addListener((msg, sender, reply) => {
  ask(msg, sender).then(reply, () => reply({ fallback: true }));
  return true; // answers later
});

async function ask(msg, sender) {
  if (!sender.origin || !sender.tab?.url) return { fallback: true };
  // A frame of another site inside this page: left to the browser's own authenticator.
  if (new URL(sender.tab.url).origin !== sender.origin) return { fallback: true };
  const c = await config();
  const r = await fetch(`${c.hub}/nb/passkey`, {
    method: 'POST',
    headers: { Authorization: `Bearer ${c.token}`, 'Content-Type': 'application/json' },
    body: JSON.stringify({ kind: msg.kind, origin: sender.origin, options: msg.options }),
  });
  if (!r.ok) return { fallback: true };
  return r.json();
}
