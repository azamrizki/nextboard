// A change a Claude console proposes, as a diff: the right side can still be edited, and each
// change has its own undo arrow in the middle. Accept hands the result back to the console,
// which writes the file; Reject tells it no.

import { useEffect, useRef, useState } from 'react';
import { Button } from '../../components/Button';
import { currentTheme, languageFor, monaco } from './monaco';
import { nameOf, type Tab, type Workspace } from './workspace';

export function ProposalPane({ ws, tab, font }: { ws: Workspace; tab: Tab; font: string }) {
  const host = useRef<HTMLDivElement>(null);
  const diffRef = useRef<monaco.editor.IStandaloneDiffEditor | null>(null);
  const [sideBySide, setSideBySide] = useState(true);
  const name = tab.diff?.slice('ide:'.length) ?? '';
  const proposal = ws.ide.proposals.get(name);

  useEffect(() => {
    if (!host.current || !proposal) return;
    let stop = false;
    const ext = proposal.path.slice(proposal.path.lastIndexOf('.')).toLowerCase();
    const language = languageFor(proposal.path) ?? monaco.languages.getLanguages().find((l) => l.extensions?.includes(ext))?.id ?? 'plaintext';
    let original: monaco.editor.ITextModel | null = null;
    const modified = monaco.editor.createModel(proposal.after, language);
    proposal.currentText = () => modified.getValue();
    const diff = monaco.editor.createDiffEditor(host.current, {
      theme: currentTheme(),
      automaticLayout: true,
      fontFamily: font,
      fontSize: ws.editorPrefs.fontSize,
      renderSideBySide: sideBySide,
      originalEditable: false,
      ignoreTrimWhitespace: false,
      renderMarginRevertIcon: true,
      fixedOverflowWidgets: false,
    });
    diffRef.current = diff;
    // the old text first: compared with nothing, the whole file would flash as new
    void ws.ide.before(proposal).then((t) => {
      if (stop) return;
      original = monaco.editor.createModel(t, language);
      diff.setModel({ original, modified });
    });
    return () => {
      stop = true;
      diff.setModel(null);
      diff.dispose();
      original?.dispose();
      proposal.currentText = undefined;
      modified.dispose();
      diffRef.current = null;
    };
    // the proposal does not change; a new one with this name comes with a new call
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ws, name, proposal?.callId]);

  useEffect(() => {
    diffRef.current?.updateOptions({ renderSideBySide: sideBySide });
  }, [sideBySide]);

  if (!proposal) return <div className="wb-empty">This proposal has ended.</div>;
  return (
    <div className="wb-diff wb-proposal">
      <div className="wb-diff-bar">
        <span className="truncate">
          <strong>{proposal.console}</strong> proposes changes to {nameOf(proposal.path)}
        </span>
        <span className="spacer" />
        <Button size="sm" variant="ghost" onClick={() => setSideBySide((v) => !v)}>
          {sideBySide ? 'Inline' : 'Side by side'}
        </Button>
        <Button size="sm" variant="ghost" icon="close" onClick={() => ws.ide.reject(name)}>
          Reject
        </Button>
        <Button size="sm" variant="primary" icon="check" title="Accept · Ctrl+S" onClick={() => ws.ide.accept(name)}>
          Accept
        </Button>
      </div>
      <div className="wb-monaco" ref={host} data-keep-escape="" />
    </div>
  );
}
