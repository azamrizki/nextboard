// The Chats tab of the code view (docs/API-studio.md §7, DESIGN.md §3.5 Chats): the board's Chats
// panel in a tab. A chat resumes where it started; its console then opens as a tab of this editor
// (a board agent's console, not a copy). A lead chat shows in the Lead side bar (on a phone, which
// has none, the lead's pane opens as a tab); an orchestrator card's in the lead's pane, on that card.

import { showLead, showPane } from '../pageSide';
import { useState } from 'react';
import ChatsPanel from '../../../chats/ChatsPanel';
import { ResumeDialog } from '../../../chats/ResumeDialog';
import type { ChatView } from '../../../chats/types';
import { toast } from '../../../components/Toast';
import { pickOrchestratorThread } from '../AgentPanes';
import { ORCHESTRATOR_PANE, type Workspace } from '../workspace';
import { showConsole, type ToolEnv } from '../toolEnv';

const stay = () => undefined;

/** The lead's pane as a tab, on the thread picked: a pane open already follows the pick. */
function showLeadPane(ws: Workspace, workspaceId: string, cardId: string) {
  pickOrchestratorThread(workspaceId, cardId);
  // a phone has no side bar: showPane opens the pane as a tab there (pageSide.ts)
  if (!showPane(ws, ORCHESTRATOR_PANE)) ws.placePane(ORCHESTRATOR_PANE, 'tab');
}

export default function ChatsTool({ env }: { env: ToolEnv }) {
  const { ws, ctl, ui, project, page } = env;
  const [resume, setResume] = useState<ChatView | null>(null);

  const openThread = (threadId: string) => {
    if (threadId === project.id) {
      // the project's lead: the code view's Lead side bar (a phone: its Lead sheet, pageSide.ts),
      // else the board's command bar
      if (page) showLead(ws);
      else if (ui) ui.set({ leadThread: ui.get().leadThread + 1 });
      return;
    }
    // an orchestrator card's thread: the lead's pane, on that card
    if (!ctl.node(threadId)) {
      toast.info('That orchestrator card is not on this board. Open its board to see its chat.');
      return;
    }
    showLeadPane(ws, ctl.workspaceId, threadId);
  };

  return (
    <>
      {/* a chat continued elsewhere: its new console opens as a tab here, not on the board */}
      <ChatsPanel ctl={ctl} project={project} onClose={stay} onResume={setResume} onOpenThread={openThread} onStarted={(sid) => showConsole(ws, sid)} />
      {resume && (
        <ResumeDialog
          chat={resume}
          project={project}
          workspaceId={env.workspaceId}
          onClose={() => setResume(null)}
          onResumed={(s) => {
            if (s?.id) showConsole(ws, s.id);
          }}
          onShow={(sessionId) => {
            setResume(null);
            showConsole(ws, sessionId);
          }}
        />
      )}
    </>
  );
}
