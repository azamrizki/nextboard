// Between the page and the extension (docs/API-vault.md, Passkey situs lain). The page can say
// anything here, which is why the origin is taken from the browser in background.js.
window.addEventListener('message', (e) => {
  if (e.source !== window || e.data?.nbPasskey !== 'ask') return;
  const { id, kind, options } = e.data;
  chrome.runtime.sendMessage({ kind, options }, (answer) => {
    const a = chrome.runtime.lastError ? { fallback: true } : answer ?? { fallback: true };
    window.postMessage({ nbPasskey: 'answer', id, answer: a }, location.origin);
  });
});
