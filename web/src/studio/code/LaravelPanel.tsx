// The Laravel view of the Code card (docs/API-studio.md §7.14): the application's routes, the end
// of its log, and artisan. Routes and log are read from files. Artisan is only ever typed into a
// terminal for a person to run, or asked by a person for the full route list: it boots the
// application, and this may be a live server. Each part has its own: a Root card with Laravel in
// two parts shows one at a time, with a switch.

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { errorMessage } from '../../api';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { Icon } from '../../components/Icon';
import { laravelApi, type LaravelLog, type LaravelRoute } from '../api';
import { SegmentedControl } from '../../components/form';
import type { PartService } from './partsvc';
import { fileKey } from './paths';
import { useWs } from './useWs';
import type { Workspace } from './workspace';

const ARTISAN = ['route:list', 'migrate:status', 'about', 'queue:failed', 'schedule:list', 'optimize:clear', 'tinker'];
const QUIET = new Set(['DEBUG', 'INFO', 'NOTICE']);

/** onTerminal(command, dir): typed into a new terminal in that folder (the Laravel part's, on its machine). */
export function LaravelPanel({ ws, onTerminal }: { ws: Workspace; onTerminal: (command: string, dir?: string) => void }) {
  useWs(ws);
  const [chosen, setChosen] = useState('');
  // the parts with Laravel; the one in use, else the one picked, else the first
  const detected = ws.parts.filter((p) => p.laravel?.detected);
  const active = ws.activePart;
  const part: PartService = detected.find((p) => p.partId === chosen) ?? (detected.includes(active) ? active : detected[0]) ?? active;
  const switcher =
    detected.length > 1 ? (
      <div className="wb-laravel-parts">
        <SegmentedControl size="sm" label="Laravel of which part" value={part.partId} onChange={setChosen} options={detected.map((p) => ({ value: p.partId, label: p.name, title: `${p.name}: ${p.root}` }))} />
      </div>
    ) : null;
  return <PartLaravel key={part.partId} ws={ws} part={part} switcher={switcher} onTerminal={onTerminal} />;
}

function PartLaravel({ ws, part, switcher, onTerminal }: { ws: Workspace; part: PartService; switcher: ReactNode; onTerminal: (command: string, dir?: string) => void }) {
  const titled = !!switcher;
  const idx = part.laravel;
  const [filter, setFilter] = useState('');
  const [full, setFull] = useState<LaravelRoute[] | null>(null);
  const [askArtisan, setAskArtisan] = useState(false);
  const [log, setLog] = useState<LaravelLog | null>(null);
  const [logError, setLogError] = useState('');
  const [problemsOnly, setProblemsOnly] = useState(true);
  const [openEntry, setOpenEntry] = useState(-1);
  const [command, setCommand] = useState('');

  const loadLog = async () => {
    setLogError('');
    try {
      setLog(await laravelApi.log(part.ref));
      setOpenEntry(-1);
    } catch (err) {
      setLogError(errorMessage(err));
    }
  };

  useEffect(() => {
    void part.refreshLaravel();
    void loadLog();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [part]);

  const routes = useMemo(() => {
    const list = full ?? idx?.routes ?? [];
    const q = filter.trim().toLowerCase();
    return q ? list.filter((r) => `${r.method} ${r.uri} ${r.name ?? ''} ${r.action ?? ''}`.toLowerCase().includes(q)) : list;
  }, [full, idx, filter]);

  const entries = useMemo(() => (log?.entries ?? []).map((e, i) => ({ e, i })).filter(({ e }) => !problemsOnly || !QUIET.has(e.level)), [log, problemsOnly]);

  // the index names files inside the part (under the application's folder); a log trace may name real paths
  const inRoot = (file: string) => (file.startsWith('/') ? fileKey(part.machineId, file) : part.keyOf(idx?.root ? `${idx.root}/${file}` : file));
  const run = (cmd: string) => onTerminal(`${idx?.root ? `cd ${idx.root} && ` : ''}php artisan ${cmd}`, part.rootKey);

  return (
    <div className="wb-view">
      <header className="wb-view-head">
        <h2 className="truncate">
          Laravel{idx?.version ? ` ${idx.version}` : ''}
          {titled ? ` · ${part.name}` : ''}
        </h2>
        <Button
          size="sm"
          variant="ghost"
          iconOnly
          icon="refresh"
          aria-label="Refresh"
          title="Read the routes, config and log again"
          onClick={() => {
            setFull(null);
            void part.refreshLaravel();
            void loadLog();
          }}
        />
      </header>
      {switcher}
      <div className="wb-tree nowheel wb-laravel">
        <div className="wb-section-title">
          Routes <span className="text-3 tabular">{routes.length}</span>
        </div>
        <div className="wb-laravel-tools">
          <input className="control control-sm grow" aria-label="Filter routes" placeholder="Filter" value={filter} onChange={(e) => setFilter(e.target.value)} />
          <Button size="sm" title="Asks the application itself, which also finds routes built by code" onClick={() => setAskArtisan(true)}>
            Ask artisan
          </Button>
        </div>
        {full && <div className="wb-row-msg">The list artisan gave. Refresh to go back to the one read from routes/.</div>}
        {!routes.length && <div className="wb-row-msg">{filter ? 'No route matches.' : 'No routes found in routes/.'}</div>}
        {routes.slice(0, 500).map((r, i) => (
          <button
            key={`${r.method} ${r.uri} ${i}`}
            type="button"
            className="wb-row wb-lh-row"
            disabled={!r.file}
            title={[r.action, r.file && r.line ? `${r.file}:${r.line}` : ''].filter(Boolean).join(' · ')}
            onClick={() => r.file && ws.open(inRoot(r.file), { line: r.line, source: true })}
          >
            <span className="wb-row-name truncate">
              <span className="mono fs-xs text-3">{r.method}</span> /{r.uri.replace(/^\//, '')}
            </span>
            <span className="wb-result-dir truncate">{r.name || r.action || ''}</span>
          </button>
        ))}
        {routes.length > 500 && <div className="wb-row-msg">+ {routes.length - 500} more. Filter to find them.</div>}

        <div className="wb-section-title wb-laravel-section">
          Log
          <label className="wb-laravel-check">
            <input type="checkbox" checked={problemsOnly} onChange={(e) => setProblemsOnly(e.target.checked)} /> Problems only
          </label>
        </div>
        {logError && <div className="wb-row-msg">{logError}</div>}
        {log && !log.file && <div className="wb-row-msg">No log in storage/logs yet.</div>}
        {log?.file && !entries.length && <div className="wb-row-msg">{problemsOnly ? 'No problems in the end of the log.' : 'The log is empty.'}</div>}
        {entries.map(({ e, i }) => (
          <div key={i} className="wb-laravel-log">
            <button type="button" className="wb-row wb-lh-row" aria-expanded={openEntry === i} onClick={() => setOpenEntry(openEntry === i ? -1 : i)}>
              <span className="wb-row-name truncate">
                <span className={`mono fs-xs ${QUIET.has(e.level) ? 'text-3' : 'wb-laravel-level'}`}>{e.level}</span> {e.message}
              </span>
              <span className="wb-result-dir truncate">{e.at}</span>
            </button>
            {openEntry === i && (
              <div className="wb-laravel-detail">
                {e.file && (
                  <Button size="sm" variant="ghost" icon="file" onClick={() => ws.open(inRoot(e.file!), { line: e.line, source: true })}>
                    {e.file}:{e.line}
                  </Button>
                )}
                <pre className="mono fs-xs">{e.message}{e.trace ? `\n${e.trace}` : ''}</pre>
              </div>
            )}
          </div>
        ))}
        {log?.cut && <div className="wb-row-msg">Only the end of {log.file} was read.</div>}

        <div className="wb-section-title wb-laravel-section">Artisan</div>
        <div className="wb-row-msg">{titled ? `Typed into a terminal in ${part.name}, on its machine.` : 'Typed into a terminal on this project’s machine.'} Nothing runs until you press Enter.</div>
        <div className="wb-laravel-artisan">
          {ARTISAN.map((c) => (
            <Button key={c} size="sm" variant="ghost" onClick={() => run(c)}>
              {c}
            </Button>
          ))}
        </div>
        <form
          className="wb-laravel-tools"
          onSubmit={(e) => {
            e.preventDefault();
            if (command.trim()) run(command.trim());
            setCommand('');
          }}
        >
          <input className="control control-sm grow mono" aria-label="Artisan command" placeholder="make:model Post -m" value={command} onChange={(e) => setCommand(e.target.value)} />
          <Button size="sm" type="submit" disabled={!command.trim()}>
            <Icon name="chevronRight" />
          </Button>
        </form>
      </div>

      {askArtisan && (
        <ConfirmDialog
          title="Ask artisan for the routes?"
          confirmLabel="Run route:list"
          onClose={() => setAskArtisan(false)}
          onConfirm={async () => {
            const r = await laravelApi.routes(part.ref);
            setFull(r.items ?? []);
          }}
        >
          <p>
            This runs <span className="mono">php artisan route:list</span> on this project’s machine. It boots the application, with what its service providers do at startup, so on a live server it counts as running code there.
          </p>
        </ConfirmDialog>
      )}
    </div>
  );
}
