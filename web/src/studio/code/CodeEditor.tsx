// React wrapper around the CodeMirror editor: (re)created when the file or its loaded
// text changes (rev), edits flow out through onChange.

import { useEffect, useRef } from 'react';
import { createEditor, type EditorHandle } from './cm';

interface Props {
  path: string;
  text: string;
  /** Bump to replace the editor contents (file loaded, draft restored). */
  rev: number;
  readOnly?: boolean;
  crlf?: boolean;
  wrap?: boolean;
  placeholder?: string;
  onChange: (text: string) => void;
  onSave: () => void;
  label: string;
}

export function CodeEditor({ path, text, rev, readOnly, crlf, wrap, placeholder, onChange, onSave, label }: Props) {
  const host = useRef<HTMLDivElement>(null);
  const ed = useRef<EditorHandle | null>(null);
  const cb = useRef({ onChange, onSave });
  cb.current = { onChange, onSave };

  useEffect(() => {
    if (!host.current) return;
    const h = createEditor({
      parent: host.current,
      text,
      path,
      readOnly,
      crlf,
      wrap,
      placeholder,
      onChange: (t) => cb.current.onChange(t),
      onSave: () => cb.current.onSave(),
    });
    h.view.contentDOM.setAttribute('aria-label', label);
    ed.current = h;
    return () => {
      h.destroy();
      ed.current = null;
    };
    // text is read only when the editor is (re)created
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, rev, crlf, wrap]);

  useEffect(() => {
    ed.current?.setReadOnly(!!readOnly);
  }, [readOnly]);

  return <div className="code-editor" ref={host} data-keep-escape="" />;
}
