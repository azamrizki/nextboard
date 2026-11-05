// Team composer dialog (docs/API-orchestration.md §5, D14/C2): Spawn 4, Group, "Add consoles" to
// a group, or agents for an orchestrator card. The form is the one the board's right-click ring
// shows; a new team goes right of the board's content, or under its group.

import type { BoardController } from '../../board/controller';
import { readGroup, readOrchestrator } from '../../board/kinds';
import type { ComposerOpts } from '../../board/ui';
import { Modal } from '../../components/Modal';
import type { Project } from '../../lib/types';
import ComposeForm from './ComposeForm';

export default function TeamComposer({
  ctl,
  project,
  opts,
  onClose,
  onSpawned,
}: {
  ctl: BoardController;
  project: Project;
  opts: ComposerOpts;
  onClose: () => void;
  onSpawned: (sessionIds: string[]) => void;
}) {
  const existing = opts.groupId ? ctl.node(opts.groupId) : undefined;
  const card = opts.orchestratorId ? ctl.node(opts.orchestratorId) : undefined;
  const title = existing ? `Add consoles to ${readGroup(existing).name}` : card ? `Agents for ${readOrchestrator(card).title}` : opts.group ? 'Compose group' : 'Spawn consoles';

  return (
    <Modal title={title} icon={opts.group || existing ? 'group' : 'users'} size="sm" className="composer-modal" onClose={onClose} help={<span>Each console gets its own handle (@Arkan, @Bintang, …).</span>}>
      <ComposeForm ctl={ctl} project={project} opts={opts} onClose={onClose} onSpawned={onSpawned} />
    </Modal>
  );
}
