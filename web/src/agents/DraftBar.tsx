// Thin bar under a console header (ROADMAP #50, docs/API-agents.md §28): what was typed at an
// agent's prompt before it slept, when the hub could not type it back on waking. Copy it, or
// let it go.

import { api, errorMessage } from '../api';
import { Button } from '../components/Button';
import { Icon } from '../components/Icon';
import { toast } from '../components/Toast';
import { copyText } from '../lib/clipboard';
import type { Session } from '../lib/types';
import { store } from '../store';

export function DraftBar({ session }: { session: Session }) {
  const draft = session.unsentDraft;
  if (!draft) return null;
  const dismiss = () =>
    void api
      .dismissDraft(session.id)
      .then((s) => store.upsertSession(s))
      .catch((err) => toast.error(`Could not dismiss it: ${errorMessage(err)}`));
  return (
    <div className="agent-bar is-offer nodrag" role="region" aria-label="Unsent text">
      <Icon name="fileText" />
      <span className="agent-bar-text truncate" title={draft}>
        What you had typed before it slept could not be put back: “{draft.length > 80 ? `${draft.slice(0, 80)}…` : draft}”
      </span>
      <Button
        size="sm"
        variant="primary"
        onClick={() =>
          void copyText(draft).then((ok) => (ok ? toast.success('Copied. Paste it at the prompt.') : toast.error('Could not copy: select it in the tooltip instead.')))
        }
      >
        Copy
      </Button>
      <Button size="sm" variant="ghost" onClick={dismiss}>
        Dismiss
      </Button>
    </div>
  );
}
