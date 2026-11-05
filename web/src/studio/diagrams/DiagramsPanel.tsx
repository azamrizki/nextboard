// Diagrams panel (E3, docs/API-studio.md §6): ask your CLI for a diagram of the project
// (subject, effort, ink or colour), or drop a starter. Results land on the board as loose
// shapes (one undo step); earlier diagrams can land again or be exported (PNG 2×,
// Excalidraw) from their as-generated JSON. Arrow style and arrow colour apply to whatever
// lands or exports next, an earlier diagram too, and are remembered in this browser.

import { useCallback, useEffect, useState } from 'react';
import { errorMessage } from '../../api';
import { useEffectiveSettings } from '../../agents/settings';
import type { BoardController } from '../../board/controller';
import { Button } from '../../components/Button';
import { SegmentedControl } from '../../components/form';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { prefs, timeAgo } from '../../lib/format';
import type { Project } from '../../lib/types';
import { orchStore } from '../../orch/store';
import { diagramApi } from '../api';
import { download, fileSafe } from '../documents/model';
import { useStudio } from '../store';
import { DIAGRAM_SUBJECTS, type ArrowColor, type ArrowStyle, type DiagramListItem, type DiagramStyle, type DiagramSubject, type Effort } from '../types';
import { diagramExcalidraw, diagramPng } from './export';
import { landDiagram, type Reveal } from './land';
import { diagramRuns } from './runs';
import { STARTERS } from './starters';

export const SUBJECT_LABEL: Record<DiagramSubject, string> = {
  architecture: 'Architecture',
  userFlow: 'User flow',
  dataFlow: 'Data flow',
  components: 'Components',
  requestLifecycle: 'Request lifecycle',
  stateFlow: 'State flow',
  custom: 'Ask for anything',
};
const EFFORT_PARTS: Record<Effort, string> = { quick: '6–10 parts', balanced: '8–14 parts', thorough: '10–16 parts' };

interface Props {
  ctl: BoardController;
  project: Project;
  viewCenter: () => { x: number; y: number };
  reveal: Reveal;
  onClose: () => void;
}

export default function DiagramsPanel({ ctl, project, viewCenter, reveal, onClose }: Props) {
  const settings = useEffectiveSettings();
  const runnerDefault = (settings.runner as { defaultAgent?: string } | undefined)?.defaultAgent ?? 'claude';
  const [subject, setSubject] = useState<DiagramSubject>('architecture');
  const [ask, setAsk] = useState('');
  const [effort, setEffort] = useState<Effort>('balanced');
  // every launch starts on Ink (E3)
  const [style, setStyle] = useState<DiagramStyle>('ink');
  const [arrows, setArrows] = useState<ArrowStyle>(() => (prefs.get('diagram.arrows') === 'wiring' ? 'wiring' : 'curved'));
  const [arrowColor, setArrowColor] = useState<ArrowColor>(() => (prefs.get('diagram.arrowColor') === 'color' ? 'color' : 'mono'));
  const look = { arrows, arrowColor };
  const [agent, setAgent] = useState(runnerDefault);
  const [busy, setBusy] = useState(false);
  const [items, setItems] = useState<DiagramListItem[] | null>(null);
  const rev = useStudio((s) => s.diagramRev[project.id] ?? 0);

  const load = useCallback(() => {
    diagramApi
      .list(project.id)
      .then((l) => setItems([...l].sort((a, b) => b.createdAt.localeCompare(a.createdAt))))
      .catch(() => setItems([]));
  }, [project.id]);
  useEffect(load, [load, rev]);

  useEffect(() => {
    const esc = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && !document.querySelector('.modal-layer') && !(e.target as HTMLElement)?.closest?.('input, textarea, select')) onClose();
    };
    document.addEventListener('keydown', esc);
    return () => document.removeEventListener('keydown', esc);
  }, [onClose]);

  const generate = async () => {
    setBusy(true);
    const request = { subject, ask: subject === 'custom' ? ask.trim() : undefined, effort, style, agent };
    try {
      const r = await diagramApi.generate(project.id, request);
      orchStore.upsertJob(r.job);
      diagramRuns.add({ diagramId: r.diagramId, jobId: r.job.id, projectId: project.id, workspaceId: ctl.workspaceId, style, look, label: SUBJECT_LABEL[subject], request });
      toast.info('Drawing… the diagram lands on the board when it is ready.');
      load();
    } catch (err) {
      toast.error(`Could not start the diagram: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };

  const landAgain = async (it: DiagramListItem) => {
    try {
      const rec = await diagramApi.get(it.id);
      if (!rec.diagram) throw new Error('this diagram has no result');
      landDiagram(ctl, rec.diagram, (rec.style as DiagramStyle) || 'ink', rec.id, viewCenter(), reveal, look);
      toast.success('Diagram added. Ctrl+Z removes it again.');
    } catch (err) {
      toast.error(`Could not add the diagram: ${errorMessage(err)}`);
    }
  };

  const exportAs = async (it: DiagramListItem, kind: 'png' | 'excalidraw') => {
    try {
      const rec = await diagramApi.get(it.id);
      if (!rec.diagram) throw new Error('this diagram has no result');
      const name = fileSafe(rec.diagram.title || rec.title || 'diagram');
      if (kind === 'png') {
        const blob = await diagramPng(rec.diagram, (rec.style as DiagramStyle) || 'ink', look);
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = `${name}.png`;
        a.click();
        window.setTimeout(() => URL.revokeObjectURL(url), 1000);
      } else download(`${name}.excalidraw`, diagramExcalidraw(rec.diagram, (rec.style as DiagramStyle) || 'ink', look), 'application/json');
    } catch (err) {
      toast.error(`Export failed: ${errorMessage(err)}`);
    }
  };

  return (
    <aside className="side-panel diagrams-panel" aria-label="Diagrams">
      <div className="side-panel-head">
        <Icon name="diagram" />
        <h2>Diagrams</h2>
        <span className="spacer" />
        <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Close diagrams" onClick={onClose} />
      </div>
      <p className="side-panel-hint">A hidden read-only run of your CLI reads {project.name} and draws it. The result lands as loose shapes you can edit.</p>

      <section className="memory-section" aria-label="Generate">
        <div className="chip-grid" role="radiogroup" aria-label="Subject">
          {DIAGRAM_SUBJECTS.map((s) => (
            <button key={s} type="button" role="radio" aria-checked={subject === s} className={`pick-chip ${subject === s ? 'is-active' : ''}`} onClick={() => setSubject(s)}>
              {SUBJECT_LABEL[s]}
            </button>
          ))}
        </div>
        {subject === 'custom' && (
          <textarea className="control diagram-ask" aria-label="What should the diagram show?" placeholder="How does a login travel through the app?" value={ask} maxLength={1000} onChange={(e) => setAsk(e.target.value)} />
        )}
        <SegmentedControl<Effort>
          value={effort}
          onChange={setEffort}
          size="sm"
          block
          label="Effort"
          options={(['quick', 'balanced', 'thorough'] as Effort[]).map((e) => ({ value: e, label: EFFORT_PARTS[e] }))}
        />
        <div className="diagram-look">
          <span id="diagram-shapes" className="fs-xs text-3">
            Shapes
          </span>
          <SegmentedControl<DiagramStyle>
            value={style}
            onChange={setStyle}
            size="sm"
            block
            labelledBy="diagram-shapes"
            options={[
              { value: 'ink', label: 'Ink' },
              { value: 'color', label: 'Colorful' },
            ]}
          />
          <span id="diagram-arrows" className="fs-xs text-3">
            Arrows
          </span>
          <SegmentedControl<ArrowStyle>
            value={arrows}
            onChange={(v) => {
              setArrows(v);
              prefs.set('diagram.arrows', v);
            }}
            size="sm"
            block
            labelledBy="diagram-arrows"
            options={[
              { value: 'curved', label: 'Curved' },
              { value: 'wiring', label: 'Wiring' },
            ]}
          />
          <span id="diagram-arrow-color" className="fs-xs text-3">
            Arrow color
          </span>
          <SegmentedControl<ArrowColor>
            value={arrowColor}
            onChange={(v) => {
              setArrowColor(v);
              prefs.set('diagram.arrowColor', v);
            }}
            size="sm"
            block
            labelledBy="diagram-arrow-color"
            options={[
              { value: 'mono', label: 'Black & white' },
              { value: 'color', label: 'Colorful' },
            ]}
          />
        </div>
        <div className="row-gap wrap">
          <select className="control control-sm diagram-agent" aria-label="Run with" value={agent} onChange={(e) => setAgent(e.target.value)}>
            <option value="claude">Claude Code</option>
            <option value="codex">Codex</option>
          </select>
        </div>
        <div>
          <Button size="sm" variant="primary" icon="sparkle" loading={busy} disabled={subject === 'custom' && !ask.trim()} onClick={() => void generate()}>
            Draw diagram
          </Button>
        </div>
      </section>

      <section className="memory-section" aria-label="Starters">
        <h3 className="section-title">Starters · no AI</h3>
        <div className="starter-grid">
          {STARTERS.map((s) => (
            <button
              key={s.id}
              type="button"
              className="starter"
              onClick={() => {
                landDiagram(ctl, s.diagram, style, `starter:${s.id}`, viewCenter(), reveal, look);
                toast.success(`${s.label} added.`);
              }}
            >
              <span className="starter-title">{s.label}</span>
              <span className="fs-xs text-3">{s.hint}</span>
            </button>
          ))}
        </div>
      </section>

      <section className="memory-section" aria-label="Earlier diagrams">
        <h3 className="section-title">Earlier diagrams</h3>
        {!items ? (
          <p className="list-msg">Loading…</p>
        ) : !items.length ? (
          <p className="list-msg">None yet.</p>
        ) : (
          <ul className="plain diagram-list">
            {items.map((it) => (
              <li key={it.id} className="diagram-row">
                <div className="grow">
                  <div className="truncate fs-sm">{it.title || SUBJECT_LABEL[(it.subject as DiagramSubject) ?? 'custom'] || 'Diagram'}</div>
                  <div className="fs-xs text-3">
                    {it.state === 'running' ? 'Drawing…' : it.state === 'failed' ? 'Failed' : it.state === 'cancelled' ? 'Stopped' : it.agent === 'codex' ? 'Codex' : 'Claude Code'} · {timeAgo(it.createdAt)}
                  </div>
                </div>
                {(!it.state || it.state === 'done') && (
                  <>
                    <Button size="sm" variant="ghost" iconOnly icon="plus" aria-label="Add to board" title="Add to board" onClick={() => void landAgain(it)} />
                    <Button size="sm" variant="ghost" iconOnly icon="image" aria-label="Export PNG" title="Export PNG (2×)" onClick={() => void exportAs(it, 'png')} />
                    <Button size="sm" variant="ghost" iconOnly icon="download" aria-label="Export Excalidraw" title="Export Excalidraw" onClick={() => void exportAs(it, 'excalidraw')} />
                  </>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>
    </aside>
  );
}
