// One file an agent changed, in a tab: against what the agent found, kept or undone as a whole or
// change by change (docs/API-studio.md §7.10).

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { errorMessage, isCode } from '../../api';
import { Button } from '../../components/Button';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { reviewApi } from '../api';
import type { ReviewFile } from '../types';
import { currentTheme, FONT, languageFor, monaco } from './monaco';
import { baseWithChange, currentWithoutChange, type LineChange } from './reviewHunks';
import type { Tab, Workspace } from './workspace';

const SIDE_BY_SIDE_FROM = 900;

export function ReviewView({ ws, tab, session, path, agent }: { ws: Workspace; tab: Tab; session: string; path: string; agent: string }) {
  const host = useRef<HTMLDivElement>(null);
  const diffRef = useRef<monaco.editor.IStandaloneDiffEditor | null>(null);
  const models = useRef<{ original: monaco.editor.ITextModel; modified: monaco.editor.ITextModel } | null>(null);
  const deco = useRef<monaco.editor.IEditorDecorationsCollection | null>(null);
  const [file, setFile] = useState<ReviewFile | null>(null);
  const [error, setError] = useState('');
  const [changes, setChanges] = useState<LineChange[]>([]);
  const [at, setAt] = useState(0);
  const [sideBySide, setSideBySide] = useState(true);
  const [busy, setBusy] = useState(false);
  const language = useMemo(() => languageFor(path) ?? 'plaintext', [path]);
  const pending = ws.review?.agents.find((a) => a.sessionId === session)?.files.find((f) => f.path === path);

  const load = useCallback(async () => {
    try {
      const f = await reviewApi.file(ws.pid, session, path);
      setFile(f);
      setError('');
      const diff = diffRef.current;
      if (!diff || f.binary || f.tooBig) return;
      if (!models.current) {
        models.current = { original: monaco.editor.createModel(f.base, language), modified: monaco.editor.createModel(f.current, language) };
        diff.setModel(models.current);
        return;
      }
      if (models.current.original.getValue() !== f.base) models.current.original.setValue(f.base);
      if (models.current.modified.getValue() !== f.current) models.current.modified.setValue(f.current);
    } catch (err) {
      // all of it was kept or undone, from here or from the panel
      if (isCode(err, 'no_change')) ws.closeSpecial(tab.id);
      else setError(errorMessage(err));
    }
  }, [ws, session, path, language, tab.id]);

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
      scrollBeyondLastLine: false,
      fixedOverflowWidgets: false,
    });
    diffRef.current = diff;
    deco.current = diff.getModifiedEditor().createDecorationsCollection();
    const sub = diff.onDidUpdateDiff(() => {
      const list = diff.getLineChanges() ?? [];
      setChanges(list);
      setAt((i) => Math.min(i, Math.max(list.length - 1, 0)));
    });
    return () => {
      sub.dispose();
      deco.current?.clear();
      diff.setModel(null);
      diff.dispose();
      diffRef.current = null;
      models.current?.original.dispose();
      models.current?.modified.dispose();
      models.current = null;
    };
    // the editor lives as long as the view
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws]);

  useEffect(() => {
    void load();
    // new numbers from the hub mean the file moved
  }, [load, pending?.add, pending?.del]);

  useEffect(() => {
    diffRef.current?.updateOptions({ renderSideBySide: sideBySide });
  }, [sideBySide]);

  const current = changes[at];
  useEffect(() => {
    const diff = diffRef.current;
    if (!diff || !current) {
      deco.current?.clear();
      return;
    }
    const line = current.modifiedEndLineNumber === 0 ? Math.max(current.modifiedStartLineNumber, 1) : current.modifiedStartLineNumber;
    const end = Math.max(current.modifiedEndLineNumber, line);
    deco.current?.set([{ range: new monaco.Range(line, 1, end, 1), options: { isWholeLine: true, linesDecorationsClassName: 'wb-rv-current' } }]);
    diff.getModifiedEditor().revealLineInCenterIfOutsideViewport(line);
  }, [current]);

  const act = async (fn: () => Promise<unknown>, done?: string) => {
    setBusy(true);
    try {
      await fn();
      if (done) toast.success(done);
      await ws.refreshReview();
      await load();
    } catch (err) {
      toast.error(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  const whole = (keep: boolean) =>
    act(async () => {
      if (keep) await reviewApi.accept(ws.pid, { session, path });
      else await reviewApi.reject(ws.pid, { session, path });
      ws.closeSpecial(tab.id);
    }, keep ? 'Kept.' : 'Undone. The text stays in Local History.');

  const one = (keep: boolean) => {
    const m = models.current;
    if (!current || !m) return;
    const base = m.original.getValue();
    const now = m.modified.getValue();
    void act(async () => {
      if (keep) await reviewApi.accept(ws.pid, { session, path, base: baseWithChange(base, now, current) });
      else await reviewApi.reject(ws.pid, { session, path, text: currentWithoutChange(base, now, current) });
    });
  };

  const textual = !!file && !file.binary && !file.tooBig;
  return (
    <div className="wb-diff wb-rv-view nowheel">
      <div className="wb-diff-bar wb-rv-bar">
        <span className="truncate" title={path}>
          <strong>{agent}</strong> · {path}
        </span>
        {!!pending?.also?.length && (
          <span className="wb-rv-also" role="status">
            <Icon name="alert" /> Also changed by {pending.also.join(', ')}
          </span>
        )}
        {pending?.unsure && <span className="text-3">Several agents were at work: this may be another one’s.</span>}
        {error && <span className="text-3 truncate">{error}</span>}
        <span className="spacer" />
        {textual && changes.length > 0 && (
          <span className="wb-rv-step">
            <Button size="sm" variant="ghost" iconOnly icon="arrowUp" aria-label="Previous change" disabled={at === 0} onClick={() => setAt((i) => Math.max(i - 1, 0))} />
            <span className="tabular">
              Change {at + 1} of {changes.length}
            </span>
            <Button size="sm" variant="ghost" iconOnly icon="arrowDown" aria-label="Next change" disabled={at >= changes.length - 1} onClick={() => setAt((i) => Math.min(i + 1, changes.length - 1))} />
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => one(false)}>
              Undo change
            </Button>
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => one(true)}>
              Keep change
            </Button>
          </span>
        )}
        <Button size="sm" variant="ghost" onClick={() => setSideBySide((v) => !v)}>
          {sideBySide ? 'Inline' : 'Side by side'}
        </Button>
        <Button size="sm" disabled={busy} onClick={() => void whole(false)}>
          Undo file
        </Button>
        <Button size="sm" variant="primary" disabled={busy} onClick={() => void whole(true)}>
          Keep file
        </Button>
      </div>
      {file?.binary && <div className="wb-row-msg">Binary file: nothing to compare. It can still be kept or undone.</div>}
      {file?.tooBig && <div className="wb-row-msg">Too large to compare here. It can still be kept or undone.</div>}
      <div className="wb-monaco" ref={host} data-keep-escape="" style={textual ? undefined : { display: 'none' }} />
    </div>
  );
}
