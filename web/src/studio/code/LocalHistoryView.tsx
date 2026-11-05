// The earlier states of one file, kept by the hub whoever wrote them (docs/API-studio.md §7.11).

import { useEffect, useRef, useState } from 'react';
import { errorMessage } from '../../api';
import { Button } from '../../components/Button';
import { formatDateTime, timeAgo } from '../../lib/format';
import { reviewApi } from '../api';
import type { LocalVersion } from '../types';
import { currentTheme, FONT, languageFor, monaco } from './monaco';
import type { Workspace } from './workspace';

const SIDE_BY_SIDE_FROM = 900;

export function LocalHistoryView({ ws, path }: { ws: Workspace; path: string }) {
  const host = useRef<HTMLDivElement>(null);
  const diffRef = useRef<monaco.editor.IStandaloneDiffEditor | null>(null);
  const models = useRef<monaco.editor.ITextModel[]>([]);
  const [items, setItems] = useState<LocalVersion[] | null>(null);
  const [picked, setPicked] = useState('');
  const [note, setNote] = useState('');
  const [text, setText] = useState<string | null>(null);
  const [sideBySide, setSideBySide] = useState(true);
  const rel = ws.rel(path);

  useEffect(() => {
    let stop = false;
    reviewApi
      .history(ws.pid, rel)
      .then((list) => {
        if (stop) return;
        setItems(list);
        // the first entry is what the file holds now: the one before it says more
        setPicked((list[1] ?? list[0])?.commit ?? '');
      })
      .catch((err) => {
        if (stop) return;
        setItems([]);
        setNote(errorMessage(err));
      });
    return () => {
      stop = true;
    };
  }, [ws, rel]);

  const show = (next: monaco.editor.ITextModel[]) => {
    diffRef.current?.setModel(next.length ? { original: next[0], modified: next[1] } : null);
    models.current.forEach((m) => m.dispose());
    models.current = next;
  };

  useEffect(() => {
    if (!host.current) return;
    const wide = host.current.clientWidth >= SIDE_BY_SIDE_FROM;
    setSideBySide(wide);
    const diff = monaco.editor.createDiffEditor(host.current, {
      theme: currentTheme(),
      automaticLayout: true,
      fontFamily: FONT,
      fontSize: ws.editorPrefs.fontSize,
      renderSideBySide: wide,
      useInlineViewWhenSpaceIsLimited: false,
      originalEditable: false,
      readOnly: true,
      ignoreTrimWhitespace: false,
      hideUnchangedRegions: { enabled: true },
      scrollBeyondLastLine: false,
      fixedOverflowWidgets: false,
    });
    diffRef.current = diff;
    return () => {
      diff.setModel(null);
      diff.dispose();
      diffRef.current = null;
      models.current.forEach((m) => m.dispose());
      models.current = [];
    };
    // the editor lives as long as the view
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws]);

  useEffect(() => {
    diffRef.current?.updateOptions({ renderSideBySide: sideBySide });
  }, [sideBySide]);

  const version = items?.find((v) => v.commit === picked);
  useEffect(() => {
    if (!version) return;
    let stop = false;
    setNote('');
    setText(null);
    Promise.all([version.exists ? reviewApi.version(ws.pid, rel, version.commit) : Promise.resolve({ content: '', binary: false }), ws.currentText(path)])
      .then(([v, now]) => {
        if (stop) return;
        if (v.binary) {
          show([]);
          setNote('Binary file: nothing to compare.');
          return;
        }
        setText(version.exists ? v.content : null);
        const language = languageFor(path) ?? 'plaintext';
        show([monaco.editor.createModel(v.content, language), monaco.editor.createModel(now, language)]);
      })
      .catch((err) => {
        if (stop) return;
        show([]);
        setNote(errorMessage(err));
      });
    return () => {
      stop = true;
    };
    // show only touches refs
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, rel, path, version?.commit, version?.exists]);

  return (
    <div className="wb-cv nowheel">
      <div className="wb-cv-split">
        <div className="wb-cv-files wb-tree" role="list" aria-label={`Versions of ${rel}`}>
          {items === null && <div className="wb-row-msg">Loading…</div>}
          {items && !items.length && <div className="wb-row-msg">{note || 'No versions of this file yet.'}</div>}
          {items?.map((v, i) => (
            <button key={v.commit} type="button" className={`wb-row wb-lh-row ${v.commit === picked ? 'is-selected' : ''}`} title={formatDateTime(v.at)} aria-current={v.commit === picked ? 'true' : undefined} onClick={() => setPicked(v.commit)}>
              <span className="wb-row-name truncate">{i === 0 ? 'Now' : timeAgo(v.at)}</span>
              <span className="wb-result-dir truncate">{v.exists ? v.who : `Deleted · ${v.who}`}</span>
            </button>
          ))}
        </div>
        <div className="wb-diff wb-cv-diff">
          <div className="wb-diff-bar">
            <span className="truncate" title={rel}>
              {version ? `${formatDateTime(version.at)} ↔ now` : rel}
            </span>
            {note && <span className="text-3 truncate">{note}</span>}
            <span className="spacer" />
            <Button size="sm" variant="ghost" onClick={() => setSideBySide((v) => !v)}>
              {sideBySide ? 'Inline' : 'Side by side'}
            </Button>
            <Button
              size="sm"
              variant="primary"
              disabled={text === null}
              onClick={() => {
                if (text !== null) void ws.restoreText(path, text).catch((err) => setNote(errorMessage(err)));
              }}
            >
              Restore this version
            </Button>
          </div>
          <div className="wb-monaco" ref={host} data-keep-escape="" />
        </div>
      </div>
    </div>
  );
}
