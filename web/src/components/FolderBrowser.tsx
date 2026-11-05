// Folder picker backed by GET /api/machines/{id}/ls?path= (read-only `ls` on the machine).
// With `onChooseFile` it also picks files ("File from project" on the board).

import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { api, errorMessage } from '../api';
import type { DirListing } from '../lib/types';
import { Button } from './Button';
import { Checkbox } from './form';
import { Icon } from './Icon';
import { Modal } from './Modal';

interface FolderBrowserProps {
  machineId: string;
  machineName: string;
  /** Starting path; "" or "~" = home directory. */
  initialPath: string;
  onChoose: (path: string) => void;
  onClose: () => void;
  /** File mode: called with the chosen file path. `onChoose` then means "add this folder". */
  onChooseFile?: (path: string) => void;
  title?: string;
  /** Label of the folder button (default "Use this folder"). */
  chooseLabel?: string;
}

export function FolderBrowser({ machineId, machineName, initialPath, onChoose, onClose, onChooseFile, title, chooseLabel }: FolderBrowserProps) {
  const [listing, setListing] = useState<DirListing | null>(null);
  const [selected, setSelected] = useState<string | null>(null);
  const [pathInput, setPathInput] = useState(initialPath);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [showHidden, setShowHidden] = useState(false);
  const abortRef = useRef<AbortController | null>(null);
  // what the path field held when a load started; a later edit by the user wins
  const inputRef = useRef(initialPath);
  inputRef.current = pathInput;

  const load = useCallback(
    async (path: string) => {
      abortRef.current?.abort();
      const ctrl = new AbortController();
      abortRef.current = ctrl;
      setLoading(true);
      setError('');
      const typed = inputRef.current;
      try {
        const l = await api.listDir(machineId, path, ctrl.signal);
        setListing(l);
        if (inputRef.current === typed || inputRef.current === path) setPathInput(l.path);
        setSelected(null);
      } catch (err) {
        if (ctrl.signal.aborted) return;
        setError(errorMessage(err));
      } finally {
        if (!ctrl.signal.aborted) setLoading(false);
      }
    },
    [machineId],
  );

  useEffect(() => {
    void load(initialPath);
    return () => abortRef.current?.abort();
  }, [load]); // initialPath is only the starting point; later changes are ignored on purpose

  const entries = (listing?.entries ?? [])
    .filter((e) => showHidden || !e.name.startsWith('.'))
    .sort((a, b) => Number(b.dir) - Number(a.dir) || a.name.localeCompare(b.name));

  const join = (base: string, name: string) => (base.endsWith('/') ? base + name : `${base}/${name}`);
  const canGoUp = !!listing && !!listing.parent && listing.parent !== listing.path;

  const submitPath = (e: FormEvent) => {
    e.preventDefault();
    e.stopPropagation();
    void load(pathInput.trim());
  };

  return (
    <Modal
      title={title ?? `Choose a folder on ${machineName}`}
      icon="folder"
      onClose={onClose}
      size="lg"
      help={<Checkbox checked={showHidden} onChange={setShowHidden} label="Show hidden files" />}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          {onChooseFile ? (
            <>
              <Button variant="secondary" icon="folderOpen" disabled={!listing || loading} onClick={() => listing && onChoose(listing.path)}>
                {chooseLabel ?? 'Add folder card'}
              </Button>
              <Button
                variant="primary"
                icon="plus"
                disabled={!listing || loading || !selected}
                onClick={() => listing && selected && onChooseFile(join(listing.path, selected))}
              >
                Add file
              </Button>
            </>
          ) : (
            <Button variant="primary" icon="check" disabled={!listing || loading} onClick={() => listing && onChoose(listing.path)}>
              {chooseLabel ?? 'Use this folder'}
            </Button>
          )}
        </>
      }
    >
      <form className="fb-bar" onSubmit={submitPath} aria-label="Go to path">
        <Button
          iconOnly
          icon="arrowUp"
          aria-label="Parent folder"
          title="Parent folder"
          disabled={!canGoUp || loading}
          onClick={() => listing && void load(listing.parent)}
        />
        <Button iconOnly icon="home" aria-label="Home folder" title="Home folder" disabled={loading} onClick={() => void load('~')} />
        <div className="input-group has-lead has-addon">
          <span className="ig-lead">
            <Icon name="folder" />
          </span>
          <input
            className="control mono"
            value={pathInput}
            onChange={(e) => setPathInput(e.target.value)}
            aria-label="Folder path"
            spellCheck={false}
          />
          <Button type="submit" className="ig-addon" loading={loading} disabled={!pathInput.trim()}>
            Open path
          </Button>
        </div>
      </form>

      {error && (
        <div className="alert mb-3" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}

      <ul className="dir-list" aria-busy={loading} aria-label={listing ? `Contents of ${listing.path}` : 'Folder contents'}>
        {loading && !listing && <li className="list-msg">Loading…</li>}
        {listing && entries.length === 0 && !loading && <li className="list-msg">This folder is empty.</li>}
        {listing &&
          entries.map((e) =>
            e.dir ? (
              <li key={e.name}>
                <button type="button" className="dir-entry" onClick={() => void load(join(listing.path, e.name))} title={e.name}>
                  <Icon name="folder" />
                  <span>{e.name}</span>
                </button>
              </li>
            ) : onChooseFile ? (
              <li key={e.name}>
                <button
                  type="button"
                  className={`dir-entry file-pickable ${selected === e.name ? 'selected' : ''}`}
                  aria-pressed={selected === e.name}
                  onClick={() => setSelected(e.name)}
                  onDoubleClick={() => onChooseFile(join(listing.path, e.name))}
                  title={`${e.name} — double-click to add`}
                >
                  <Icon name="file" />
                  <span>{e.name}</span>
                </button>
              </li>
            ) : (
              <li key={e.name} className="file-entry" title={e.name}>
                <Icon name="file" />
                <span>{e.name}</span>
              </li>
            ),
          )}
      </ul>
    </Modal>
  );
}
