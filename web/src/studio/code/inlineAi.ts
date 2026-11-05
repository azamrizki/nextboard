// Ghost text in the editor: Monaco's inline suggestions filled by the hidden runner
// (docs/API-studio.md §7.4). A CLI run takes seconds rather than milliseconds, so this waits
// for a pause in typing, and any further typing cancels the run. Off by default, since each
// suggestion spends the user's plan.

import { errorMessage } from '../../api';
import { filesApi, jobApi } from '../api';
import { waitJob } from '../store';
import { monaco } from './monaco';
import type { Workspace } from './workspace';

const TYPING_PAUSE_MS = 900;
const LINES_BEFORE = 120;
const LINES_AFTER = 40;

let current: Workspace | null = null;
let registered = false;

/** Text of a finished job, without the code fence models sometimes add. */
function completionOf(result: unknown): string {
  const text = (result as { text?: unknown } | null)?.text;
  if (typeof text !== 'string') return '';
  const m = /^```[\w-]*\n([\s\S]*?)\n?```\s*$/.exec(text.trim());
  return (m ? m[1] : text).replace(/\s+$/, '');
}

export function registerInlineAi(ws: Workspace) {
  current = ws;
  if (registered) return;
  registered = true;
  monaco.languages.registerInlineCompletionsProvider('*', {
    async provideInlineCompletions(model, position, ctx, token) {
      const w = current;
      if (!w || model.uri.scheme !== 'file' || !w.inside(model.uri.path)) return { items: [] };
      const mode = w.editorPrefs.aiInline;
      const explicit = ctx.triggerKind === monaco.languages.InlineCompletionTriggerKind.Explicit;
      if (mode === 'off' || (mode === 'manual' && !explicit)) return { items: [] };
      if (!explicit) {
        await new Promise((r) => window.setTimeout(r, TYPING_PAUSE_MS));
        if (token.isCancellationRequested) return { items: [] };
      }
      const before = model.getValueInRange(new monaco.Range(Math.max(1, position.lineNumber - LINES_BEFORE), 1, position.lineNumber, position.column));
      const last = Math.min(model.getLineCount(), position.lineNumber + LINES_AFTER);
      const after = model.getValueInRange(new monaco.Range(position.lineNumber, position.column, last, model.getLineMaxColumn(last)));
      if (!before.trim() && !after.trim()) return { items: [] };
      w.setAiBusy(1);
      let jobId = '';
      const cancel = token.onCancellationRequested(() => {
        if (jobId) void jobApi.cancel(jobId).catch(() => undefined);
      });
      try {
        const j = await filesApi.aiComplete(w.pid, { path: w.rel(model.uri.path), language: model.getLanguageId(), before, after });
        jobId = j.id;
        if (token.isCancellationRequested) {
          void jobApi.cancel(j.id).catch(() => undefined);
          return { items: [] };
        }
        const done = await waitJob(j.id, 40_000);
        if (token.isCancellationRequested || done.state !== 'done') {
          if (done.state === 'failed') w.log(`AI suggestion: ${done.error ?? 'failed'}`);
          return { items: [] };
        }
        const insertText = completionOf(done.result);
        if (!insertText) return { items: [] };
        return { items: [{ insertText, range: new monaco.Range(position.lineNumber, position.column, position.lineNumber, position.column) }] };
      } catch (err) {
        w.log(`AI suggestion: ${errorMessage(err)}`);
        return { items: [] };
      } finally {
        cancel.dispose();
        w.setAiBusy(-1);
      }
    },
    disposeInlineCompletions() {},
  });
}
