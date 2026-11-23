// The Databases tab of the code view (docs/API-db.md): the project's database browser, the same
// connections, saved queries and agents' access as every Database card of its board.

import { DbWorkspace } from '../../../db/Workspace';
import type { ToolBodyProps } from '../ToolTab';

export default function DbTool({ env }: ToolBodyProps) {
  return (
    <section className="tool-db" aria-label="Databases">
      <DbWorkspace projectId={env.project.id} storeKey={`code:${env.project.id}`} />
    </section>
  );
}
