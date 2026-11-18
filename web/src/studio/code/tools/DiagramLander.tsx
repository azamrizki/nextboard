// The code view's diagram runs (docs/API-studio.md §7.16): their Drawing cards, and the landing on
// the bound board when one finishes. Mounted with the page, not with the Diagrams tab, so a run
// that finishes while that tab is closed lands too. Nothing loads until a run starts.

import { lazy, Suspense } from 'react';
import type { BoardController } from '../../../board/controller';
import { useDiagramRuns } from '../../diagrams/runs';

const DrawingCards = lazy(() => import('../../diagrams/DrawingCards'));
const noReveal = () => undefined;

export default function DiagramLander({ ctl, viewCenter }: { ctl: BoardController; viewCenter: () => { x: number; y: number } }) {
  const runs = useDiagramRuns(ctl.workspaceId);
  if (!runs.length) return null;
  return (
    <Suspense fallback={null}>
      <DrawingCards ctl={ctl} viewCenter={viewCenter} reveal={noReveal} />
    </Suspense>
  );
}
