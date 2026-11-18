// The Browser tab of the code view (docs/API-studio.md §7): an address bar over a page. A
// localhost dev server of the project's machine, or a site the hub can show, loads through the
// hub's preview proxy, as in a web tile; a site that needs more opens in the real browser of
// This PC, the managed browser, shown in the tab. The tab's argument is the page's address and
// is kept with the editor's tabs; the editor's openWeb(url, machine) lands here, the machine kept
// in the argument (toolsList.ts browserArg) so a server part's port opens from that server.

import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { errorMessage, isCode } from '../../../api';
import { Button } from '../../../components/Button';
import { Icon } from '../../../components/Icon';
import { toast } from '../../../components/Toast';
import { guiApi } from '../../../gui/api';
import { AppDisplay, AppHeadControls, useAppViewer } from '../../../gui/AppView';
import { guiStore, useGuiSession } from '../../../gui/store';
import { prefs } from '../../../lib/format';
import { newId } from '../../../lib/ids';
import { projectParts } from '../../../lib/parts';
import { FillButton, type FillResult, type FillSink, type FillValues } from '../../../vault/FillDialog';
import { displayUrl, externalUrl, localTarget, previewOrigin, prefersRealBrowser, rememberRealBrowser, siteTarget, targetLabel, toDevUrl, type LocalTarget } from '../../../web/model';
import { PortDial } from '../../../web/WebTile';
import { previewApi } from '../../api';
import { studioStore, usePreview } from '../../store';
import type { ToolBodyProps } from '../ToolTab';
import { browserArg, parseBrowserArg } from '../toolsList';
import type { Workspace } from '../workspace';

/** The hub cannot show these through its proxy: they are for the real browser (BrowserTile). */
const FOR_THE_BROWSER = ['private_address', 'no_such_host', 'needs_browser', 'bad_url', 'bad_path', 'bad_port'];
const RETRIES = 10;
const FILL_TIMEOUT_MS = 3000;

interface BridgeMsg extends Partial<FillResult> {
  nb?: string;
  type?: string;
  id?: string;
  url?: string;
  title?: string;
}

const bareHost = (host?: string) => (host ?? '').replace(/^\[|\]$/g, '');
const onOrigin = (url: string, origin: string) => {
  try {
    return new URL(url).origin === origin;
  } catch {
    return false;
  }
};

/** What the tab shows for an address: a dev server or a site through the hub, or nothing yet. */
function targetOf(url: string): { target: LocalTarget; site: boolean } | null {
  const local = localTarget(url);
  if (local) return { target: local, site: false };
  const ext = externalUrl(url);
  const site = ext ? siteTarget(ext) : null;
  return site ? { target: site, site: true } : null;
}

/** The real browser a tab shows, while this page lives (a tab keeps its id as its address changes). */
interface Saved {
  sessionId?: string;
  appId?: string;
}
const shownBrowsers = new WeakMap<Workspace, Map<string, Saved>>();
const savedOf = (ws: Workspace) => {
  let m = shownBrowsers.get(ws);
  if (!m) shownBrowsers.set(ws, (m = new Map()));
  return m;
};
/** What closed tabs left: a tab closed while it was not mounted goes at the next sweep. */
function sweep(ws: Workspace) {
  const open = new Set(ws.groups.flatMap((g) => g.tabs.map((t) => t.id)));
  const m = savedOf(ws);
  for (const id of [...m.keys()]) if (!open.has(id)) m.delete(id);
}
/** Whose localhost a project's browser tabs show, when its parts are on several machines. */
const machineKey = (projectId: string) => `wb.tool.browser.machine.${projectId}`;

export default function BrowserTool({ env, shown }: ToolBodyProps) {
  const { ws, tab, project } = env;
  // the address, and the machine the tab was opened for (a port of a server part), if any
  const { url, machineId: argMachine } = parseBrowserArg(tab.tool?.arg);
  const [saved, setSavedState] = useState<Saved>(() => savedOf(ws).get(tab.id) ?? {});
  const save = useCallback(
    (patch: Saved) =>
      setSavedState((cur) => {
        const next = { ...cur, ...patch };
        savedOf(ws).set(tab.id, next);
        return next;
      }),
    [ws, tab.id],
  );
  // this tab closed (it unmounts after it has left the groups), and any closed out of sight
  useEffect(() => {
    sweep(ws);
    return () => sweep(ws);
  }, [ws]);

  // localhost is a machine's: the one picked here, else the active file's part's, else the project's
  const parts = projectParts(project);
  const machines = [...new Set(parts.map((p) => p.machineId || 'local'))];
  const [picked, setPicked] = useState(() => prefs.get(machineKey(project.id)) ?? '');
  const pick = (id: string) => {
    prefs.set(machineKey(project.id), id);
    setPicked(id);
    // a tab opened for a machine keeps the one picked now
    if (argMachine) ws.setToolArg(tab.id, browserArg(url, id));
  };
  // the active file's part when the tab opened: switching files later does not move the page
  const [opened] = useState(() => ws.activePart?.machineId || project.machineId || 'local');
  const machineId = argMachine && machines.includes(argMachine) ? argMachine : picked && machines.includes(picked) ? picked : opened;

  const go = (u: string) => ws.setToolArg(tab.id, browserArg(u, argMachine));
  const t = useMemo(() => targetOf(url), [url]);
  const host = t?.site ? (t.target.host ?? '') : '';
  // the browser of This PC: what this tab shows already, a site the hub cannot show, or one moved there before
  const [needsBrowser, setNeedsBrowser] = useState<{ host: string; why: string } | null>(null);
  const why = needsBrowser && needsBrowser.host === host ? needsBrowser.why : '';
  const realBrowser = !!host && (!!saved.sessionId || !!why || prefersRealBrowser(host));

  if (!url)
    return (
      <div className="tool-browser">
        <AddressBar address="" onGo={go} />
        <div className="web-body">
          <PortDial
            machineId={machineId}
            onPick={go}
            parts={machines.length > 1 ? parts : undefined}
            partId={parts.find((p) => (p.machineId || 'local') === machineId)?.id ?? ''}
            onPart={(p) => pick(p.machineId || 'local')}
          />
        </div>
      </div>
    );
  if (realBrowser)
    return (
      <RealBrowser
        url={url}
        shown={shown}
        reason={why}
        saved={saved}
        save={save}
        onGo={go}
        onLight={() => {
          // back to the hub's proxy: the browser keeps running for its other tabs
          rememberRealBrowser(host, false);
          setNeedsBrowser(null);
          save({ sessionId: '', appId: '' });
        }}
      />
    );
  if (!t)
    return (
      <div className="tool-browser">
        <AddressBar address={url} onGo={go} />
        <div className="web-body">
          <div className="web-msg" role="alert">
            <Icon name="alertCircle" />
            <p>Type a localhost address (localhost:5173) or a web address.</p>
          </div>
        </div>
      </div>
    );
  return (
    <PreviewPage
      // a dev server of another machine or port, or another site: a page of its own
      key={`${t.site ? 'site' : machineId}:${t.target.host ?? ''}:${t.target.port}`}
      url={url}
      shown={shown}
      target={t.target}
      site={t.site}
      machineId={machineId}
      machines={machines.length > 1 ? parts : null}
      onMachine={pick}
      onGo={go}
      onBrowser={(reason) => {
        if (host) rememberRealBrowser(host, reason === 'browser');
        setNeedsBrowser({ host, why: reason });
      }}
    />
  );
}

/** Back, forward, reload, the address, and what goes with them. */
function AddressBar({
  address,
  onGo,
  ready,
  canReload,
  post,
  reload,
  extra,
}: {
  address: string;
  onGo: (url: string) => void;
  ready?: boolean;
  canReload?: boolean;
  post?: (msg: Record<string, unknown>) => void;
  reload?: () => void;
  extra?: ReactNode;
}) {
  const [text, setText] = useState(address);
  const [editing, setEditing] = useState(false);
  useEffect(() => {
    if (!editing) setText(address);
  }, [address, editing]);
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const raw = text.trim();
    setEditing(false);
    if (!raw) return;
    const next = localTarget(raw) ? displayUrl(localTarget(raw)!) : externalUrl(raw);
    if (next) onGo(next);
    else toast.error('Type a localhost address (localhost:5173) or a web address.');
  };
  return (
    <form className="web-toolbar tool-browser-bar" onSubmit={submit}>
      <Button size="sm" variant="ghost" iconOnly icon="arrowLeft" aria-label="Back" disabled={!ready} onClick={() => post?.({ type: 'back' })} />
      <Button size="sm" variant="ghost" iconOnly icon="arrowRight" aria-label="Forward" disabled={!ready} onClick={() => post?.({ type: 'forward' })} />
      <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Reload" disabled={!canReload} onClick={() => reload?.()} />
      <input
        className="control control-sm web-address mono"
        value={text}
        aria-label="Address"
        placeholder="localhost:3000 or a web address"
        spellCheck={false}
        onFocus={(e) => {
          setEditing(true);
          e.currentTarget.select();
        }}
        onBlur={() => setEditing(false)}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            setEditing(false);
            setText(address);
            e.currentTarget.blur();
          }
        }}
      />
      {extra}
    </form>
  );
}

/** A dev server or a site, through the hub's preview proxy and its bridge (docs/API-studio.md §4). */
function PreviewPage({
  url,
  shown,
  target,
  site,
  machineId,
  machines,
  onMachine,
  onGo,
  onBrowser,
}: {
  url: string;
  /** False while the tab is kept alive out of sight: the page stays loaded, nothing is polled. */
  shown: boolean;
  target: LocalTarget;
  site: boolean;
  machineId: string;
  machines: { id: string; name: string; machineId: string }[] | null;
  onMachine: (machineId: string) => void;
  onGo: (url: string) => void;
  onBrowser: (why: string) => void;
}) {
  const [pvId, setPvId] = useState('');
  const pv = usePreview(pvId || undefined);
  const [launch, setLaunch] = useState<{ src: string; key: number } | null>(null);
  const [failed, setFailed] = useState('');
  const [ready, setReady] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const frameRef = useRef<HTMLIFrameElement>(null);
  const first = useRef(url);
  const latest = useRef({ url, onGo });
  latest.current = { url, onGo };

  // one preview per (machine, port) or site: the hub gives the old one back with a new token
  const [nonce, setNonce] = useState(0);
  useEffect(() => {
    let alive = true;
    setFailed('');
    setReady(false);
    previewApi
      .create(site ? { url: first.current } : { machineId, port: target.port, scheme: target.scheme, path: target.path })
      .then((r) => {
        if (!alive) return;
        // the "preview" event may come before this response, which still says "waiting"
        if (!studioStore.get().previews[r.preview.id]) studioStore.upsertPreview(r.preview);
        setPvId(r.preview.id);
        setLaunch({ src: previewOrigin(r.preview.listenPort) + r.launchPath, key: Date.now() });
        if (site) {
          // the hub followed redirects: https, or the name with www
          const p = r.preview;
          const where = displayUrl({ scheme: p.targetScheme === 'https' ? 'https' : 'http', host: p.targetHost, port: p.targetPort, path: p.path });
          if (where !== latest.current.url) latest.current.onGo(where);
        }
      })
      .catch((err: unknown) => {
        if (!alive) return;
        if (site && FOR_THE_BROWSER.some((code) => isCode(err, code))) {
          if (isCode(err, 'needs_browser')) {
            const why = errorMessage(err).split('needs a real browser: ')[1];
            toast.info(`${target.host} doesn’t load through the hub${why ? ` (${why})` : ''}, so it opens in the real browser.`);
          }
          onBrowser(errorMessage(err));
          return;
        }
        setFailed(errorMessage(err));
      });
    return () => {
      alive = false;
    };
    // the page keeps its own address as it moves; the target is this component's key
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [nonce]);

  const relaunch = useCallback(
    async (path?: string) => {
      if (!pv) return;
      try {
        const r = await previewApi.launch(pv.id, path ?? target.path);
        setReady(false);
        setLaunch({ src: previewOrigin(pv.listenPort) + r.launchPath, key: Date.now() });
      } catch (err) {
        setFailed(errorMessage(err));
      }
    },
    [pv, target.path],
  );

  // waiting for the dev server: 10 tries, 1 s apart, then the hub's own flip to live; out of
  // sight the tries wait, and go on once the tab shows again
  const waiting = pv?.state === 'waiting';
  useEffect(() => {
    if (!waiting || !pv) {
      setAttempt(0);
      return;
    }
    if (attempt >= RETRIES || !shown) return;
    const timer = window.setTimeout(() => {
      previewApi
        .probe(pv.machineId, [target.port])
        .then((r) => {
          if (r[0]?.open) {
            void relaunch();
            void studioStore.loadPreviews();
          }
        })
        .catch(() => undefined)
        .finally(() => setAttempt((a) => a + 1));
    }, 1000);
    return () => window.clearTimeout(timer);
  }, [waiting, attempt, pv, target.port, relaunch, shown]);
  const wasWaiting = useRef(false);
  useEffect(() => {
    if (wasWaiting.current && pv?.state === 'live') void relaunch();
    wasWaiting.current = pv?.state === 'waiting';
  }, [pv?.state, relaunch]);

  // ---- bridge ----
  const origin = pv ? previewOrigin(pv.listenPort) : '';
  const post = useCallback(
    (msg: Record<string, unknown>) => {
      const w = frameRef.current?.contentWindow;
      if (w && origin) w.postMessage({ nb: 'bridge', v: 1, ...msg }, origin);
    },
    [origin],
  );
  const [title, setTitle] = useState('');
  const fillWaiting = useRef(new Map<string, (r: FillResult) => void>());
  useEffect(() => {
    if (!pv) return;
    const onMsg = (ev: MessageEvent) => {
      if (ev.source !== frameRef.current?.contentWindow || ev.origin !== origin) return;
      const m = ev.data as BridgeMsg;
      if (!m || m.nb !== 'bridge') return;
      if (m.type === 'ready' || m.type === 'nav') {
        setReady(true);
        if (m.title !== undefined) setTitle(m.title);
        // a site only gets to report addresses on its own preview origin
        if (m.url && (!target.host || onOrigin(m.url, origin))) {
          const dev = toDevUrl(m.url, pv.listenPort, pv.targetScheme || target.scheme, pv.targetPort, target.host);
          if (dev !== latest.current.url) latest.current.onGo(dev);
        }
      } else if (m.type === 'filled' && m.id) {
        fillWaiting.current.get(m.id)?.({ username: m.username === true, password: m.password === true, code: m.code === true, fields: typeof m.fields === 'number' ? m.fields : 0 });
      } else if (m.type === 'open' && m.url) {
        // a link to a new window: the same origin stays here, any other goes to a browser tab
        if (onOrigin(m.url, origin)) latest.current.onGo(toDevUrl(m.url, pv.listenPort, pv.targetScheme || target.scheme, pv.targetPort, target.host));
        else if (/^https?:/i.test(m.url)) window.open(m.url, '_blank', 'noopener,noreferrer');
      }
    };
    window.addEventListener('message', onMsg);
    return () => window.removeEventListener('message', onMsg);
  }, [pv, origin, target.host, target.scheme]);

  // typed into the address bar: the same server navigates in place, another one is another page
  const goTo = (next: string) => {
    const t = localTarget(next) ?? (site ? siteTarget(next) : null);
    const same = !!t && t.port === target.port && (site ? bareHost(t.host) === bareHost(target.host) : !t.host);
    if (same && pv) {
      if (ready) post({ type: 'navigate', url: t.path });
      else void relaunch(t.path);
    }
    onGo(next);
  };

  const openNewTab = async () => {
    if (!pv) return;
    // opened at once (pop-up blockers), then pointed at a fresh launch link
    const w = window.open('about:blank', '_blank');
    try {
      const r = await previewApi.launch(pv.id, localTarget(url)?.path ?? siteTarget(url)?.path ?? target.path);
      if (w) {
        w.opener = null;
        w.location.href = previewOrigin(pv.listenPort) + r.launchPath;
      }
    } catch (err) {
      w?.close();
      toast.error(`Could not open the preview: ${errorMessage(err)}`);
    }
  };

  // the Credentials fill: only into this preview's page, and only when picked
  const fillSink: FillSink = {
    mode: 'page',
    where: targetLabel(target),
    host: target.host ?? 'localhost',
    port: target.port,
    send: (values: FillValues) =>
      new Promise<FillResult>((resolve, reject) => {
        const fillId = newId('i');
        const timer = window.setTimeout(() => {
          fillWaiting.current.delete(fillId);
          reject(new Error('The page did not answer. Reload it and try again.'));
        }, FILL_TIMEOUT_MS);
        fillWaiting.current.set(fillId, (r) => {
          window.clearTimeout(timer);
          fillWaiting.current.delete(fillId);
          resolve(r);
        });
        post({ type: 'fill', id: fillId, ...values });
      }),
    onFilled: () => window.setTimeout(() => frameRef.current?.focus(), 0),
  };

  const state = failed || pv?.state === 'error' ? 'Error' : waiting ? 'Waiting' : pv?.state === 'live' ? 'Live' : 'Starting';
  return (
    <div className="tool-browser" data-state={state.toLowerCase()}>
      <AddressBar
        address={url}
        onGo={goTo}
        ready={ready}
        canReload={!!pv}
        post={post}
        reload={() => (ready ? post({ type: 'reload' }) : void relaunch())}
        extra={
          <>
            {machines && (
              <select className="control control-sm tool-browser-part" aria-label="Machine whose localhost to show" value={machineId} onChange={(e) => onMachine(e.target.value)} title="Whose localhost">
                {[...new Map(machines.map((p) => [p.machineId || 'local', p])).values()].map((p) => (
                  <option key={p.machineId || 'local'} value={p.machineId || 'local'}>
                    {p.name}
                  </option>
                ))}
              </select>
            )}
            <span className={`web-state st-${state.toLowerCase()}`} title={title || undefined}>
              <span className="dot" aria-hidden="true" />
              <span className="tool-browser-state">{state}</span>
            </span>
            <FillButton sink={fillSink} disabled={!ready} />
            {site && <Button size="sm" variant="ghost" iconOnly icon="monitor" aria-label="Open in the real browser" title="Doesn’t work right? Open it in the real browser of This PC" onClick={() => onBrowser('browser')} />}
            <Button size="sm" variant="ghost" iconOnly icon="external" aria-label="Open in a browser tab" title="Open in a browser tab" disabled={!pv} onClick={() => void openNewTab()} />
          </>
        }
      />
      <div className="web-body">
        {failed ? (
          <div className="web-msg" role="alert">
            <Icon name="alertCircle" />
            <p>{failed}</p>
            <Button
              size="sm"
              icon="refresh"
              onClick={() => {
                setLaunch(null);
                setNonce((n) => n + 1);
              }}
            >
              Retry
            </Button>
          </div>
        ) : (
          <>
            {launch && (
              <div className="web-frame-wrap">
                <iframe
                  key={launch.key}
                  ref={frameRef}
                  className="web-frame tool-browser-frame"
                  src={launch.src}
                  title={title || targetLabel(target)}
                  onLoad={() => post({ type: 'hello' })}
                  allow="clipboard-read; clipboard-write"
                  // dev apps need scripts, their own origin, forms and pop-ups; never top navigation
                  sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads"
                  referrerPolicy="no-referrer"
                />
              </div>
            )}
            {(!launch || !pv) && (
              <div className="web-msg" role="status">
                <span className="spinner" aria-hidden="true" />
                <p>Opening {targetLabel(target)}…</p>
              </div>
            )}
            {waiting && (
              <div className="web-msg web-wait" role="status">
                {attempt < RETRIES ? <span className="spinner" aria-hidden="true" /> : <Icon name="clock" />}
                <p>
                  Waiting for the dev server on :{target.port}
                  {attempt < RETRIES ? `… (try ${attempt + 1} of ${RETRIES})` : '. Nothing answers yet.'}
                </p>
                <Button
                  size="sm"
                  icon="refresh"
                  onClick={() => {
                    setAttempt(0);
                    void relaunch();
                  }}
                >
                  Retry now
                </Button>
              </div>
            )}
            {pv?.state === 'error' && (
              <div className="web-msg" role="alert">
                <Icon name="alertCircle" />
                <p>{pv.error || 'The preview failed.'}</p>
                <Button size="sm" icon="refresh" onClick={() => void relaunch()}>
                  Retry
                </Button>
              </div>
            )}
          </>
        )}
      </div>
    </div>
  );
}

/** A site in the real browser of This PC (docs/API-gui.md §8): a tab there, shown in this tab. */
function RealBrowser({
  url,
  shown,
  reason,
  saved,
  save,
  onGo,
  onLight,
}: {
  url: string;
  /** False while the tab is kept alive out of sight: the viewer goes (no stream), the browser runs on. */
  shown: boolean;
  reason: string;
  saved: Saved;
  save: (p: Saved) => void;
  onGo: (url: string) => void;
  onLight: () => void;
}) {
  const session = useGuiSession(saved.sessionId || undefined);
  const viewer = useAppViewer(saved.appId ?? '', session);
  const [opening, setOpening] = useState(false);
  const [failed, setFailed] = useState('');
  const bodyRef = useRef<HTMLDivElement>(null);
  let host = url;
  try {
    host = new URL(url).host;
  } catch {
    /* the address as typed */
  }
  const name = session?.name || 'Browser';

  const open = useCallback(async () => {
    setOpening(true);
    setFailed('');
    try {
      const box = bodyRef.current;
      // a tab of the browser that runs already, else a new one: the hub decides
      const r = await guiApi.openUrl({ url, width: Math.round(box?.clientWidth || 1200), height: Math.round(box?.clientHeight || 800) });
      if (!guiStore.get().sessions[r.session.id]) guiStore.upsert(r.session);
      save({ sessionId: r.session.id, appId: r.session.appId });
    } catch (err) {
      setFailed(errorMessage(err));
    } finally {
      setOpening(false);
    }
  }, [url, save]);
  // another address typed while the browser shows: it opens there as a tab
  const lastUrl = useRef(url);
  useEffect(() => {
    if (lastUrl.current === url) return;
    lastUrl.current = url;
    if (session) void open();
  }, [url, session, open]);

  const why = failed || (reason && reason !== 'browser' ? reason : '') || `${host} opens in the real browser of This PC.`;
  return (
    <div className="tool-browser is-real">
      <AddressBar
        address={url}
        onGo={(next) => {
          // a dev server goes back to the light view; another site opens as a tab of this browser
          if (localTarget(next)) save({ sessionId: '', appId: '' });
          onGo(next);
        }}
        extra={
          <>
            {session && <AppHeadControls viewer={viewer} session={session} name={name} />}
            <Button size="sm" variant="ghost" iconOnly icon="globe" aria-label="Try the light view" title="Load it through the hub again" onClick={onLight} />
            <Button size="sm" variant="ghost" iconOnly icon="external" aria-label="Open in a browser tab" title="Open in a browser tab" onClick={() => window.open(url, '_blank', 'noopener,noreferrer')} />
          </>
        }
      />
      <div className="web-body" ref={bodyRef}>
        {shown && <AppDisplay viewer={viewer} session={session} title={session?.title || host} name={name} selected icon={<Icon name="globe" />} />}
        {!session && (
          <div className="web-msg" role={failed ? 'alert' : undefined}>
            <Icon name="globe" />
            <p>{why}</p>
            <div className="row-gap wrap">
              <Button size="sm" variant="primary" icon="play" loading={opening} onClick={() => void open()}>
                Open here
              </Button>
              <Button size="sm" icon="external" onClick={() => window.open(url, '_blank', 'noopener,noreferrer')}>
                Browser tab
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
