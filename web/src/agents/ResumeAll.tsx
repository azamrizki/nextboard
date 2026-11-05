// "Resume all" banner (docs/API-agents.md §3.4, resumeOnRestart: ask): after a hub
// restart, consoles that dropped to the shell can pick their conversations up again.
// Subscribes on its own so the board canvas doesn't re-render on session updates.

import { useMemo, useState, useSyncExternalStore } from 'react';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { store } from '../store';
import { launchAgent, resumeMode } from './actions';

export function ResumeAllBanner({ workspaceId }: { workspaceId: string }) {
  const key = useSyncExternalStore(store.subscribe, () =>
    Object.values(store.getState().sessions)
      .filter((s) => s.workspaceId === workspaceId && s.resumeOffer && s.agentState === 'shell' && s.status === 'running')
      .map((s) => s.id)
      .sort()
      .join(','),
  );
  const ids = useMemo(() => (key ? key.split(',') : []), [key]);
  const [dismissed, setDismissed] = useState('');
  const [busy, setBusy] = useState(false);
  if (ids.length < 2 || dismissed === key) return null;

  const resumeAll = async () => {
    setBusy(true);
    const list = ids.map((id) => store.getState().sessions[id]).filter((s) => !!s);
    await Promise.all(list.map((s) => launchAgent(s, resumeMode(s).mode, resumeMode(s).agentSessionId)));
    setBusy(false);
  };

  return (
    <div className="resume-banner" role="region" aria-label="Resume conversations">
      <Icon name="resume" />
      <span>
        {ids.length} consoles can pick up the conversations they had before the hub restarted.
      </span>
      <Button size="sm" variant="primary" loading={busy} onClick={() => void resumeAll()}>
        Resume all
      </Button>
      <Button size="sm" variant="ghost" onClick={() => setDismissed(key)}>
        Not now
      </Button>
    </div>
  );
}
