// Document card (node kind "document", docs/API-studio.md §4.7): a version of a studio
// document on the board, with its own PDF export.

import { memo, useEffect, useState } from 'react';
import type { NodeProps } from '@xyflow/react';
import { errorMessage } from '../../api';
import type { MenuEntry } from '../../components/Menu';
import type { FlowNode } from '../../board/flow';
import { TileFrame, TileHead } from '../../board/tiles/common';
import { useBoardEnv } from '../../board/ui';
import { docApi } from '../api';
import { MarkdownView } from '../MarkdownView';
import type { StudioDocument } from '../types';
import { docLabel, printDocUrl } from './model';

function DocumentTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ui } = useBoardEnv();
  const documentId = typeof data.node.data.documentId === 'string' ? data.node.data.documentId : '';
  const pinned = typeof data.node.data.versionId === 'string' ? data.node.data.versionId : '';
  const [doc, setDoc] = useState<StudioDocument | null>(null);
  const [text, setText] = useState<string | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    let stop = false;
    (async () => {
      try {
        const d = await docApi.get(documentId);
        if (stop) return;
        setDoc(d);
        const done = (d.versions ?? []).filter((v) => v.state !== 'running');
        const v = done.find((x) => x.id === pinned) ?? done[done.length - 1];
        if (!v) throw new Error('This document has no finished version yet.');
        const r = await docApi.version(d.id, v.id);
        if (!stop) setText(r.markdown);
      } catch (err) {
        if (!stop) setError(errorMessage(err));
      }
    })();
    return () => {
      stop = true;
    };
  }, [documentId, pinned]);

  const vid = pinned || doc?.versions?.[doc.versions.length - 1]?.id || '';
  const menu: MenuEntry[] = [
    { label: 'Open in Documents', icon: 'book', onSelect: () => ui.set({ studio: { kind: 'documents', documentId } }) },
    { label: 'PDF (A4)', icon: 'printer', disabled: !vid, onSelect: () => window.open(printDocUrl(documentId, vid, 'a4'), '_blank', 'noopener') },
    { label: 'PDF (Letter)', icon: 'printer', disabled: !vid, onSelect: () => window.open(printDocUrl(documentId, vid, 'letter'), '_blank', 'noopener') },
  ];

  const title = doc?.title || (typeof data.node.data.title === 'string' ? data.node.data.title : '') || (doc ? docLabel(doc.type) : 'Document');
  return (
    <TileFrame id={id} kind="document" selected={!!selected} label={`Document ${title}`} minW={280} minH={200}>
      <TileHead id={id} icon="book" title={title} menu={menu} menuLabel="Document card actions" />
      <div className="doc-tile-body nodrag nowheel">
        {error ? <p className="tile-hint">{error}</p> : text === null ? <p className="tile-hint">Loading…</p> : <MarkdownView text={text} className="doc-md doc-md-tile" />}
      </div>
    </TileFrame>
  );
}

export const DocumentTile = memo(DocumentTileImpl);
