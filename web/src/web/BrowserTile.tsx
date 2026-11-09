// A web tile on a site outside the project's machine. A site first tries the light way, through
// the hub's proxy (docs/API-studio.md §4.9). What that cannot show runs in the real browser of
// This PC (docs/API-gui.md §8), on the same virtual display and viewer as a desktop app tile. The
// browser brings its own address bar, tabs and logins. Another address on the board can become a
// tab there, which costs nothing, or a browser of this tile's own, with its own logins.

import { useCallback, useEffect, useState } from 'react';
import type { NodeProps } from '@xyflow/react';
import { errorMessage, isCode } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import type { MenuEntry } from '../components/Menu';
import { toast } from '../components/Toast';
import type { FlowNode } from '../board/flow';
import { TileFrame, TileHead } from '../board/tiles/common';
import { useBoardEnv } from '../board/ui';
import { guiApi } from '../gui/api';
import { AppIcon } from '../gui/AppIcon';
import { AppDisplay, AppHeadControls, AppWhere, useAppViewer } from '../gui/AppView';
import { guiStore, useGui, useGuiSession } from '../gui/store';
import { previewApi } from '../studio/api';
import { studioStore } from '../studio/store';
import { displayUrl, prefersRealBrowser, readWeb, rememberRealBrowser } from './model';

/** Height of the tile header (--h-node-header): the display gets the rest of the node. */
const HEAD_H = 38;

/**
 * Tiles whose address was just typed. Only those start the browser by themselves: a board
 * that merely reopens must not start one for every old address it holds.
 */
const openRequests = new Set<string>();
export const requestBrowserOpen = (nodeId: string) => openRequests.add(nodeId);

const hostOf = (url: string) => new URL(url).host;

/** The hub cannot show these through its proxy: they are for the browser. */
const FOR_THE_BROWSER = ['private_address', 'no_such_host', 'needs_browser', 'bad_url', 'bad_path', 'bad_port'];

export function BrowserTile({ id, data, selected, url }: NodeProps<FlowNode> & { url: string }) {
  const { ctl, frame } = useBoardEnv();
  const node = data.node;
  const d = readWeb(node);
  const session = useGuiSession(d.sessionId || undefined);
  const ended = useGui((s) => (d.sessionId ? s.ended[d.sessionId] : undefined));
  const loaded = useGui((s) => s.loaded);
  const [opening, setOpening] = useState(false);
  const [failed, setFailed] = useState('');
  /** Another tile shows this browser already, and this one has to say what it wants. */
  const [asking, setAsking] = useState('');
  const viewer = useAppViewer(d.appId, session);
  const host = hostOf(url);
  const name = d.name || 'Browser';

  const patch = useCallback((set: Record<string, unknown>) => ctl.apply([{ op: 'patch', id, set: { data: set } }], { history: false }), [ctl, id]);

  const open = useCallback(async (how: 'ask' | 'here' | 'tab') => {
    setOpening(true);
    setFailed('');
    setAsking('');
    try {
      if (!d.realBrowser && !prefersRealBrowser(host)) {
        // the light way first (docs/API-studio.md §4.9)
        const site = await previewApi.create({ url }).catch((err: unknown) => {
          if (isCode(err, 'needs_browser')) {
            // the hub says why: a bot check, a sign-in elsewhere, a redirect it cannot follow
            const why = errorMessage(err).split('needs a real browser: ')[1];
            toast.info(`${host} doesn’t load through the hub${why ? ` (${why})` : ''}, so it opens in the real browser.`);
          }
          if (FOR_THE_BROWSER.some((code) => isCode(err, code))) return null;
          throw err;
        });
        if (site) {
          if (!studioStore.get().previews[site.preview.id]) studioStore.upsertPreview(site.preview);
          // the hub followed redirects: https, or the name with www
          const p = site.preview;
          const where = displayUrl({ scheme: p.targetScheme === 'https' ? 'https' : 'http', host: p.targetHost, port: p.targetPort, path: p.path });
          patch({ tailnet: true, previewId: p.id, url: where });
          return;
        }
      }
      if (how === 'ask') {
        const shown = ctl.nodes.find((n) => n.id !== id && n.kind === 'web' && !!guiStore.get().sessions[String(n.data.sessionId ?? '')]);
        if (shown) {
          setAsking(guiStore.get().sessions[String(shown.data.sessionId)]?.name ?? 'The browser');
          return;
        }
      }
      const here = how === 'here';
      const r = await guiApi.openUrl({ url, width: Math.round(node.w), height: Math.round(node.h - HEAD_H), ...(here ? { here, instance: d.instance || undefined } : {}) });
      // The "guiSession" event may already have brought a newer state.
      if (!guiStore.get().sessions[r.session.id]) guiStore.upsert(r.session);
      const other = here ? undefined : ctl.nodes.find((n) => n.id !== id && (n.kind === 'app' || n.kind === 'web') && n.data.appId === r.session.appId);
      if (!other) {
        patch({ sessionId: r.session.id, appId: r.session.appId, name: r.session.name, instance: r.session.instance ?? 1 });
        return;
      }
      // A tile on this board shows this browser already: the page is a tab there, and this
      // tile has nothing left to show. Left empty, it would cover the page it just opened.
      ctl.apply(
        [
          { op: 'patch', id: other.id, set: { data: { sessionId: r.session.id, name: r.session.name } } },
          { op: 'del', id },
        ],
        { history: false },
      );
      ctl.select([other.id]);
      frame(other.id);
      toast.info(`${host} opened as a new tab of ${r.session.name}.`);
    } catch (err) {
      setFailed(errorMessage(err));
    } finally {
      setOpening(false);
    }
  }, [url, d.realBrowser, d.instance, node.w, node.h, ctl, id, patch, frame, host]);

  useEffect(() => {
    if (openRequests.delete(id)) void open('ask');
    // only when the tile switches to this address, or to the light view of it
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [id, url, d.realBrowser, d.sessionId]);

  const openElsewhere = () => window.open(url, '_blank', 'noopener,noreferrer');
  const linkCard = () => ctl.addNodes([ctl.make('link', { x: node.x, y: node.y + node.h + 32, w: 320, h: 112 }, { url, title: '' })]);
  const clear = () => {
    // nothing else shows the browser then, and it holds a lot of memory
    if (session) void guiApi.stop(session.id).catch(() => undefined);
    patch({ url: '', sessionId: '', appId: '', name: '', previewId: '', tabs: [], activeTab: '', realBrowser: false });
  };

  const lightView = () => {
    rememberRealBrowser(host, false);
    // nothing else shows the browser then, and it holds a lot of memory
    if (session) void guiApi.stop(session.id).catch(() => undefined);
    requestBrowserOpen(id);
    patch({ sessionId: '', appId: '', name: '', realBrowser: false });
  };

  const menu: MenuEntry[] = [
    { label: 'Try the light view', icon: 'globe', onSelect: lightView },
    { label: `Open ${host} in a browser tab`, icon: 'external', onSelect: openElsewhere },
    { label: 'Add link card', icon: 'link', onSelect: linkCard },
    'sep',
    { label: session ? 'Close and clear page' : 'Clear page', icon: 'close', onSelect: clear },
  ];

  const icon = d.appId ? <AppIcon appId={d.appId} className="app-icon-lg" /> : <Icon name="globe" />;
  const title = session?.title || host;
  const reason =
    failed ||
    (ended?.reason === 'failed' && ended.message) ||
    (ended ? `${name} was closed.` : prefersRealBrowser(host) ? `${host} opens in a real browser on This PC, as you chose for it before. Try the light view to load it through the hub again.` : `${host} opens in a real browser on This PC.`);

  return (
    <TileFrame id={id} kind="web" selected={!!selected} label={`Web tile ${title}`} minW={320} minH={240}>
      <TileHead
        id={id}
        icon={d.appId ? <AppIcon appId={d.appId} className="app-icon" /> : 'globe'}
        title={title}
        meta={<AppWhere viewer={viewer} />}
        actions={<AppHeadControls viewer={viewer} session={session} name={name} />}
        menu={menu}
        menuLabel="Web tile actions"
        showDisplay
      />
      <div className="web-body nodrag nopan nowheel">
        <AppDisplay viewer={viewer} session={session} title={title} name={name} selected={!!selected} icon={icon} />
        {!session && opening && (
          <div className="web-msg app-wait" role="status">
            <span className="spinner" aria-hidden="true" />
            <p>Opening {host}…</p>
          </div>
        )}
        {!session && !opening && asking && (
          <div className="web-msg" data-testid="browser-choice">
            {icon}
            <p>
              {asking} is open in another tile. A tab there costs almost no memory. A browser here takes several hundred MB and has its own logins.
            </p>
            <div className="row-gap wrap">
              <Button size="sm" variant="primary" icon="play" onClick={() => void open('tab')}>
                New tab there
              </Button>
              <Button size="sm" onClick={() => void open('here')}>
                Browser here
              </Button>
            </div>
          </div>
        )}
        {!session && !opening && !asking && (loaded || !d.sessionId) && (
          <div className="web-msg" role={failed ? 'alert' : undefined}>
            {icon}
            <p>{reason}</p>
            <div className="row-gap wrap">
              <Button size="sm" variant="primary" icon="play" onClick={() => void open('here')}>
                Open here
              </Button>
              <Button size="sm" icon="external" onClick={openElsewhere}>
                Browser tab
              </Button>
              <Button size="sm" icon="link" onClick={linkCard}>
                Link card
              </Button>
            </div>
          </div>
        )}
      </div>
    </TileFrame>
  );
}
