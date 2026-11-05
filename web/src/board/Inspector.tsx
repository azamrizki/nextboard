// Style inspector next to the tool rail: pen settings while drawing, otherwise the
// style of the selected shapes / lines / text / notes / strokes (applied to all of them).

import { useState, type CSSProperties } from 'react';
import { Button } from '../components/Button';
import { SegmentedControl } from '../components/form';
import { Icon } from '../components/Icon';
import type { BoardNode, Op } from '../lib/types';
import { useMediaQuery } from '../lib/useMediaQuery';
import { duplicate } from './clipboard';
import type { BoardController } from './controller';
import { ALIGNS, FONT_LABEL, FONTS, readInk, readLine, readNote, readShape, readText, SIZES, type AlignName } from './kinds';
import { FILL_COLORS, inkColor, INK_COLORS, INK_WIDTHS, NOTE_COLORS, type Swatch } from './palette';
import type { UiStore } from './ui';

const STYLED = new Set(['shape', 'line', 'text', 'note', 'ink']);

function Swatches({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: { value: string; label: string; color: string }[];
  onChange: (v: string) => void;
}) {
  return (
    <div className="insp-row">
      <span className="insp-label" id={`insp-${label}`}>
        {label}
      </span>
      <div className="swatch-row" role="radiogroup" aria-labelledby={`insp-${label}`}>
        {options.map((o) => (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={value === o.value}
            aria-label={o.label}
            title={o.label}
            className={`swatch-btn ${o.value === 'transparent' ? 'is-none' : ''}`}
            style={{ '--swatch': o.color } as CSSProperties}
            onClick={() => onChange(o.value)}
          />
        ))}
      </div>
    </div>
  );
}

const hex = (s: Swatch[]) => s.map((c) => ({ value: c.value, label: c.label, color: c.value === 'transparent' ? 'transparent' : inkColor(c.value) }));
const NOTE_SW = NOTE_COLORS.map((c) => ({ value: c, label: c[0].toUpperCase() + c.slice(1), color: `var(--note-${c})` }));

function Seg<T extends string>({ label, value, options, onChange }: { label: string; value: T; options: { value: T; label: string; icon?: 'alignLeft' | 'alignCenter' | 'alignRight' }[]; onChange: (v: T) => void }) {
  return (
    <div className="insp-row">
      <span className="insp-label">{label}</span>
      <SegmentedControl<T>
        label={label}
        size="sm"
        block
        value={value}
        onChange={onChange}
        options={options.map((o) => ({ value: o.value, label: o.icon ? <Icon name={o.icon} label={o.label} /> : o.label, title: o.label }))}
      />
    </div>
  );
}

export function Inspector({ ctl, ui, selection, tool, penColor, penWidth }: {
  ctl: BoardController;
  ui: UiStore;
  selection: BoardNode[];
  tool: string;
  penColor: string;
  penWidth: number;
}) {
  const compact = useMediaQuery('(max-width: 719px)');
  const [open, setOpen] = useState(!compact);
  const styled = selection.filter((n) => STYLED.has(n.kind));
  const pen = tool === 'pen';
  if (!pen && (styled.length === 0 || styled.length !== selection.length)) return null;

  if (compact && !open) {
    return (
      <button type="button" className="insp-toggle" onClick={() => setOpen(true)} aria-expanded={false}>
        <Icon name="palette" />
        Style
      </button>
    );
  }

  const ids = styled.map((n) => n.id);
  const of = (kind: string) => styled.filter((n) => n.kind === kind);
  const shapes = of('shape');
  const lines = of('line');
  const texts = of('text');
  const notes = of('note');
  const inks = of('ink');
  const stroked = [...shapes, ...lines];
  const mergeKey = `style:${ids.join(',')}`;

  const setData = (nodes: BoardNode[], data: Record<string, unknown>, merge?: boolean) => {
    const ops: Op[] = nodes.map((n) => ({ op: 'patch', id: n.id, set: { data } }));
    ctl.apply(ops, merge ? { merge: mergeKey } : undefined);
  };

  const s0 = shapes[0] ? readShape(shapes[0]) : null;
  const l0 = lines[0] ? readLine(lines[0]) : null;
  const st = s0 ?? l0;
  const t0 = texts[0] ? readText(texts[0]) : null;
  // shapes show text options once they have a label (double-click a shape to add one)
  const labelled = shapes.filter((n) => readShape(n).text.trim());
  const ls0 = labelled[0] ? readShape(labelled[0]) : null;
  const textish = t0 ?? (ls0 ? { font: ls0.font, size: ls0.size, align: ls0.align } : null);
  const textNodes = [...texts, ...labelled];

  return (
    <aside className="inspector" aria-label={pen ? 'Pen settings' : 'Style'}>
      <div className="insp-head">
        <h2>{pen ? 'Pen' : 'Style'}</h2>
        {compact && <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Hide style panel" onClick={() => setOpen(false)} />}
      </div>

      {pen && (
        <>
          <Swatches label="Colour" value={penColor} options={hex(INK_COLORS)} onChange={(v) => ui.set({ penColor: v })} />
          <div className="insp-row">
            <span className="insp-label">Width</span>
            <div className="width-row" role="radiogroup" aria-label="Pen width">
              {INK_WIDTHS.map((w) => (
                <button
                  key={w}
                  type="button"
                  role="radio"
                  aria-checked={penWidth === w}
                  aria-label={`${w} px`}
                  title={`${w} px`}
                  className="width-btn"
                  onClick={() => ui.set({ penWidth: w })}
                >
                  <span style={{ width: Math.min(18, w + 2), height: Math.min(18, w + 2), background: inkColor(penColor) } as CSSProperties} />
                </button>
              ))}
            </div>
          </div>
        </>
      )}

      {!pen && st && (
        <>
          <Swatches label="Stroke" value={st.stroke} options={hex(INK_COLORS)} onChange={(v) => setData(stroked, { stroke: v })} />
          <Seg
            label="Width"
            value={String(st.strokeWidth)}
            options={[
              { value: '1', label: 'Thin' },
              { value: '2', label: 'Bold' },
              { value: '4', label: 'Heavy' },
            ]}
            onChange={(v) => setData(stroked, { strokeWidth: Number(v) })}
          />
          <Seg
            label="Line style"
            value={st.strokeStyle}
            options={[
              { value: 'solid', label: 'Solid' },
              { value: 'dashed', label: 'Dashed' },
              { value: 'dotted', label: 'Dotted' },
            ]}
            onChange={(v) => setData(stroked, { strokeStyle: v })}
          />
          <Seg
            label="Roughness"
            value={String(st.roughness)}
            options={[
              { value: '0', label: 'Smooth' },
              { value: '1', label: 'Sketch' },
              { value: '2', label: 'Loose' },
            ]}
            onChange={(v) => setData(stroked, { roughness: Number(v) })}
          />
        </>
      )}

      {!pen && s0 && (
        <>
          <Swatches label="Fill" value={s0.fill} options={hex(FILL_COLORS)} onChange={(v) => setData(shapes, { fill: v })} />
          {s0.fill !== 'transparent' && (
            <Seg
              label="Fill style"
              value={s0.fillStyle}
              options={[
                { value: 'hachure', label: 'Hatch' },
                { value: 'cross-hatch', label: 'Cross' },
                { value: 'solid', label: 'Solid' },
              ]}
              onChange={(v) => setData(shapes, { fillStyle: v })}
            />
          )}
          {shapes.some((n) => readShape(n).shape !== 'ellipse') && (
            <Seg
              label="Corners"
              value={s0.edges}
              options={[
                { value: 'sharp', label: 'Sharp' },
                { value: 'round', label: 'Round' },
              ]}
              onChange={(v) => setData(shapes, { edges: v })}
            />
          )}
        </>
      )}

      {!pen && l0 && (
        <div className="insp-row">
          <span className="insp-label">Arrowheads</span>
          <div className="segmented sm block" role="group" aria-label="Arrowheads">
            <button type="button" className="segmented-item" aria-pressed={l0.arrowStart} onClick={() => setData(lines, { arrowStart: !l0.arrowStart })}>
              Start
            </button>
            <button type="button" className="segmented-item" aria-pressed={l0.arrowEnd} onClick={() => setData(lines, { arrowEnd: !l0.arrowEnd })}>
              End
            </button>
          </div>
        </div>
      )}

      {!pen && st && (
        <div className="insp-row">
          <label className="insp-label" htmlFor="insp-opacity">
            Opacity <span className="tabular">{st.opacity}%</span>
          </label>
          <input
            id="insp-opacity"
            type="range"
            className="range"
            min={10}
            max={100}
            step={5}
            value={st.opacity}
            onChange={(e) => setData(stroked, { opacity: Number(e.target.value) }, true)}
          />
        </div>
      )}

      {!pen && textish && textNodes.length > 0 && (
        <>
          <Seg label="Font" value={textish.font} options={FONTS.map((f) => ({ value: f, label: FONT_LABEL[f] }))} onChange={(v) => setData(textNodes, { font: v })} />
          <Seg label="Size" value={textish.size} options={SIZES.map((s) => ({ value: s, label: s.toUpperCase() }))} onChange={(v) => setData(textNodes, { size: v })} />
          <Seg<AlignName>
            label="Align"
            value={textish.align}
            options={ALIGNS.map((a) => ({
              value: a,
              label: a === 'left' ? 'Align left' : a === 'center' ? 'Centre' : 'Align right',
              icon: a === 'left' ? 'alignLeft' : a === 'center' ? 'alignCenter' : 'alignRight',
            }))}
            onChange={(v) => setData(textNodes, { align: v })}
          />
        </>
      )}

      {!pen && t0 && <Swatches label="Text colour" value={t0.color} options={hex(INK_COLORS)} onChange={(v) => setData(texts, { color: v })} />}

      {!pen && notes.length > 0 && (
        <Swatches label="Paper" value={readNote(notes[0]).color} options={NOTE_SW} onChange={(v) => setData(notes, { color: v })} />
      )}

      {!pen && inks.length > 0 && (
        <>
          <Swatches label="Ink" value={readInk(inks[0]).color} options={hex(INK_COLORS)} onChange={(v) => setData(inks, { color: v })} />
          <Seg
            label="Width"
            value={String(readInk(inks[0]).width)}
            options={INK_WIDTHS.map((w) => ({ value: String(w), label: String(w) }))}
            onChange={(v) => setData(inks, { width: Number(v) })}
          />
        </>
      )}

      {!pen && (
        <div className="insp-row">
          <span className="insp-label">Arrange</span>
          <div className="insp-actions">
            <Button size="sm" variant="ghost" iconOnly icon="front" aria-label="Bring to front" title="Bring to front" onClick={() => ctl.zOrder(ids, 'front')} />
            <Button size="sm" variant="ghost" iconOnly icon="back" aria-label="Send to back" title="Send to back" onClick={() => ctl.zOrder(ids, 'back')} />
            <Button size="sm" variant="ghost" iconOnly icon="duplicate" aria-label="Duplicate" title="Duplicate (Ctrl+D)" onClick={() => duplicate(ctl, ids)} />
            <Button size="sm" variant="danger-ghost" iconOnly icon="trash" aria-label="Delete" title="Delete (Del)" onClick={() => ctl.deleteNodes(ids)} />
          </div>
        </div>
      )}
    </aside>
  );
}
