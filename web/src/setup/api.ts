// The setup wizard's endpoints (docs/API.md Instances): the other instances a copy can come from,
// and the copy itself, done by this (the new) hub from the other instance's files on disk.

import { apiRequest } from '../api';
import type { ImportBody, ImportCopied, WizardHub } from './wizard';

export interface ImportSource {
  name: string;
  hubName: string;
  /** It has an admin password, which a copy needs. */
  setUp: boolean;
}

export interface ImportResult {
  from: string;
  copied: ImportCopied;
  /** What was left out or needs a hand, one line each. */
  notes: string[];
}

export const setupApi = {
  /** GET /api/instances/import (admin): the other instances of this machine, by name. */
  importSources: () => apiRequest<{ self: string; sources: ImportSource[] | null }>('GET', '/api/instances/import').then((r) => r.sources ?? []),
  /** POST /api/instances/import (admin), with the other instance's admin password. */
  importFrom: (body: ImportBody) => apiRequest<ImportResult>('POST', '/api/instances/import', body),
  hub: () => apiRequest<WizardHub>('GET', '/api/hub'),
};
