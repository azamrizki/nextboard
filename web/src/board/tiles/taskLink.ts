// A task board wired from an orchestrator card (docs/API-orchestration.md §3.10): who leads it,
// who can be given its tasks, whether the lead hands them out by itself.

import { useSyncExternalStore } from 'react';
import type { MenuEntry } from '../../components/Menu';
import type { Session } from '../../lib/types';
import { useStoreSessions } from '../../orch/hooks';
import { useLeadName } from '../../orch/lead';
import { directedSessions } from '../groups';
import { readOrchestrator } from '../kinds';
import { useBoardEnv } from '../ui';

const STATE_LABEL: Record<string, string> = {
  queued: 'Queued',
  working: 'Working',
  needsYou: 'Needs you',
  finished: 'Finished',
  failed: 'Failed',
};

export const taskStateLabel = (state?: string) => (state ? (STATE_LABEL[state] ?? state) : '');

export function useTaskLink(id: string, autoRun: boolean) {
  const { ctl } = useBoardEnv();
  const doc = useSyncExternalStore(ctl.sync.subscribe, () => ctl.sync.state);
  const sessions = useStoreSessions();
  const cards = doc.edges.flatMap((e) => {
    if (e.kind !== 'wire' || e.target !== id) return [];
    const n = doc.nodes.find((x) => x.id === e.source);
    return n?.kind === 'orchestrator' ? [n] : [];
  });
  // the agents inside an editor card wired to the lead are on the team too
  const team = cards
    .flatMap((c) => directedSessions(doc.nodes, doc.edges, c.id, sessions, { workspaceId: ctl.workspaceId, project: ctl.project }))
    .flatMap((sid) => (sessions[sid] ? [sessions[sid]] : []));
  const lead = useLeadName(cards[0] ? readOrchestrator(cards[0]).title : undefined);
  const menu: MenuEntry[] = cards.length
    ? [
        {
          label: `${lead} hands out these tasks`,
          icon: 'play',
          checked: autoRun,
          title: 'Whatever waits in To do goes to a free agent of the team, without asking first. Each task costs that agent a turn.',
          onSelect: () => void ctl.updateData(id, { autoRun: autoRun ? null : true }),
        },
      ]
    : [];
  return { linked: cards.length > 0, lead, team: uniqueSessions(team), menu };
}

function uniqueSessions(list: Session[]): Session[] {
  return [...new Map(list.map((s) => [s.id, s])).values()];
}
