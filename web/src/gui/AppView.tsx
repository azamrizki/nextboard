// What every tile showing an app of This PC shares (docs/API-gui.md §4): the viewer page in
// an iframe, its state, and the header's location, sound and close controls.

import { useEffect, useRef, useState, type ReactNode, type RefObject } from 'react';
import { errorMessage } from '../api';
import { Button } from '../components/Button';
import { toast } from '../components/Toast';
import { prefs } from '../lib/format';
import { FillButton, type FillResult, type FillValues } from '../vault/FillDialog';
import { newId } from '../lib/ids';
import { guiApi, guiViewerUrl, type GuiSession } from './api';

/**
 * A press that starts outside an iframe stays outside until released. Over an iframe the
 * board's drag and resize stop seeing the pointer, release included, so a resize dragged
 * inward would never end and the tile would keep following the mouse.
 */
let pressGuardInstalled = false;
function installPressGuard() {
  if (pressGuardInstalled) return;
  pressGuardInstalled = true;
  const root = document.documentElement;
  window.addEventListener('pointerdown', () => root.classList.add('nb-pressing'), true);
  for (const type of ['pointerup', 'pointercancel']) window.addEventListener(type, () => root.classList.remove('nb-pressing'), true);
}

export interface AppViewer {
  frameRef: RefObject<HTMLIFrameElement | null>;
  connected: boolean;
  /** WebCodecs only exists on https and localhost: elsewhere big motion stays slow RFB. */
  noVideo: boolean;
  soundOn: boolean;
  toggleSound: () => void;
  /** Into whatever has focus in the app: text, a Tab, more text. */
  enterInto: (parts: EnterPart[]) => Promise<void>;
}

export type EnterPart = { text: string } | { key: 'tab' };

const ENTER_TIMEOUT_MS = 8000;

export function useAppViewer(appId: string, session: GuiSession | undefined): AppViewer {
  const [connected, setConnected] = useState(false);
  const [soundOn, setSoundOn] = useState(() => prefs.get(`guiSound.${appId}`) !== 'off');
  const [viewerReady, setViewerReady] = useState(0);
  const [noVideo, setNoVideo] = useState(false);
  const frameRef = useRef<HTMLIFrameElement>(null);
  const hasSound = !!session?.audio;
  const entering = useRef(new Map<string, (ok: boolean) => void>());

  useEffect(installPressGuard, []);

  useEffect(() => {
    setConnected(false);
  }, [session?.id]);

  useEffect(() => {
    const onMessage = (e: MessageEvent) => {
      if (e.origin !== location.origin || e.source !== frameRef.current?.contentWindow) return;
      const m = e.data as { nb?: string; type?: string; state?: string; video?: boolean; id?: string; ok?: boolean } | null;
      if (m?.nb === 'gui' && m.type === 'entered' && m.id) entering.current.get(m.id)?.(m.ok === true);
      if (m?.nb === 'gui' && m.type === 'ready') setViewerReady((n) => n + 1);
      if (m?.nb === 'gui' && m.type === 'state') {
        setConnected(m.state === 'connected');
        setNoVideo(m.video === false);
      }
    };
    window.addEventListener('message', onMessage);
    return () => window.removeEventListener('message', onMessage);
  }, []);

  useEffect(() => {
    frameRef.current?.contentWindow?.postMessage({ nb: 'gui', type: 'audio', on: hasSound && soundOn }, location.origin);
  }, [hasSound, soundOn, viewerReady]);

  const toggleSound = () => {
    prefs.set(`guiSound.${appId}`, soundOn ? 'off' : 'on');
    setSoundOn(!soundOn);
  };

  const enterInto = (parts: EnterPart[]) =>
    new Promise<void>((resolve, reject) => {
      const id = newId('i');
      const timer = window.setTimeout(() => {
        entering.current.delete(id);
        reject(new Error('The app did not answer.'));
      }, ENTER_TIMEOUT_MS);
      entering.current.set(id, (ok) => {
        window.clearTimeout(timer);
        entering.current.delete(id);
        if (ok) resolve();
        else reject(new Error('The app is not connected.'));
      });
      frameRef.current?.contentWindow?.postMessage({ nb: 'gui', type: 'enter', id, parts }, location.origin);
    });

  return { frameRef, connected, noVideo, soundOn, toggleSound, enterInto };
}

/** The session is there to show: not gone and not on its way out. */
export const isLive = (session: GuiSession | undefined): session is GuiSession => !!session && session.state !== 'stopping';

/** Header meta: where the app runs. */
export function AppWhere({ viewer }: { viewer: AppViewer }) {
  return (
    <span className="tile-meta" title={viewer.noVideo ? 'Smooth video needs HTTPS or localhost. This page is on plain HTTP, so big motion is slower.' : undefined}>
      {viewer.noVideo ? 'This PC · slower without HTTPS' : 'This PC'}
    </span>
  );
}

/** Header actions: the app's sound, close. */
export function AppHeadControls({ viewer, session, name }: { viewer: AppViewer; session: GuiSession | undefined; name: string }) {
  const stop = () => {
    if (!session) return;
    guiApi.stop(session.id).catch((err) => toast.error(`Could not close ${name}: ${errorMessage(err)}`));
  };
  const live = isLive(session);
  // It goes to whatever has focus in there, so anything asked for counts as filled.
  const send = async (v: FillValues): Promise<FillResult> => {
    const parts: EnterPart[] = [];
    if (v.username) parts.push({ text: v.username });
    if (v.username && v.password) parts.push({ key: 'tab' });
    if (v.password) parts.push({ text: v.password });
    if (v.code) parts.push({ text: v.code });
    await viewer.enterInto(parts);
    return { username: !!v.username, password: !!v.password, code: !!v.code };
  };
  return (
    <>
      {live && viewer.connected && (
        <FillButton
          sink={{
            mode: 'keyboard',
            where: session.title || name,
            send,
            onFilled: () => window.setTimeout(() => viewer.frameRef.current?.focus(), 0),
          }}
        />
      )}
      {live && session.audio && (
        <Button size="sm" variant="ghost" iconOnly icon={viewer.soundOn ? 'volume' : 'volumeOff'} className="nodrag" aria-label={viewer.soundOn ? 'Mute' : 'Unmute'} title={viewer.soundOn ? 'Mute' : 'Unmute'} onClick={viewer.toggleSound} />
      )}
      {live && <Button size="sm" variant="ghost" iconOnly icon="power" className="nodrag" aria-label={`Close ${name}`} title={`Close ${name}`} onClick={stop} />}
    </>
  );
}

/** The app's display, with a note on top while it opens, connects or closes. */
export function AppDisplay({ viewer, session, title, name, selected, icon }: { viewer: AppViewer; session: GuiSession | undefined; title: string; name: string; selected: boolean; icon: ReactNode }) {
  const live = isLive(session);
  let wait = '';
  if (session?.state === 'stopping') wait = `Closing ${name}…`;
  else if (session?.state === 'starting') wait = `Opening ${name}…`;
  else if (session && !viewer.connected) wait = 'Connecting…';
  return (
    <>
      {live && <iframe key={session.id} ref={viewer.frameRef} className="app-frame" src={guiViewerUrl(session)} title={title} allow="autoplay; clipboard-read; clipboard-write" />}
      {live && !selected && <div className="web-shield" aria-hidden="true" />}
      {wait && (
        <div className="web-msg app-wait">
          {icon}
          <p>{wait}</p>
        </div>
      )}
    </>
  );
}
