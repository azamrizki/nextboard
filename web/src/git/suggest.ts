// The sparkle button of the Git panel and of the editor's Source Control
// (docs/API-studio.md §3.2): a Conventional Commits message written from the changes.

import { toast } from '../components/Toast';
import { orchStore } from '../orch/store';
import { gitApi } from '../studio/api';
import { waitJob } from '../studio/store';
import type { GitTarget } from '../studio/types';
import { gitError } from './common';

export const SUGGEST_LABEL = 'Write a commit message from the changes';
export const SUGGEST_LABEL_OFFLINE = 'Write a commit subject from the file names';

/** The hub's job ends after 2 minutes; this only outlives it. */
const JOB_WAIT_MS = 150_000;

async function fromCli(target: GitTarget, paths: string[], staged: boolean): Promise<string> {
  const j = await gitApi.suggestAI(target, paths, staged);
  orchStore.upsertJob(j);
  const done = await waitJob(j.id, JOB_WAIT_MS);
  const message = (done.result as { message?: string } | null)?.message;
  if (done.state !== 'done' || !message) throw new Error(done.error || 'the CLI gave no message');
  return message;
}

/** With the CLI when that is on; without it, or when it fails, from the file names. */
export async function suggestCommitMessage(target: GitTarget, paths: string[], opts: { staged?: boolean; useCli: boolean }): Promise<string> {
  if (opts.useCli) {
    try {
      return await fromCli(target, paths, !!opts.staged);
    } catch (err) {
      toast.info(`The CLI could not read the changes (${gitError(err)}). This subject comes from the file names.`);
    }
  }
  return (await gitApi.suggest(target, paths)).subject;
}
