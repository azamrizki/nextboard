// The memory of every project: a galaxy to look at and a vault to work in. All
// projects sit at the core; each project is a star with its pages in orbit and its
// critical notes close by. Click anything to read it.

import { useEffect, useMemo, useRef, useState } from 'react';
import { errorMessage } from '../api';
import { AppHeader } from '../components/AppHeader';
import { Button } from '../components/Button';
import { Icon, type IconName } from '../components/Icon';
import { useMediaQuery } from '../lib/useMediaQuery';
import { boardPath, memoryPath, navigate, Link } from '../lib/router';
import { useOrch } from '../orch/store';
import { memoryApi } from './api';
import { DocPanel } from './DocPanel';
import { GalaxyEngine } from './engine';
import { buildGraph, nodeId, type MemNode } from './graph';
import { InitDialog, useInitJob } from './InitDialog';
import { NewPageDialog } from './NewPageDialog';
import type { DocRef, GalaxyDoc, GalaxyOwner, GalaxyView, MemoryHit } from './types';
import '../styles/memory.css';

const LEGEND: { cls: string; label: string }[] = [
  { cls: 'is-star', label: 'Main memory' },
  { cls: 'is-critical', label: 'Critical notes' },
  { cls: 'is-page', label: 'Sub-memory' },
  { cls: 'is-note', label: 'Agent note' },
  { cls: 'is-section', label: 'Heading' },
];

function docIcon(d: GalaxyDoc): IconName {
  if (d.critical) return 'shieldAlert';
  if (d.kind === 'main') return 'brain';
  if (d.kind === 'note') return 'note';
  return 'layers';
}

function countOf(d: GalaxyDoc): string {
  if (d.facts) return `${d.facts}`;
  return d.chars ? `${d.chars < 1000 ? d.chars : `${Math.round(d.chars / 100) / 10}k`}` : '—';
}

function DocRow({ d, owner, active, onOpen }: { d: GalaxyDoc; owner: string; active: boolean; onOpen: (r: DocRef) => void }) {
  return (
    <button type="button" className={`mem-row ${d.critical ? 'is-critical' : ''} ${active ? 'is-active' : ''}`} aria-current={active ? 'true' : undefined} onClick={() => onOpen({ owner, name: d.name })} title={d.description}>
      <Icon name={docIcon(d)} />
      <span className="truncate">{d.kind === 'main' ? 'Main memory' : d.title}</span>
      <span className="mem-count tabular" title={d.facts ? `${d.facts} ${d.critical ? 'rules' : 'facts'}` : `${d.chars} characters`}>
        {countOf(d)}
      </span>
    </button>
  );
}

function OwnerDocs({ o, doc, onOpen }: { o: GalaxyOwner; doc: DocRef | null; onOpen: (r: DocRef) => void }) {
  const owner = o.projectId ?? '';
  const main = o.docs.find((d) => d.kind === 'main');
  const critical = o.docs.find((d) => d.critical);
  const pages = o.docs.filter((d) => d.kind === 'sub' && !d.critical);
  const notes = o.docs.filter((d) => d.kind === 'note');
  const isActive = (d: GalaxyDoc) => doc?.owner === owner && doc.name === d.name;
  return (
    <>
      {main && <DocRow d={main} owner={owner} active={isActive(main)} onOpen={onOpen} />}
      {critical && <DocRow d={critical} owner={owner} active={isActive(critical)} onOpen={onOpen} />}
      {pages.length > 0 && <h3 className="mem-group">Sub-memories</h3>}
      {pages.map((d) => (
        <DocRow key={d.name} d={d} owner={owner} active={isActive(d)} onOpen={onOpen} />
      ))}
      {notes.length > 0 && <h3 className="mem-group">Notes from agents</h3>}
      {notes.map((d) => (
        <DocRow key={d.name} d={d} owner={owner} active={isActive(d)} onOpen={onOpen} />
      ))}
    </>
  );
}

export function MemoryPage({ projectId }: { projectId?: string }) {
  const canvas = useRef<HTMLCanvasElement>(null);
  const engine = useRef<GalaxyEngine | null>(null);
  const tip = useRef<HTMLDivElement>(null);
  const [view, setView] = useState<GalaxyView | null>(null);
  const [loadError, setLoadError] = useState('');
  const [doc, setDoc] = useState<DocRef | null>(null);
  const [editNew, setEditNew] = useState(false);
  const [hover, setHover] = useState<MemNode | null>(null);
  const [query, setQuery] = useState('');
  const [hits, setHits] = useState<MemoryHit[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [newPage, setNewPage] = useState<{ owner: string; name: string } | null>(null);
  const [initOpen, setInitOpen] = useState(false);
  const [nonce, setNonce] = useState(0);
  const narrow = useMediaQuery('(max-width: 899px)');
  const [vaultOpen, setVaultOpen] = useState(!narrow);
  const search = useRef<HTMLInputElement>(null);
  const searched = useRef(false);

  const owner = projectId ?? '';
  const memoryEvents = useOrch((s) => Object.values(s.memoryRev).reduce((a, b) => a + b, 0));
  const rev = memoryEvents + nonce;
  const init = useInitJob(owner);
  const initBusy = !!init.jobId && (!init.job || init.job.state === 'queued' || init.job.state === 'running');
  const initReady = init.job?.state === 'done';

  useEffect(() => {
    let alive = true;
    memoryApi
      .galaxy()
      .then((v) => {
        if (!alive) return;
        setView(v);
        setLoadError('');
      })
      .catch((err) => alive && setLoadError(errorMessage(err)));
    return () => {
      alive = false;
    };
  }, [rev]);

  const project = view?.projects.find((p) => p.projectId === projectId);
  const openDoc = (r: DocRef) => {
    setEditNew(false);
    setDoc(r);
  };

  // The engine lives as long as the page.
  useEffect(() => {
    if (!canvas.current) return;
    const e = new GalaxyEngine(canvas.current, {
      onHover: setHover,
      onPointer: (x, y) => {
        const el = tip.current;
        if (!el) return;
        // Beside the pointer, on the side where it fits.
        const tx = x + 16 + el.offsetWidth > window.innerWidth - 8 ? x - el.offsetWidth - 12 : x + 16;
        const ty = y + 16 + el.offsetHeight > window.innerHeight - 8 ? y - el.offsetHeight - 12 : y + 16;
        el.style.transform = `translate(${tx}px, ${ty}px)`;
      },
      onSelect: (n) => {
        if (n.kind === 'core' || n.kind === 'star') {
          if ((n.owner || undefined) !== projectIdRef.current && n.kind === 'star') navigate(memoryPath(n.owner));
        }
        setEditNew(false);
        setDoc({ owner: n.owner, name: n.doc });
      },
    });
    engine.current = e;
    return () => {
      e.destroy();
      engine.current = null;
    };
  }, []);
  const projectIdRef = useRef(projectId);
  projectIdRef.current = projectId;

  const graph = useMemo(() => (view ? buildGraph(view) : null), [view]);
  useEffect(() => {
    if (graph) engine.current?.setData(graph.nodes, graph.links);
  }, [graph]);

  // Keep the free part of the canvas the part that is looked at.
  const docOpen = !!doc;
  useEffect(() => {
    const left = !narrow && vaultOpen ? 320 : 0;
    const right = !narrow && docOpen ? 460 : 0;
    const top = document.querySelector('.app-header')?.getBoundingClientRect().bottom ?? 64;
    engine.current?.setInsets(left, right, top, narrow ? 56 : 0);
  }, [narrow, vaultOpen, docOpen]);

  // The URL says which project is in view.
  const graphReady = !!graph;
  useEffect(() => {
    if (!graphReady) return;
    if (projectId) engine.current?.focus(nodeId(projectId, 'main'));
    else engine.current?.fit();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [projectId, graphReady]);

  useEffect(() => {
    if (doc) {
      const id = nodeId(doc.owner, doc.name);
      engine.current?.setSelected(id);
      if (!doc.owner || doc.owner === projectId) engine.current?.focus(doc.owner ? id : nodeId('', 'main'), !!doc.owner);
    } else {
      engine.current?.setSelected(null);
      if (projectId) engine.current?.focus(nodeId(projectId, 'main'));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [doc]);

  // A deep link from the board: /memory/<project>?doc=<name>
  useEffect(() => {
    const name = new URLSearchParams(window.location.search).get('doc');
    if (name) setDoc({ owner, name });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Look things up as you type, like an agent would.
  useEffect(() => {
    const q = query.trim();
    if (!q) {
      setHits(null);
      setSearching(false);
      engine.current?.setHighlight(null);
      if (searched.current) {
        searched.current = false;
        if (projectId) engine.current?.focus(nodeId(projectId, 'main'));
        else engine.current?.fit();
      }
      return;
    }
    searched.current = true;
    const ctl = new AbortController();
    setSearching(true);
    const t = window.setTimeout(() => {
      memoryApi
        .search(q, owner, ctl.signal)
        .then((list) => {
          setHits(list);
          setSearching(false);
          const ids = new Set<string>();
          for (const h of list) {
            const o = h.global ? '' : (h.projectId ?? owner);
            ids.add(nodeId(o, h.doc));
            if (h.section) ids.add(nodeId(o, h.doc, h.section));
          }
          engine.current?.setHighlight(ids);
          engine.current?.reveal(ids);
        })
        .catch(() => undefined);
    }, 180);
    return () => {
      window.clearTimeout(t);
      ctl.abort();
    };
  }, [query, owner, rev]);

  // "/" to search, Esc to step back.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const typing = (e.target as HTMLElement)?.closest?.('input, textarea, select');
      if (e.key === '/' && !typing && !document.querySelector('.modal-layer')) {
        e.preventDefault();
        setVaultOpen(true);
        search.current?.focus();
      } else if (e.key === 'Escape' && !typing && !document.querySelector('.modal-layer')) {
        if (doc) setDoc(null);
        else if (projectId) navigate(memoryPath());
      }
    };
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [doc, projectId]);

  const ownerName = (o: string) => (o ? (view?.projects.find((p) => p.projectId === o)?.name ?? 'Project') : 'All projects');
  const ownerOf = (o: string): GalaxyOwner | undefined => (o ? view?.projects.find((p) => p.projectId === o) : view?.global);
  const projectFacts = (p: GalaxyOwner) => p.docs.find((d) => d.kind === 'main')?.facts ?? 0;
  const projectPages = (p: GalaxyOwner) => p.docs.filter((d) => d.kind === 'sub' && !d.critical).length;
  const uninitialised = !!project && (project.docs.find((d) => d.kind === 'main')?.chars ?? 0) === 0;

  return (
    <div className="page page-memory" data-theme="dark">
      <div className="mem-stage">
        <canvas
          ref={canvas}
          className="mem-canvas"
          role="img"
          aria-label={view ? `Memory galaxy: ${view.projects.length} project${view.projects.length === 1 ? '' : 's'}. The list beside it holds the same memories.` : 'Memory galaxy'}
        />
        {hover && (
          <div className="mem-tip" ref={tip} role="status">
            <strong>{hover.kind === 'core' ? 'All projects' : hover.title}</strong>
            {hover.description && <span>{hover.description}</span>}
            <span className="text-3 tabular">
              {hover.kind === 'section' ? `Heading in ${hover.doc === 'main' ? 'main memory' : hover.doc}` : hover.facts ? `${hover.facts} ${hover.kind === 'critical' ? 'rule' : 'fact'}${hover.facts === 1 ? '' : 's'}` : hover.chars ? `${hover.chars} characters` : 'Empty'}
            </span>
          </div>
        )}
      </div>
      <AppHeader />

      <button type="button" className="mem-vault-toggle" aria-expanded={vaultOpen} onClick={() => setVaultOpen((v) => !v)}>
        <Icon name={vaultOpen ? 'close' : 'rows'} />
        <span>Memories</span>
      </button>

      {vaultOpen && (
        <nav className="mem-panel mem-vault" aria-label="Memories">
          <div className="mem-crumb">
            {projectId ? (
              <>
                <Link to={memoryPath()} className="mem-back" title="Back to all memories">
                  <Icon name="arrowLeft" />
                  Galaxy
                </Link>
                <h1 className="truncate" title={project?.name}>
                  {project?.name ?? ' '}
                </h1>
              </>
            ) : (
              <h1>Memory</h1>
            )}
          </div>

          <div className="mem-search">
            <Icon name="search" />
            <input ref={search} className="control control-sm" type="search" value={query} placeholder={projectId ? 'Look up a topic…  /' : 'Search every memory…  /'} aria-label="Look up a topic" onChange={(e) => setQuery(e.target.value)} />
          </div>

          {loadError && (
            <div className="alert" role="alert">
              <Icon name="alertCircle" />
              <span>Could not load the memories: {loadError}</span>
            </div>
          )}

          <div className="mem-scroll">
            {hits ? (
              <div aria-live="polite">
                <h3 className="mem-group">
                  {searching ? 'Looking…' : hits.length ? `${hits.length} passage${hits.length === 1 ? '' : 's'}` : 'Nothing about that yet'}
                </h3>
                {hits.map((h, i) => {
                  const o = h.global ? '' : (h.projectId ?? owner);
                  return (
                    <button key={`${o}/${h.doc}/${h.section}/${i}`} type="button" className={`mem-hit ${h.critical ? 'is-critical' : ''}`} onClick={() => openDoc({ owner: o, name: h.doc })}>
                      <span className="mem-hit-where truncate">
                        {!projectId && <span className="text-3">{h.global ? 'All projects' : h.project} › </span>}
                        {h.title}
                        {h.section ? ` › ${h.section}` : ''}
                      </span>
                      <span className="mem-hit-text">{h.text}</span>
                    </button>
                  );
                })}
              </div>
            ) : projectId ? (
              project && (
                <>
                  {uninitialised && !initBusy && !initReady && (
                    <p className="mem-hint">
                      This star is still dark. Let an agent read the project and propose its memory, or write it yourself.
                    </p>
                  )}
                  <div className="mem-actions">
                    <Button size="sm" variant={uninitialised || initReady ? 'primary' : 'secondary'} icon={initBusy ? undefined : 'search'} onClick={() => setInitOpen(true)}>
                      {initBusy && <span className="spinner" aria-hidden="true" />}
                      {initBusy ? 'Reading the project…' : initReady ? 'Review what it found' : 'Init memory'}
                    </Button>
                    <Button size="sm" icon="plus" onClick={() => setNewPage({ owner, name: project.name })}>
                      Sub-memory
                    </Button>
                  </div>
                  <OwnerDocs o={project} doc={doc} onOpen={openDoc} />
                  <p className="mem-foot">
                    <Link to={boardPath(owner)}>Open the board of {project.name}</Link>
                  </p>
                </>
              )
            ) : (
              view && (
                <>
                  <h3 className="mem-group">Projects</h3>
                  {view.projects.length === 0 && <p className="mem-hint">No projects yet. Each project you add becomes a star here.</p>}
                  {view.projects.map((p) => (
                    <Link key={p.projectId} to={memoryPath(p.projectId)} className="mem-row mem-row-project">
                      <Icon name="brain" />
                      <span className="mem-row-text">
                        <span className="truncate">{p.name}</span>
                        <span className="mem-row-meta tabular">
                          {projectFacts(p) || projectPages(p) ? `${projectFacts(p)} fact${projectFacts(p) === 1 ? '' : 's'} · ${projectPages(p)} sub-memor${projectPages(p) === 1 ? 'y' : 'ies'}` : 'Not initialised yet'}
                        </span>
                      </span>
                    </Link>
                  ))}
                  <h3 className="mem-group mem-group-row">
                    <span>All projects</span>
                    <button type="button" className="mem-group-add" aria-label="New sub-memory for all projects" title="New sub-memory for all projects" onClick={() => setNewPage({ owner: '', name: 'All projects' })}>
                      <Icon name="plus" />
                    </button>
                  </h3>
                  <OwnerDocs o={view.global} doc={doc} onOpen={openDoc} />
                </>
              )
            )}
          </div>
        </nav>
      )}

      {doc && (
        <DocPanel
          doc={doc}
          owner={ownerOf(doc.owner)}
          ownerName={ownerName(doc.owner)}
          rev={rev}
          startEditing={editNew}
          onClose={() => setDoc(null)}
          onOpen={openDoc}
          onChanged={() => setNonce((n) => n + 1)}
        />
      )}

      <div className="mem-hud">
        <ul className="mem-legend" aria-label="Legend">
          {LEGEND.map((l) => (
            <li key={l.cls}>
              <span className={`mem-mark ${l.cls}`} aria-hidden="true" />
              {l.label}
            </li>
          ))}
        </ul>
        <div className="mem-zoom">
          <Button size="sm" variant="ghost" iconOnly icon="plus" aria-label="Zoom in" onClick={() => engine.current?.zoomBy(1.3)} />
          <Button size="sm" variant="ghost" iconOnly icon="minus" aria-label="Zoom out" onClick={() => engine.current?.zoomBy(1 / 1.3)} />
          <Button
            size="sm"
            variant="ghost"
            iconOnly
            icon="fit"
            aria-label="Show everything"
            onClick={() => {
              if (projectId) engine.current?.focus(nodeId(projectId, 'main'));
              else engine.current?.fit();
            }}
          />
        </div>
      </div>

      {newPage && (
        <NewPageDialog
          owner={newPage.owner}
          ownerName={newPage.name}
          taken={ownerOf(newPage.owner)?.docs.map((d) => d.name) ?? []}
          onClose={() => setNewPage(null)}
          onCreated={(name) => {
            const o = newPage.owner;
            setNewPage(null);
            setNonce((n) => n + 1);
            setEditNew(true);
            setDoc({ owner: o, name });
          }}
        />
      )}

      {initOpen && projectId && project && (
        <InitDialog
          projectId={projectId}
          projectName={project.name}
          init={init}
          onClose={() => setInitOpen(false)}
          onApplied={() => {
            setInitOpen(false);
            setNonce((n) => n + 1);
          }}
        />
      )}
    </div>
  );
}
