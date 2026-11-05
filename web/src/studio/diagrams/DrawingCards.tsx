// "Drawing" cards (E3): one per diagram run of this client, on top of the board. Stop
// ends the run; when it finishes the diagram lands next to the view centre as shapes;
// an unusable result offers Try again.

import { useEffect, useRef } from 'react';
import { errorMessage } from '../../api';
import type { BoardController } from '../../board/controller';
import { Button } from '../../components/Button';
import { Icon } from '../../components/Icon';
import { toast } from '../../components/Toast';
import { useNow } from '../../orch/hooks';
import { orchStore } from '../../orch/store';
import { diagramApi, jobApi } from '../api';
import { jobActive, useJob } from '../store';
import type { DiagramSubject, Effort } from '../types';
import { landDiagram, type Reveal } from './land';
import { diagramRuns, useDiagramRuns, type DiagramRun } from './runs';

export default function DrawingCards({ ctl, viewCenter, reveal }: { ctl: BoardController; viewCenter: () => { x: number; y: number }; reveal: Reveal }) {
  const runs = useDiagramRuns(ctl.workspaceId);
  if (!runs.length) return null;
  return (
    <div className="drawing-cards" aria-label="Diagrams being drawn">
      {runs.map((r) => (
        <DrawingCard key={r.diagramId} run={r} ctl={ctl} viewCenter={viewCenter} reveal={reveal} />
      ))}
    </div>
  );
}

const PHASE: Record<string, string> = { launching: 'Starting', thinking: 'Reading the project', writing: 'Drawing', finishing: 'Finishing' };

function DrawingCard({ run, ctl, viewCenter, reveal }: { run: DiagramRun; ctl: BoardController; viewCenter: () => { x: number; y: number }; reveal: Reveal }) {
  const job = useJob(run.jobId);
  const active = !job || jobActive(job);
  const now = useNow(1000, active);
  const landed = useRef(false);

  useEffect(() => {
    if (!job || active || landed.current) return;
    if (job.state === 'cancelled') {
      diagramRuns.remove(run.diagramId);
      return;
    }
    if (job.state !== 'done') return;
    landed.current = true;
    diagramApi
      .get(run.diagramId)
      .then((rec) => {
        if (!rec.diagram) throw new Error(rec.error || 'the CLI gave no diagram');
        landDiagram(ctl, rec.diagram, run.style, rec.id, viewCenter(), reveal, run.look);
        toast.success(`${rec.diagram.title || 'Diagram'} added: ${rec.diagram.nodes.length} parts. Ctrl+Z removes it.`);
        if (rec.warnings?.length) toast.info(rec.warnings[0]);
        diagramRuns.remove(run.diagramId);
      })
      .catch((err) => {
        landed.current = false;
        toast.error(`The diagram could not land: ${errorMessage(err)}`);
        diagramRuns.remove(run.diagramId);
      });
  }, [job, active, run, ctl, viewCenter, reveal]);

  const stop = () => {
    void jobApi
      .cancel(run.jobId)
      .then((j) => orchStore.upsertJob(j))
      .catch(() => undefined);
  };
  const retry = async () => {
    try {
      const r = await diagramApi.generate(run.projectId, { ...run.request, subject: run.request.subject as DiagramSubject, effort: run.request.effort as Effort });
      orchStore.upsertJob(r.job);
      diagramRuns.remove(run.diagramId);
      diagramRuns.add({ ...run, diagramId: r.diagramId, jobId: r.job.id });
    } catch (err) {
      toast.error(`Could not start again: ${errorMessage(err)}`);
    }
  };
  const started = job?.startedAt ? Date.parse(job.startedAt) : job?.createdAt ? Date.parse(job.createdAt) : now;
  const failed = job?.state === 'failed';
  return (
    <div className={`drawing-card ${failed ? 'is-failed' : ''}`} role="status">
      <span className="drawing-art" aria-hidden="true">
        <Icon name="diagram" />
      </span>
      <div className="grow">
        <div className="fs-sm">{failed ? `Couldn’t draw the ${run.label.toLowerCase()} diagram` : `Drawing ${run.label.toLowerCase()}…`}</div>
        <div className="fs-xs text-3">
          {failed ? job?.code === 'bad_output' ? 'The CLI’s answer wasn’t a usable diagram.' : job?.error || job?.code : `${PHASE[job?.progress?.phase ?? ''] ?? 'Queued'} · ${Math.max(0, Math.round((now - started) / 1000))}s`}
        </div>
      </div>
      {failed ? (
        <>
          <Button size="sm" onClick={() => void retry()}>
            Try again
          </Button>
          <Button size="sm" variant="ghost" iconOnly icon="close" aria-label="Dismiss" onClick={() => diagramRuns.remove(run.diagramId)} />
        </>
      ) : (
        <Button size="sm" variant="ghost" icon="stop" onClick={stop}>
          Stop
        </Button>
      )}
    </div>
  );
}
