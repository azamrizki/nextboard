// Extensions view: search Open VSX through the hub, install, uninstall, pick a theme. What
// an extension brings here is its data: themes, icons, snippets, grammars, languages. For some
// the editor itself does the main work, and those say so here.

import { useEffect, useState } from 'react';
import { errorMessage, isCode } from '../../api';
import { Button } from '../../components/Button';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { extApi, type ExtensionHit, type InstalledExtension } from '../api';
import { applyExtension, applyTheme, loadExtensions, refreshGrammars, removeExtension, savedThemeKey, useBuiltinTheme } from './extensions';
import { activeIconTheme, applyIconTheme, useDefaultIcons, useIconTheme } from './iconTheme';
import { useWs } from './useWs';
import type { Workspace } from './workspace';

const plural = (n: number, one: string) => `${n} ${one}${n === 1 ? '' : 's'}`;

function summary(x: InstalledExtension) {
  const c = x.contributes;
  const parts = [];
  if (c.themes?.length) parts.push(plural(c.themes.length, 'theme'));
  if (c.iconThemes?.length) parts.push(plural(c.iconThemes.length, 'icon theme'));
  if (c.snippets?.length) parts.push(plural(c.snippets.length, 'snippet file'));
  if (c.grammars?.length) parts.push(plural(c.grammars.length, 'grammar'));
  return parts.join(' · ');
}

/** Longer than the hub's own 90 s limit: this one catches a request that never got through. */
const INSTALL_TIMEOUT_MS = 120_000;

const NATIVE_TEXT: Record<string, string> = {
  prettier: 'Prettier formats supported files with Format Document and on save rules, using this project’s JSON, YAML, TOML and .editorconfig settings.',
  eslint: 'ESLint problems and quick fixes come from the project’s own eslint, once allowed for the project. See the status bar.',
  gitlens: 'Blame on the current line and above each file; file blame and file or line history are in the editor menu and the command palette.',
  errorlens: 'Problems are shown at the end of their line.',
  vim: 'Vim keys are on in every editor, with :w, :q and search. Uninstall to turn them off.',
};

/** A language server's state, for an extension that relies on one. */
function ServerRow({ ws, id, onInstall }: { ws: Workspace; id: string; onInstall: (command: string) => void }) {
  const s = ws.lsp.servers.find((x) => x.id === id);
  if (!s) return <p className="wb-ext-meta">Checking this machine for its language server…</p>;
  if (s.available) return <p className="wb-ext-meta">IntelliSense comes from {s.name} on this machine.</p>;
  return (
    <p className="wb-ext-meta wb-ext-server">
      <span className="grow">IntelliSense needs {s.name}, which this machine does not have.</span>
      <Button size="sm" variant="ghost" title={s.install} onClick={() => onInstall(s.install)}>
        Install…
      </Button>
    </p>
  );
}

const downloads = (n: number) => (n >= 1e6 ? `${(n / 1e6).toFixed(1)}M` : n >= 1e3 ? `${Math.round(n / 1e3)}K` : String(n));

export function ExtensionsPanel({ ws, onInstall }: { ws: Workspace; onInstall: (command: string) => void }) {
  useWs(ws);
  useIconTheme();
  const [query, setQuery] = useState('');
  const [hits, setHits] = useState<ExtensionHit[] | null>(null);
  const [left, setLeft] = useState({ hidden: 0, unchecked: 0 });
  const [installed, setInstalled] = useState<InstalledExtension[]>([]);
  const [busy, setBusy] = useState('');
  const [searching, setSearching] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    void extApi
      .list()
      .then((r) => setInstalled(r.items ?? []))
      .catch(() => undefined);
  }, []);

  useEffect(() => {
    const q = query.trim();
    if (!q) {
      setHits(null);
      setError('');
      return;
    }
    let stop = false;
    // each search reads many manifests: an older one stops when the text changes
    const ctl = new AbortController();
    const t = window.setTimeout(() => {
      setSearching(true);
      extApi
        .search(q, ctl.signal)
        .then((r) => {
          if (stop) return;
          setHits(r.items ?? []);
          setLeft({ hidden: r.hidden ?? 0, unchecked: r.unchecked ?? 0 });
          setError('');
        })
        .catch((err) => !stop && setError(errorMessage(err)))
        .finally(() => !stop && setSearching(false));
    }, 350);
    return () => {
      stop = true;
      ctl.abort();
      window.clearTimeout(t);
    };
  }, [query]);

  /** Colours and grammars load behind the button: a slow one must not hold it. */
  const activate = async (x: InstalledExtension, list: InstalledExtension[]) => {
    try {
      await applyExtension(x);
      await refreshGrammars(list);
      const theme = x.contributes.themes?.[0];
      if (theme) await applyTheme(x, theme.path);
      const icons = x.contributes.iconThemes?.[0];
      if (icons) await applyIconTheme(x, icons.path);
    } catch (err) {
      toast.error(`${x.displayName || x.name} is installed, but could not be applied: ${errorMessage(err)}`);
    }
    ws.changed();
  };

  const install = async (h: ExtensionHit) => {
    const id = `${h.namespace}.${h.name}`;
    const label = h.displayName || h.name;
    setBusy(id);
    const ctl = new AbortController();
    const timer = window.setTimeout(() => ctl.abort(), INSTALL_TIMEOUT_MS);
    try {
      const x = await extApi.install(h.namespace, h.name, ctl.signal);
      const next = [...installed.filter((i) => i.id !== x.id), x].sort((a, b) => a.id.localeCompare(b.id));
      setInstalled(next);
      toast.success(`${x.displayName || x.name} installed.${summary(x) ? ` ${summary(x)}.` : ''}`);
      void activate(x, next);
    } catch (err) {
      if (ctl.signal.aborted) toast.error(`${label}: no answer from the hub after 2 minutes. Requests may be waiting on a slow machine; try again when the file tree has loaded.`);
      else toast.error(isCode(err, 'extension_code_only') ? `${label} only has code, which cannot run here. Themes, snippets and grammars can.` : `Could not install ${label}: ${errorMessage(err)}`);
    } finally {
      window.clearTimeout(timer);
      setBusy('');
    }
  };

  const uninstall = async (x: InstalledExtension) => {
    setBusy(x.id);
    try {
      await extApi.remove(x.id);
      removeExtension(x.id);
      const next = installed.filter((i) => i.id !== x.id);
      setInstalled(next);
      await refreshGrammars(next);
      if (savedThemeKey().includes(`"ext":"${x.id}"`)) useBuiltinTheme();
      if (activeIconTheme()?.ext === x.id) useDefaultIcons();
      ws.changed();
    } catch (err) {
      toast.error(`Could not uninstall: ${errorMessage(err)}`);
    } finally {
      setBusy('');
    }
  };

  const isInstalled = (h: ExtensionHit) => installed.some((i) => i.id === `${h.namespace}.${h.name}`);

  return (
    <div className="wb-view">
      <header className="wb-view-head">
        <h2>Extensions</h2>
        <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Reload extensions" title="Reload extensions" onClick={() => void loadExtensions().then(setInstalled)} />
      </header>
      <div className="wb-search-form">
        <div className="wb-search-input">
          <input aria-label="Search Open VSX" placeholder="Search Open VSX: dracula, snippets, toml" spellCheck={false} value={query} onChange={(e) => setQuery(e.target.value)} />
        </div>
      </div>
      <div className="wb-ext-list nowheel">
        {searching && <p className="wb-row-msg">Searching…</p>}
        {error && <p className="wb-row-msg is-error">{error}</p>}
        {hits?.map((h) => {
          const id = `${h.namespace}.${h.name}`;
          return (
            <article key={id} className="wb-ext">
              <div className="wb-ext-head">
                <strong className="truncate">{h.displayName || h.name}</strong>
                {isInstalled(h) ? (
                  <span className="wb-ext-tag">Installed</span>
                ) : (
                  <Button size="sm" loading={busy === id} disabled={!!busy} onClick={() => void install(h)}>
                    {busy === id ? 'Installing…' : 'Install'}
                  </Button>
                )}
              </div>
              <p className="clamp-2">{h.description}</p>
              {h.hasCode && !h.native && <p className="wb-ext-meta"><Icon name="info" /> Only its {h.gives?.join(', ')} will work here: its code does not run.</p>}
              {h.native && <p className="wb-ext-meta"><Icon name="check" /> The editor does its main work itself: its own code does not run.</p>}
              <p className="wb-ext-meta">
                {h.namespace}
                {h.downloadCount ? ` · ${downloads(h.downloadCount)} downloads` : ''}
              </p>
            </article>
          );
        })}
        {hits && !hits.length && !searching && !left.hidden && !left.unchecked && <p className="wb-row-msg">Nothing found.</p>}
        {hits && !searching && left.hidden > 0 && <p className="wb-row-msg">{left.hidden === 1 ? '1 result is' : `${left.hidden} results are`} not listed: their main work is in code, which cannot run in this editor.</p>}
        {hits && !searching && left.unchecked > 0 && <p className="wb-row-msg is-error">{left.unchecked === 1 ? '1 result' : `${left.unchecked} results`} could not be checked. Search again to retry.</p>}
        {!hits && (
          <>
            <div className="wb-section-title">
              <span>Installed</span>
              <span className="wb-result-count tabular">{installed.length}</span>
            </div>
            {installed.map((x) => (
              <article key={x.id} className="wb-ext">
                <div className="wb-ext-head">
                  <strong className="truncate">{x.displayName || x.name}</strong>
                  <Button size="sm" variant="ghost" iconOnly icon="trash" aria-label={`Uninstall ${x.displayName || x.name}`} title="Uninstall" loading={busy === x.id} disabled={!!busy} onClick={() => void uninstall(x)} />
                </div>
                <p className="wb-ext-meta">{[summary(x), x.version].filter(Boolean).join(' · ')}</p>
                {x.native && !x.native.startsWith('lsp:') && <p className="wb-ext-meta">{NATIVE_TEXT[x.native]}</p>}
                {x.native?.startsWith('lsp:') && <ServerRow ws={ws} id={x.native.slice(4)} onInstall={onInstall} />}
                {x.hasCode && (
                  <p className="wb-ext-meta">
                    <Icon name="info" /> {x.native ? 'Its own code does not run here: the editor does this work itself.' : 'Its code does not run here, only the parts above.'}
                  </p>
                )}
                {x.contributes.themes?.map((t) => (
                  <Button key={t.path} size="sm" variant="ghost" icon="palette" onClick={() => void applyTheme(x, t.path).catch((err) => toast.error(errorMessage(err)))}>
                    {t.label}
                  </Button>
                ))}
                {x.contributes.iconThemes?.map((t) => (
                  <Button key={t.path} size="sm" variant="ghost" icon="file" onClick={() => void applyIconTheme(x, t.path).catch((err) => toast.error(errorMessage(err)))}>
                    {t.label}
                  </Button>
                ))}
              </article>
            ))}
            {!installed.length && <p className="wb-row-msg">Search above for colour and icon themes, snippets and syntax highlighting. An extension’s code cannot run here: there is no extension host in the editor. Vim, Prettier, ESLint, GitLens, Error Lens and the Python, Go, Rust, Tailwind, Vue and TOML extensions work through the editor itself.</p>}
            {installed.some((x) => x.contributes.themes?.length) && (
              <Button size="sm" variant="ghost" icon="palette" className="wb-ext-default" onClick={() => useBuiltinTheme()}>
                Back to the NextBoard theme
              </Button>
            )}
            {activeIconTheme() && (
              <Button size="sm" variant="ghost" icon="file" className="wb-ext-default" onClick={() => useDefaultIcons()}>
                Back to the plain icons
              </Button>
            )}
          </>
        )}
      </div>
    </div>
  );
}
