// Which machines can be archived or deleted (docs/API.md Machines): not This PC, and not one a
// project part (active or archived) or a console uses. The hub refuses those with 409 in_use;
// the list says so before anyone tries.

import { projectParts } from '../lib/parts';
import type { Machine, MachineUser, Project } from '../lib/types';
import { nameList } from '../home/remove';

/** The projects with a part on the machine, by name. */
export function machineUsers(machineId: string, projects: Project[]): MachineUser[] {
  return projects
    .filter((p) => projectParts(p).some((pt) => pt.machineId === machineId))
    .map((p) => ({ id: p.id, name: p.name }))
    .sort((a, b) => a.name.localeCompare(b.name));
}

/** Why the machine cannot be archived or deleted, or '' when it can. */
export function machineBlock(m: Machine, users: MachineUser[]): string {
  if (m.id === 'local' || m.kind === 'local') return 'This PC always stays';
  if (users.length) return `Used by ${nameList(users.map((u) => u.name))}`;
  if (m.sessionCount > 0) return `Has ${m.sessionCount === 1 ? 'a console' : `${m.sessionCount} consoles`} open`;
  return '';
}
