// What a tool tab of the code view shows (docs/API-studio.md §7): today's Chats, Memory, Loops and
// Mission Map panels, the Documents, Diagrams, Markdown and Skills studios, the vault and the
// browser, in an editor tab. They work on the editor's board binding (toolEnv.ts) and draw no
// canvas. Each loads when its tab first shows; one that fails says so in its own tab and leaves
// the editor running. A group keeps a few tool tabs alive behind the shown one (EditorArea): one
// hidden there, or by a phone's other views, is told so, and Mission Map and Loops, which poll
// and tick, pause (React's Activity: their state stays, their effects stop).

import { Activity, Component, lazy, Suspense, useEffect, useRef, useState, type ComponentType, type ReactNode, type RefObject } from 'react';
import { Button } from '../../components/Button';
import { Icon } from '../../components/Icon';
import type { ToolKind } from './toolKinds';
import { useToolEnv, type ToolEnv } from './toolEnv';
import { toolMeta } from './toolsList';
import type { Tab, Workspace } from './workspace';
import '../../styles/tooltabs.css';

const boardTools = () => import('./tools/BoardTools');

/** What a tool's body gets: `shown` is false while its tab is kept alive out of sight. */
export interface ToolBodyProps {
  env: ToolEnv;
  shown: boolean;
}

const BODIES: Record<ToolKind, ComponentType<ToolBodyProps>> = {
  chats: lazy(() => import('./tools/ChatsTool')),
  memory: lazy(() => boardTools().then((m) => ({ default: m.MemoryTool }))),
  documents: lazy(() => boardTools().then((m) => ({ default: m.DocumentsTool }))),
  diagrams: lazy(() => boardTools().then((m) => ({ default: m.DiagramsTool }))),
  markdown: lazy(() => boardTools().then((m) => ({ default: m.MarkdownTool }))),
  skills: lazy(() => boardTools().then((m) => ({ default: m.SkillsTool }))),
  vault: lazy(() => import('./tools/VaultTool')),
  browser: lazy(() => import('./tools/BrowserTool')),
  loops: lazy(() => boardTools().then((m) => ({ default: m.LoopsTool }))),
  mission: lazy(() => boardTools().then((m) => ({ default: m.MissionTool }))),
  card: lazy(() => import('./tools/CardTool')),
};

/** Tools whose effects only poll or tick: paused while out of sight, their state kept. */
const PAUSED_OUT_OF_SIGHT: ReadonlySet<ToolKind> = new Set(['mission', 'loops']);

/** Whether the element is on screen: false under a hidden ancestor (a kept tab, a phone's other view). */
function useShown(ref: RefObject<HTMLElement | null>): boolean {
  const [shown, setShown] = useState(true);
  useEffect(() => {
    const el = ref.current;
    if (!el || typeof IntersectionObserver === 'undefined') return;
    const io = new IntersectionObserver((entries) => setShown(entries.some((e) => e.isIntersecting)));
    io.observe(el);
    return () => io.disconnect();
  }, [ref]);
  return shown;
}

function Message({ icon = 'info', children }: { icon?: 'info' | 'alertCircle'; children: ReactNode }) {
  return (
    <div className="tool-tab-msg" role={icon === 'alertCircle' ? 'alert' : 'status'}>
      <Icon name={icon} />
      {children}
    </div>
  );
}

/** A tool that throws shows it here; the editor and its other tabs go on. */
class ToolBoundary extends Component<{ title: string; children: ReactNode }, { error: string }> {
  state = { error: '' };
  static getDerivedStateFromError(err: unknown) {
    return { error: err instanceof Error ? err.message : String(err) };
  }
  render() {
    if (!this.state.error) return this.props.children;
    return (
      <Message icon="alertCircle">
        <p>
          {this.props.title} stopped: {this.state.error}
        </p>
        <Button size="sm" icon="refresh" onClick={() => this.setState({ error: '' })}>
          Try again
        </Button>
      </Message>
    );
  }
}

export default function ToolTab({ ws, tab }: { ws: Workspace; tab: Tab }) {
  const tool = tab.tool;
  const env = useToolEnv(ws, tab);
  const root = useRef<HTMLDivElement>(null);
  const shown = useShown(root);
  if (!tool) return null;
  const meta = toolMeta(tool.kind, tool.arg);
  const Body = BODIES[tool.kind] as ComponentType<ToolBodyProps> | undefined;
  return (
    <div ref={root} className={`tool-tab tool-tab-${tool.kind}`} data-tool={tool.kind} data-shown={shown || undefined} role="region" aria-label={meta.title}>
      {!Body ? (
        <Message>
          <p>This tab is from a newer NextBoard. Update NextBoard on the hub to open it here.</p>
        </Message>
      ) : !env ? (
        <Message>
          <p>{meta.title} needs the project’s board. Open the project’s code view again.</p>
        </Message>
      ) : (
        <ToolBoundary key={tab.id} title={meta.title}>
          <Suspense
            fallback={
              <div className="tool-tab-msg" role="status">
                <span className="spinner" aria-hidden="true" />
                <p>Opening {meta.title}…</p>
              </div>
            }
          >
            <Activity mode={!shown && PAUSED_OUT_OF_SIGHT.has(tool.kind) ? 'hidden' : 'visible'}>
              <Body env={env} shown={shown} />
            </Activity>
          </Suspense>
        </ToolBoundary>
      )}
    </div>
  );
}
