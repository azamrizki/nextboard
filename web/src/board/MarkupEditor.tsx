// Markup editor for image tiles (lazy chunk): arrow, rectangle, ellipse, line, pen,
// highlighter and text in 8 colours × 4 widths. "Save markup" uploads a NEW image and
// places it next to the original; the original is never changed.

import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent } from 'react';
import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon, type IconName } from '../components/Icon';
import { Modal } from '../components/Modal';
import type { BoardController } from './controller';
import { readImage } from './kinds';
import { MARKUP_COLORS, MARKUP_TEXT_HALO, MARKUP_WIDTHS } from './palette';
import { imageSrcUrl } from './tiles/ImageTile';

type MTool = 'arrow' | 'rect' | 'ellipse' | 'line' | 'pen' | 'highlight' | 'text';
interface Mark {
  tool: MTool;
  color: string;
  width: number;
  pts: [number, number][];
  text?: string;
}

const TOOLS: { tool: MTool; icon: IconName; label: string }[] = [
  { tool: 'arrow', icon: 'arrowUpRight', label: 'Arrow' },
  { tool: 'rect', icon: 'square', label: 'Rectangle' },
  { tool: 'ellipse', icon: 'circle', label: 'Ellipse' },
  { tool: 'line', icon: 'line', label: 'Line' },
  { tool: 'pen', icon: 'pen', label: 'Pen' },
  { tool: 'highlight', icon: 'highlighter', label: 'Highlighter' },
  { tool: 'text', icon: 'type', label: 'Text' },
];
const MAX_EXPORT = 4096;

function drawMark(ctx: CanvasRenderingContext2D, m: Mark, k: number) {
  const w = m.width * k;
  ctx.save();
  ctx.strokeStyle = m.color;
  ctx.fillStyle = m.color;
  ctx.lineWidth = w;
  ctx.lineCap = 'round';
  ctx.lineJoin = 'round';
  const [a, b] = [m.pts[0], m.pts[m.pts.length - 1]];
  switch (m.tool) {
    case 'pen':
    case 'highlight': {
      if (m.tool === 'highlight') {
        ctx.globalAlpha = 0.35;
        ctx.lineWidth = w * 4;
        ctx.lineCap = 'square';
      }
      ctx.beginPath();
      m.pts.forEach(([x, y], i) => (i ? ctx.lineTo(x, y) : ctx.moveTo(x, y)));
      ctx.stroke();
      break;
    }
    case 'line':
    case 'arrow': {
      ctx.beginPath();
      ctx.moveTo(a[0], a[1]);
      ctx.lineTo(b[0], b[1]);
      ctx.stroke();
      if (m.tool === 'arrow') {
        const ang = Math.atan2(b[1] - a[1], b[0] - a[0]);
        const len = 12 * k + w * 3;
        ctx.beginPath();
        ctx.moveTo(b[0] - Math.cos(ang - 0.45) * len, b[1] - Math.sin(ang - 0.45) * len);
        ctx.lineTo(b[0], b[1]);
        ctx.lineTo(b[0] - Math.cos(ang + 0.45) * len, b[1] - Math.sin(ang + 0.45) * len);
        ctx.stroke();
      }
      break;
    }
    case 'rect':
      ctx.strokeRect(Math.min(a[0], b[0]), Math.min(a[1], b[1]), Math.abs(b[0] - a[0]), Math.abs(b[1] - a[1]));
      break;
    case 'ellipse':
      ctx.beginPath();
      ctx.ellipse((a[0] + b[0]) / 2, (a[1] + b[1]) / 2, Math.abs(b[0] - a[0]) / 2, Math.abs(b[1] - a[1]) / 2, 0, 0, Math.PI * 2);
      ctx.stroke();
      break;
    case 'text': {
      const size = (14 + m.width * 3) * k;
      ctx.font = `600 ${size}px "Geist Variable", system-ui, sans-serif`;
      ctx.textBaseline = 'top';
      ctx.lineWidth = Math.max(2, size / 6);
      ctx.strokeStyle = MARKUP_TEXT_HALO;
      (m.text ?? '').split('\n').forEach((line, i) => {
        ctx.strokeText(line, a[0], a[1] + i * size * 1.25);
        ctx.fillText(line, a[0], a[1] + i * size * 1.25);
      });
      break;
    }
  }
  ctx.restore();
}

export default function MarkupEditor({ ctl, nodeId, onClose }: { ctl: BoardController; nodeId: string; onClose: () => void }) {
  const node = ctl.node(nodeId);
  const d = node ? readImage(node) : null;
  const url = d ? imageSrcUrl(d.src) : '';
  const canvasRef = useRef<HTMLCanvasElement>(null);
  const wrapRef = useRef<HTMLDivElement>(null);
  const [img, setImg] = useState<HTMLImageElement | null>(null);
  const [loadError, setLoadError] = useState('');
  const [marks, setMarks] = useState<Mark[]>([]);
  const [draft, setDraft] = useState<Mark | null>(null);
  const [tool, setTool] = useState<MTool>('arrow');
  const [color, setColor] = useState(MARKUP_COLORS[0]);
  const [width, setWidth] = useState<number>(MARKUP_WIDTHS[1]);
  const [textAt, setTextAt] = useState<{ x: number; y: number; nx: number; ny: number } | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [scale, setScale] = useState(1); // natural px per display px

  useEffect(() => {
    if (!url) return;
    const im = new Image();
    im.onload = () => setImg(im);
    im.onerror = () => setLoadError('The image could not be loaded.');
    im.src = url;
  }, [url]);

  // canvas at (capped) natural resolution, CSS-scaled to fit the dialog
  const dims = img ? (() => {
    const k = Math.min(1, MAX_EXPORT / Math.max(img.naturalWidth, img.naturalHeight));
    return { w: Math.round(img.naturalWidth * k), h: Math.round(img.naturalHeight * k) };
  })() : null;

  useLayoutEffect(() => {
    const c = canvasRef.current;
    if (!c || !img || !dims) return;
    const ctx = c.getContext('2d');
    if (!ctx) return;
    ctx.clearRect(0, 0, dims.w, dims.h);
    ctx.drawImage(img, 0, 0, dims.w, dims.h);
    const k = c.width / Math.max(1, c.getBoundingClientRect().width || c.width);
    for (const m of marks) drawMark(ctx, m, k);
    if (draft) drawMark(ctx, draft, k);
    setScale(k);
  });

  const toCanvas = (e: { clientX: number; clientY: number }): [number, number] => {
    const c = canvasRef.current!;
    const r = c.getBoundingClientRect();
    return [((e.clientX - r.left) / r.width) * c.width, ((e.clientY - r.top) / r.height) * c.height];
  };

  const onDown = (e: ReactPointerEvent<HTMLCanvasElement>) => {
    if (e.button !== 0 || !img) return;
    const p = toCanvas(e);
    if (tool === 'text') {
      const wr = wrapRef.current!.getBoundingClientRect();
      setTextAt({ x: e.clientX - wr.left, y: e.clientY - wr.top, nx: p[0], ny: p[1] });
      return;
    }
    e.currentTarget.setPointerCapture(e.pointerId);
    setDraft({ tool, color, width, pts: [p, p] });
  };
  const onMove = (e: ReactPointerEvent<HTMLCanvasElement>) => {
    if (!draft) return;
    const p = toCanvas(e);
    setDraft(draft.tool === 'pen' || draft.tool === 'highlight' ? { ...draft, pts: [...draft.pts, p] } : { ...draft, pts: [draft.pts[0], p] });
  };
  const onUp = () => {
    if (!draft) return;
    const [a, b] = [draft.pts[0], draft.pts[draft.pts.length - 1]];
    if (draft.pts.length > 2 || Math.hypot(b[0] - a[0], b[1] - a[1]) > 3 * scale) setMarks((m) => [...m, draft]);
    setDraft(null);
  };

  const commitText = (text: string) => {
    if (textAt && text.trim()) setMarks((m) => [...m, { tool: 'text', color, width, pts: [[textAt.nx, textAt.ny]], text: text.trim() }]);
    setTextAt(null);
  };

  const save = async () => {
    const c = canvasRef.current;
    if (!c || !node || !img) return;
    setSaving(true);
    setError('');
    try {
      const blob = await new Promise<Blob>((resolve, reject) => c.toBlob((b) => (b ? resolve(b) : reject(new Error('Could not export the image'))), 'image/png'));
      const asset = await api.uploadAsset(blob, 'image/png');
      const fresh = ctl.make('image', { x: node.x + node.w + 40, y: node.y, w: node.w, h: node.h }, {
        src: { asset: asset.id },
        alt: `${d?.alt || 'Image'} (markup)`,
        naturalW: c.width,
        naturalH: c.height,
      });
      ctl.addNodes([fresh]);
      onClose();
    } catch (err) {
      setError(errorMessage(err));
      setSaving(false);
    }
  };

  return (
    <Modal
      title="Markup"
      icon="pen"
      size="lg"
      className="markup-modal"
      onClose={() => !saving && onClose()}
      dismissable={false}
      help="Saving creates a new image next to the original."
      footer={
        <>
          <Button variant="secondary" onClick={onClose} disabled={saving}>
            Cancel
          </Button>
          <Button variant="primary" icon="check" loading={saving} disabled={!img || marks.length === 0} onClick={() => void save()}>
            Save markup
          </Button>
        </>
      }
    >
      <div className="markup-bar">
        <div className="segmented sm" role="radiogroup" aria-label="Markup tool">
          {TOOLS.map((t) => (
            <button key={t.tool} type="button" role="radio" aria-checked={tool === t.tool} className="segmented-item" title={t.label} aria-label={t.label} onClick={() => setTool(t.tool)}>
              <Icon name={t.icon} />
            </button>
          ))}
        </div>
        <div className="swatch-row" role="radiogroup" aria-label="Colour">
          {MARKUP_COLORS.map((c) => (
            <button key={c} type="button" role="radio" aria-checked={color === c} aria-label={c} title={c} className="swatch-btn" style={{ '--swatch': c } as CSSProperties} onClick={() => setColor(c)} />
          ))}
        </div>
        <div className="width-row" role="radiogroup" aria-label="Width">
          {MARKUP_WIDTHS.map((w) => (
            <button key={w} type="button" role="radio" aria-checked={width === w} aria-label={`Width ${w}`} title={`Width ${w}`} className="width-btn" onClick={() => setWidth(w)}>
              <span style={{ width: Math.min(16, w + 2), height: Math.min(16, w + 2), background: color }} />
            </button>
          ))}
        </div>
        <span className="spacer" />
        <Button size="sm" variant="ghost" icon="undo" disabled={marks.length === 0} onClick={() => setMarks((m) => m.slice(0, -1))}>
          Undo
        </Button>
        <Button size="sm" variant="ghost" icon="broom" disabled={marks.length === 0} onClick={() => setMarks([])}>
          Clear all
        </Button>
      </div>
      <div className="markup-stage" ref={wrapRef}>
        {loadError && (
          <div className="alert" role="alert">
            <Icon name="alertCircle" />
            <span>{loadError}</span>
          </div>
        )}
        {!img && !loadError && (
          <div className="list-msg" role="status">
            <span className="spinner" aria-hidden="true" /> Loading image…
          </div>
        )}
        {img && dims && (
          <canvas
            ref={canvasRef}
            width={dims.w}
            height={dims.h}
            className={`markup-canvas tool-${tool}`}
            onPointerDown={onDown}
            onPointerMove={onMove}
            onPointerUp={onUp}
            aria-label="Image to mark up"
            role="img"
          />
        )}
        {textAt && (
          <input
            className="control control-sm markup-text"
            data-keep-escape
            style={{ left: textAt.x, top: textAt.y, color }}
            autoFocus
            placeholder="Type, then Enter"
            aria-label="Markup text"
            onKeyDown={(e) => {
              e.stopPropagation();
              if (e.key === 'Enter') commitText(e.currentTarget.value);
              if (e.key === 'Escape') setTextAt(null);
            }}
            onBlur={(e) => commitText(e.currentTarget.value)}
          />
        )}
      </div>
      {error && (
        <div className="alert mt-4" role="alert">
          <Icon name="alertCircle" />
          <span>{error}</span>
        </div>
      )}
    </Modal>
  );
}
