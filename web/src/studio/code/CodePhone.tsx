// The Code view on a phone (DESIGN.md §3.19, plan decision 2b): the same workbench made for one
// hand. Bottom navigation Files · Editor · Terminal · Lead · Agents: Files is the side view full
// screen (with the tools in its ⋯ Tools menu), Editor the active editor group, Terminal the panel
// at full height; Lead and Agents open as bottom sheets over whichever of the three is shown.

import { Icon, type IconName } from '../../components/Icon';
import { Modal } from '../../components/Modal';
import { useLeadName } from '../../orch/lead';
import { AgentsList, LeadPane } from './AgentPanes';
import type { Workspace } from './workspace';

export type PhoneTab = 'files' | 'editor' | 'terminal';
export type PhoneSheet = 'lead' | 'agents';

const TABS: { id: PhoneTab | PhoneSheet; label: string; icon: IconName }[] = [
  { id: 'files', label: 'Files', icon: 'file' },
  { id: 'editor', label: 'Editor', icon: 'code' },
  { id: 'terminal', label: 'Terminal', icon: 'terminal' },
  { id: 'lead', label: 'Lead', icon: 'orchestrator' },
  { id: 'agents', label: 'Agents', icon: 'bot' },
];

const isSheet = (id: string): id is PhoneSheet => id === 'lead' || id === 'agents';

/** Along the bottom edge, in reach of a thumb; the open sheet is the current item while it shows. */
export function PhoneNav({ tab, sheet, onTab, onSheet }: { tab: PhoneTab; sheet: PhoneSheet | null; onTab: (t: PhoneTab) => void; onSheet: (s: PhoneSheet) => void }) {
  const leadName = useLeadName();
  return (
    <nav className="tab-bar cv-phone-nav" aria-label="Code view">
      {TABS.map((t) => {
        const current = sheet ? t.id === sheet : t.id === tab;
        return (
          <button
            key={t.id}
            type="button"
            className="tab-bar-item"
            aria-current={current ? 'page' : undefined}
            title={t.id === 'lead' ? `${leadName}, the lead` : undefined}
            // a tap must not take the keyboard from the editor first: the key bar would go and the
            // page would move under the finger before the tap ends, so the tap would miss
            onPointerDown={(e) => e.preventDefault()}
            onClick={() => {
              // leaving the editor: its keyboard and key bar go with it
              if (t.id !== 'editor' && document.activeElement instanceof HTMLElement && document.activeElement.closest('.monaco-editor')) document.activeElement.blur();
              if (isSheet(t.id)) onSheet(t.id);
              else onTab(t.id);
            }}
          >
            <span className="tab-bar-icon">
              <Icon name={t.icon} />
            </span>
            <span className="tab-bar-label">{t.label}</span>
          </button>
        );
      })}
    </nav>
  );
}

/** Lead or Agents as a sheet from the bottom edge: the side bar's own panes (AgentPanes.tsx). */
export function PhoneSheetView({ kind, ws, onClose }: { kind: PhoneSheet; ws: Workspace; onClose: () => void }) {
  const leadName = useLeadName();
  return (
    <Modal title={kind === 'lead' ? leadName : 'Agents'} icon={kind === 'lead' ? 'orchestrator' : 'bot'} size="lg" className="cv-sheet" onClose={onClose}>
      {/* a console opened from the list shows in the Editor, so the sheet goes */}
      <div className={`cv-sheet-body is-${kind}`}>{kind === 'lead' ? <LeadPane ws={ws} /> : <AgentsList ws={ws} onOpen={onClose} />}</div>
    </Modal>
  );
}
