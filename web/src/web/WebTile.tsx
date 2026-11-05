// Web tile (node kind "web", docs/API-studio.md §4, B17–B19): a localhost dev server on
// the project's machine or a site on the tailnet, framed through the hub's preview proxy. The injected bridge
// reports the page URL, title, console, errors and network calls (postMessage, only to
// this hub origin); the toolbar drives back / forward / reload / navigate through it.
// Empty tile = a dial of common dev ports (green dot when something answers). Any other
// address runs in the real browser of This PC (BrowserTile), since most sites refuse frames.

import { memo, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import type { NodeProps } from '@xyflow/react';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import type { MenuEntry } from '../components/Menu';
import { toast } from '../components/Toast';
import { newId } from '../lib/ids';
import type { FlowNode } from '../board/flow';
import { TileFrame, TileHead, stopKeys } from '../board/tiles/common';
import { useBoardEnv } from '../board/ui';
import { previewApi } from '../studio/api';
import { studioStore, usePreview, useStudio } from '../studio/store';
import type { PortProbe, Preview } from '../studio/types';
import { errorMessage } from '../api';
import { FillButton, type FillResult, type FillSink, type FillValues } from '../vault/FillDialog';
import { BrowserTile, requestBrowserOpen } from './BrowserTile';
import {
  DEVICES,
  DIAL_PORTS,
  MAX_TABS,
  ZOOM_MAX,
  ZOOM_MIN,
  displayUrl,
  externalUrl,
  localTarget,
  logCardFor,
  previewOrigin,
  readWeb,
  siteTarget,
  targetLabel,
  toDevUrl,
  type LocalTarget,
  type WebTab,
  rememberRealBrowser,
} from './model';

const RETRIES = 10;

const bareHost = (host?: string) => (host ?? '').replace(/^\[|\]$/g, '');

const onOrigin = (url: string, origin: string) => {
  try {
    return new URL(url).origin === origin;
  } catch {
    return false;
  }
};

interface BridgeMsg extends Partial<FillResult> {
  nb?: string;
  v?: number;
  type?: string;
  id?: string;
  url?: string;
  title?: string;
  level?: string;
}

/** A page that has not answered by then is not going to. */
const FILL_TIMEOUT_MS = 3000;

function WebTileImpl(props: NodeProps<FlowNode>) {
  const { url, tailnet } = readWeb(props.data.node);
  const external = url && !tailnet && !localTarget(url) ? externalUrl(url) : null;
  return external ? <BrowserTile {...props} url={external} /> : <DevServerTile {...props} />;
}

function DevServerTile({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl, project } = useBoardEnv();
  const node = data.node;
  const d = readWeb(node);
  const machineId = d.machineId || project.machineId;
  const target = useMemo(() => (d.tailnet ? siteTarget(d.url) : localTarget(d.url)), [d.url, d.tailnet]);
  const preview = usePreview(d.previewId || undefined);
  const previewsLoaded = useStudio((s) => s.previewsLoaded);
  const [launch, setLaunch] = useState<{ src: string; key: number } | null>(null);
  const [failed, setFailed] = useState('');
  const [address, setAddress] = useState(d.url);
  const [editing, setEditing] = useState(false);
  const [title, setTitle] = useState('');
  const [errors, setErrors] = useState(0);
  const [ready, setReady] = useState(false);
  const [blankHint, setBlankHint] = useState(false);
  const [attempt, setAttempt] = useState(0);
  const frameRef = useRef<HTMLIFrameElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const [box, setBox] = useState({ w: node.w, h: node.h - 80 });
  const creating = useRef(false);

  const patch = useCallback((set: Record<string, unknown>) => ctl.apply([{ op: 'patch', id, set: { data: set } }], { history: false }), [ctl, id]);

  useEffect(() => {
    if (!editing) setAddress(target ? displayUrl(target) : d.url);
  }, [d.url, target, editing]);

  // ---- make sure a preview exists for (machine, port) or the site ----
  const sameTarget = !!preview && !!target && preview.targetPort === target.port && (target.host ? bareHost(preview.targetHost) === bareHost(target.host) : !preview.targetHost && preview.machineId === machineId);
  const pv: Preview | undefined = sameTarget ? preview : undefined;
  useEffect(() => {
    if (!target || !previewsLoaded || pv || creating.current) return;
    creating.current = true;
    setFailed('');
    previewApi
      .create(target.host ? { url: d.url } : { machineId, port: target.port, scheme: target.scheme, path: target.path })
      .then((r) => {
        // the "preview" event (live once the hub's first probe answers) may arrive before
        // this response, which still says "waiting": never let the response win
        if (!studioStore.get().previews[r.preview.id]) studioStore.upsertPreview(r.preview);
        patch(target.host ? { previewId: r.preview.id } : { previewId: r.preview.id, machineId, port: target.port });
        setLaunch({ src: previewOrigin(r.preview.listenPort) + r.launchPath, key: Date.now() });
      })
      .catch((err) => setFailed(errorMessage(err)))
      .finally(() => {
        creating.current = false;
      });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- target comes from d.url
  }, [target, previewsLoaded, pv, machineId, patch]);

  const relaunch = useCallback(
    async (path?: string) => {
      if (!pv || !target) return;
      try {
        const r = await previewApi.launch(pv.id, path ?? target.path);
        setReady(false);
        setLaunch({ src: previewOrigin(pv.listenPort) + r.launchPath, key: Date.now() });
      } catch (err) {
        setFailed(errorMessage(err));
      }
    },
    [pv, target],
  );

  // first load of an existing preview (board reopened, other client created it)
  useEffect(() => {
    if (pv && !launch) void relaunch();
  }, [pv, launch, relaunch]);

  // ---- waiting for the dev server: 10 retries, 1 s apart ----
  const waiting = pv?.state === 'waiting';
  useEffect(() => {
    if (!waiting || !pv || !target) {
      setAttempt(0);
      return;
    }
    if (attempt >= RETRIES) return;
    const t = window.setTimeout(() => {
      previewApi
        .probe(pv.machineId, [target.port])
        .then((r: PortProbe[]) => {
          if (r[0]?.open) {
            void relaunch();
            void studioStore.loadPreviews();
          }
        })
        .catch(() => undefined)
        .finally(() => setAttempt((a) => a + 1));
    }, 1000);
    return () => window.clearTimeout(t);
  }, [waiting, attempt, pv, target, relaunch]);
  // the hub flips the preview to live when a request gets through
  const wasWaiting = useRef(false);
  useEffect(() => {
    if (wasWaiting.current && pv?.state === 'live') void relaunch();
    wasWaiting.current = pv?.state === 'waiting';
  }, [pv?.state, relaunch]);

  // ---- bridge ----
  const fillWaiting = useRef(new Map<string, (r: FillResult) => void>());
  const origin = pv ? previewOrigin(pv.listenPort) : '';
  const post = useCallback(
    (msg: Record<string, unknown>) => {
      const w = frameRef.current?.contentWindow;
      if (w && origin) w.postMessage({ nb: 'bridge', v: 1, ...msg }, origin);
    },
    [origin],
  );
  useEffect(() => {
    if (!pv || !target) return;
    const onMsg = (ev: MessageEvent) => {
      if (ev.source !== frameRef.current?.contentWindow || ev.origin !== origin) return;
      const m = ev.data as BridgeMsg;
      if (!m || m.nb !== 'bridge') return;
      switch (m.type) {
        case 'ready':
        case 'nav': {
          setReady(true);
          setBlankHint(false);
          if (m.title !== undefined) setTitle(m.title);
          // a site is not ours: it only gets to report addresses on its own preview origin
          if (m.url && (!target.host || onOrigin(m.url, origin))) {
            const dev = toDevUrl(m.url, pv.listenPort, pv.targetScheme || target.scheme, pv.targetPort, target.host);
            if (dev !== d.url) patch({ url: dev, ...(d.tabs.length ? { tabs: d.tabs.map((t) => (t.id === d.activeTab ? { ...t, url: dev, title: m.title ?? t.title } : t)) } : {}) });
          }
          break;
        }
        case 'filled':
          if (m.id) fillWaiting.current.get(m.id)?.({ username: m.username === true, password: m.password === true, code: m.code === true });
          break;
        case 'error':
          setErrors((n) => n + 1);
          break;
        case 'console':
          if (m.level === 'error') setErrors((n) => n + 1);
          break;
        case 'open': {
          const u = m.url ?? '';
          let same = false;
          try {
            same = new URL(u).origin === origin;
          } catch {
            same = false;
          }
          if (same) {
            addTab(toDevUrl(u, pv.listenPort, pv.targetScheme || target.scheme, pv.targetPort, target.host));
          } else if (/^https?:/i.test(u)) window.open(u, '_blank', 'noopener,noreferrer');
          break;
        }
      }
    };
    window.addEventListener('message', onMsg);
    return () => window.removeEventListener('message', onMsg);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pv, target, origin, d.url, d.tabs, d.activeTab, patch]);

  // The origin in post() keeps the values from any page but this preview's.
  const fillSink: FillSink | null = target && {
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

  const onFrameLoad = () => {
    post({ type: 'hello' });
    window.setTimeout(() => setBlankHint((b) => b || !readyRef.current), 4000);
  };
  const readyRef = useRef(ready);
  readyRef.current = ready;

  // ---- size: device frame scaled into the body ----
  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setBox({ w: el.clientWidth, h: el.clientHeight }));
    ro.observe(el);
    setBox({ w: el.clientWidth, h: el.clientHeight });
    return () => ro.disconnect();
  }, [pv, target]);
  const dev = DEVICES.find((x) => x.id === d.device) ?? DEVICES[0];
  const z = d.zoom / 100;
  const vw = dev.w || box.w;
  const vh = dev.h || box.h;
  const fitScale = dev.w ? Math.min(1, box.w / dev.w, box.h / dev.h) : 1;
  const frameStyle = dev.w
    ? { width: vw / z, height: vh / z, transform: `translateX(-50%) scale(${z * fitScale})`, transformOrigin: '50% 0' }
    : { width: vw / z, height: vh / z, transform: `scale(${z})` };

  // ---- navigation ----
  const go = (raw: string) => {
    setEditing(false);
    const t = localTarget(raw);
    if (t) {
      if (pv && target && !target.host && t.port === target.port) {
        if (ready) post({ type: 'navigate', url: t.path });
        else void relaunch(t.path);
        patch({ url: displayUrl(t) });
      } else {
        // another dev server: a new preview
        setLaunch(null);
        setReady(false);
        patch({ url: displayUrl(t), previewId: '', port: t.port, tailnet: false });
      }
      return;
    }
    const ext = externalUrl(raw);
    if (ext) {
      const site = target?.host ? siteTarget(ext) : null;
      if (pv && site && target && bareHost(site.host) === bareHost(target.host) && site.port === target.port) {
        // another page of the same site
        if (ready) post({ type: 'navigate', url: site.path });
        else void relaunch(site.path);
        patch({ url: displayUrl(site) });
        return;
      }
      // the tile finds out there whether this site loads through the proxy
      requestBrowserOpen(id);
      setLaunch(null);
      setReady(false);
      patch({ url: ext, previewId: '', port: 0, tabs: [], activeTab: '', tailnet: false, realBrowser: false });
    } else toast.error('Type a localhost address (localhost:5173) or a web address.');
  };

  const addTab = (url: string) => {
    const tabs = d.tabs.length ? d.tabs : [{ id: newId('i'), url: d.url, title }];
    if (tabs.length >= MAX_TABS) {
      toast.info(`A web tile holds up to ${MAX_TABS} tabs.`);
      return;
    }
    const tab: WebTab = { id: newId('i'), url, title: '' };
    switchTab([...tabs, tab], tab);
  };
  const switchTab = (tabs: WebTab[], tab: WebTab) => {
    setLaunch(null);
    setReady(false);
    const t = d.tailnet ? null : localTarget(tab.url);
    patch({ tabs, activeTab: tab.id, url: tab.url, ...(t && target && t.port !== target.port ? { previewId: '', port: t.port } : {}) });
  };
  const closeTab = (tab: WebTab) => {
    const rest = d.tabs.filter((t) => t.id !== tab.id);
    if (rest.length <= 1) {
      patch({ tabs: [], activeTab: '', url: rest[0]?.url ?? d.url });
      return;
    }
    if (tab.id === d.activeTab) switchTab(rest, rest[Math.max(0, d.tabs.findIndex((t) => t.id === tab.id) - 1)]);
    else patch({ tabs: rest });
  };

  const openNewTab = async () => {
    if (!pv || !target) return;
    // open synchronously (pop-up blockers), then point it at a fresh launch link
    const w = window.open('about:blank', '_blank');
    try {
      const r = await previewApi.launch(pv.id, target.path);
      if (w) {
        w.opener = null;
        w.location.href = previewOrigin(pv.listenPort) + r.launchPath;
      }
    } catch (err) {
      w?.close();
      toast.error(`Could not open the preview: ${errorMessage(err)}`);
    }
  };

  const setZoom = (v: number) => patch({ zoom: Math.max(ZOOM_MIN, Math.min(ZOOM_MAX, v)) });
  const logCard = () => {
    if (!pv) return;
    const nid = logCardFor(ctl, node, pv.id);
    ctl.select([nid]);
  };
  const openInBrowser = () => {
    if (target?.host) rememberRealBrowser(target.host, true);
    requestBrowserOpen(id);
    patch({ tailnet: false, realBrowser: true, previewId: '', tabs: [], activeTab: '' });
  };

  const menu: MenuEntry[] = [
    {
      type: 'stepper',
      label: 'Page zoom',
      value: `${d.zoom}%`,
      onDec: d.zoom > ZOOM_MIN ? () => setZoom(d.zoom - 10) : undefined,
      onInc: d.zoom < ZOOM_MAX ? () => setZoom(d.zoom + 10) : undefined,
      onReset: d.zoom !== 100 ? () => setZoom(100) : undefined,
      decLabel: 'Zoom out',
      incLabel: 'Zoom in',
      resetLabel: 'Reset zoom',
    },
    'sep',
    { label: 'Console log card', icon: 'braces', disabled: !pv, onSelect: logCard },
    { label: 'Hard reload', icon: 'refresh', disabled: !pv, onSelect: () => (ready ? post({ type: 'reload', hard: true }) : void relaunch()) },
    { label: 'Open in new tab', icon: 'external', disabled: !pv, onSelect: () => void openNewTab() },
    { label: 'Copy URL', icon: 'copy', disabled: !d.url, onSelect: () => void navigator.clipboard?.writeText(d.url).then(() => toast.success('URL copied.')) },
    ...(target?.host ? [{ label: 'Open in a real browser', icon: 'monitor', onSelect: openInBrowser } as const] : []),
    'sep',
    { label: 'Clear page', icon: 'close', disabled: !d.url, onSelect: () => patch({ url: '', previewId: '', tabs: [], activeTab: '', tailnet: false, realBrowser: false }) },
  ];

  const stateLabel = !target ? '' : failed ? 'Error' : waiting ? 'Waiting' : pv?.state === 'error' ? 'Error' : pv ? 'Live' : 'Starting';
  const headTitle = title || (target ? targetLabel(target) : 'Web page');

  return (
    <TileFrame id={id} kind="web" selected={!!selected} label={`Web tile ${headTitle}`} minW={320} minH={220}>
      <TileHead
        id={id}
        icon="globe"
        title={headTitle}
        meta={
          <>
            {stateLabel && (
              <span className={`web-state st-${stateLabel.toLowerCase()}`}>
                <span className="dot" aria-hidden="true" />
                {stateLabel}
              </span>
            )}
            {d.auto && <span className="badge web-auto">Auto</span>}
          </>
        }
        menu={menu}
        menuLabel="Web tile actions"
        showDisplay
      />
      {target && (
        <form
          className="web-toolbar nodrag"
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            go(address);
          }}
        >
          <Button size="sm" variant="ghost" iconOnly icon="arrowLeft" aria-label="Back" disabled={!ready} onClick={() => post({ type: 'back' })} />
          <Button size="sm" variant="ghost" iconOnly icon="arrowRight" aria-label="Forward" disabled={!ready} onClick={() => post({ type: 'forward' })} />
          <Button size="sm" variant="ghost" iconOnly icon="refresh" aria-label="Reload" disabled={!pv} onClick={() => (ready ? post({ type: 'reload' }) : void relaunch())} />
          <input
            className="control control-sm web-address mono"
            value={address}
            aria-label="Address"
            spellCheck={false}
            onFocus={(e) => {
              setEditing(true);
              e.currentTarget.select();
            }}
            onBlur={() => setEditing(false)}
            onChange={(e) => setAddress(e.target.value)}
            onKeyDown={(e) => {
              stopKeys(e);
              if (e.key === 'Escape') {
                setEditing(false);
                setAddress(target ? displayUrl(target) : d.url);
                e.currentTarget.blur();
              }
            }}
          />
          {d.zoom !== 100 && (
            <button type="button" className="badge web-zoom tabular" title="Reset page zoom to 100%" onClick={() => setZoom(100)}>
              {d.zoom}%
            </button>
          )}
          <button
            type="button"
            className="btn btn-ghost btn-sm btn-icon web-log-btn"
            aria-label={errors ? `Console log card (${errors} errors)` : 'Console log card'}
            title="Console log card"
            disabled={!pv}
            onClick={logCard}
          >
            <Icon name="braces" />
            {errors > 0 && <span className="web-err-count tabular">{errors > 99 ? '99+' : errors}</span>}
          </button>
          {fillSink && <FillButton sink={fillSink} disabled={!ready} />}
          {target?.host && <Button size="sm" variant="ghost" iconOnly icon="monitor" aria-label="Open in a real browser" title="Doesn’t work right? Open it in a real browser" onClick={openInBrowser} />}
          <Button size="sm" variant="ghost" iconOnly icon="external" aria-label="Open in new tab" title="Open in new tab" onClick={() => void openNewTab()} />
        </form>
      )}
      {d.tabs.length > 1 && (
        <div className="web-tabs nodrag" role="tablist" aria-label="Tabs">
          {d.tabs.map((t) => (
            <span key={t.id} className={`web-tab ${t.id === d.activeTab ? 'is-active' : ''}`}>
              <button type="button" role="tab" aria-selected={t.id === d.activeTab} className="web-tab-btn truncate" title={t.url} onClick={() => t.id !== d.activeTab && switchTab(d.tabs, t)}>
                {t.title || t.url.replace(/^https?:\/\//, '')}
              </button>
              <button type="button" className="web-tab-x" aria-label={`Close tab ${t.title || t.url}`} onClick={() => closeTab(t)}>
                <Icon name="close" />
              </button>
            </span>
          ))}
          {d.tabs.length < MAX_TABS && (
            <button type="button" className="web-tab-add" aria-label="New tab" onClick={() => addTab(target ? displayUrl({ ...target, path: '/' }) : d.url)}>
              <Icon name="plus" />
            </button>
          )}
        </div>
      )}
      <div className={`web-body ${selected ? 'is-live' : ''}`} ref={bodyRef}>
        {!d.url ? (
          <PortDial machineId={machineId} onPick={(u) => go(u)} />
        ) : failed ? (
          <div className="web-msg" role="alert">
            <Icon name="alertCircle" />
            <p>{failed}</p>
            <div className="row-gap wrap">
              <Button size="sm" icon="refresh" onClick={() => {
                setFailed('');
                setLaunch(null);
                patch({ previewId: '' });
              }}>
                Retry
              </Button>
              {target?.host && (
                <Button size="sm" icon="monitor" onClick={openInBrowser}>
                  Open in a real browser
                </Button>
              )}
            </div>
          </div>
        ) : (
          <>
            {launch && (
              <div className={`web-frame-wrap ${dev.w ? 'is-device' : ''}`}>
                <iframe
                  key={launch.key}
                  ref={frameRef}
                  className="web-frame"
                  src={launch.src}
                  title={headTitle}
                  style={frameStyle}
                  onLoad={onFrameLoad}
                  allow="clipboard-read; clipboard-write"
                  // dev apps need scripts, their own origin, forms and pop-ups; never top navigation
                  sandbox="allow-scripts allow-same-origin allow-forms allow-popups allow-popups-to-escape-sandbox allow-modals allow-downloads"
                  referrerPolicy="no-referrer"
                />
              </div>
            )}
            {!selected && <div className="web-shield" aria-hidden="true" />}
            {(!launch || !pv) && (
              <div className="web-msg" role="status">
                <span className="spinner" aria-hidden="true" />
                <p>Opening {target ? targetLabel(target) : 'the page'}…</p>
              </div>
            )}
            {waiting && (
              <div className="web-msg web-wait" role="status">
                {attempt < RETRIES ? <span className="spinner" aria-hidden="true" /> : <Icon name="clock" />}
                <p>
                  Waiting for the dev server on :{target?.port}
                  {attempt < RETRIES ? `… (try ${attempt + 1} of ${RETRIES})` : '. Nothing answers yet.'}
                </p>
                <div className="row-gap">
                  <Button size="sm" icon="refresh" onClick={() => {
                    setAttempt(0);
                    void relaunch();
                  }}>
                    Retry now
                  </Button>
                  <Button size="sm" variant="ghost" icon="external" onClick={() => void openNewTab()}>
                    Open in new tab
                  </Button>
                </div>
              </div>
            )}
            {pv?.state === 'error' && (
              <div className="web-msg" role="alert">
                <Icon name="alertCircle" />
                <p>{pv.error || 'The preview failed.'}</p>
                <div className="row-gap wrap">
                  <Button size="sm" icon="refresh" onClick={() => void relaunch()}>
                    Retry
                  </Button>
                  {target?.host && (
                    <Button size="sm" icon="monitor" onClick={openInBrowser}>
                      Open in a real browser
                    </Button>
                  )}
                </div>
              </div>
            )}
            {blankHint && !ready && !waiting && pv?.state === 'live' && (
              <div className="web-hint" role="note">
                <span>Blank page? Port {pv.listenPort} may be blocked, or your reverse proxy may not forward it yet.</span>
                <button type="button" className="link-btn" onClick={() => void openNewTab()}>
                  Open it in a new tab once
                </button>
                {target?.host && (
                  <button type="button" className="link-btn" onClick={openInBrowser}>
                    Open in a real browser
                  </button>
                )}
                <button type="button" className="web-hint-x" aria-label="Dismiss" onClick={() => setBlankHint(false)}>
                  <Icon name="close" />
                </button>
              </div>
            )}
          </>
        )}
      </div>
    </TileFrame>
  );
}

export const WebTile = memo(WebTileImpl);

/** Empty tile: dial of common dev ports on the project's machine + an address bar. */
function PortDial({ machineId, onPick }: { machineId: string; onPick: (url: string) => void }) {
  const [probe, setProbe] = useState<PortProbe[] | null>(null);
  const [addr, setAddr] = useState('');
  useEffect(() => {
    let stop = false;
    const run = () =>
      previewApi
        .probe(machineId, DIAL_PORTS)
        .then((r) => !stop && setProbe(r))
        .catch(() => !stop && setProbe([]));
    void run();
    const t = window.setInterval(run, 5000);
    return () => {
      stop = true;
      window.clearInterval(t);
    };
  }, [machineId]);
  const open = new Map((probe ?? []).map((p) => [p.port, p]));
  return (
    <div className="web-dial nodrag nowheel">
      <p className="web-dial-title">Open a dev server</p>
      <div className="web-ports" role="list">
        {DIAL_PORTS.map((p) => {
          const st = open.get(p);
          return (
            <button key={p} type="button" role="listitem" className={`web-port ${st?.open ? 'is-open' : ''}`} onClick={() => onPick(`http://localhost:${p}/`)} title={st?.open ? `Something answers on :${p}` : `Nothing on :${p} yet`}>
              <span className={`dot ${st?.open ? 'dot-success' : 'dot-grey'}`} aria-hidden="true" />
              <span className="tabular">:{p}</span>
              <span className="sr-only">{st?.open ? 'open' : 'closed'}</span>
            </button>
          );
        })}
      </div>
      <form
        className="web-dial-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (addr.trim()) onPick(addr.trim());
        }}
      >
        <input className="control control-sm mono" value={addr} placeholder="localhost:3000 or a web address" aria-label="Address" onChange={(e) => setAddr(e.target.value)} onKeyDown={stopKeys} />
        <Button size="sm" variant="primary" type="submit" disabled={!addr.trim()}>
          Open
        </Button>
      </form>
      <p className="fs-xs text-3">Pages from the project’s machine and other sites load through the hub, which stays light. A site that needs more opens in a real browser on This PC.</p>
    </div>
  );
}

export type { LocalTarget };
