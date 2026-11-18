// Where the right side bar's things go (DESIGN.md §3.19). The Code view on a phone draws no side
// bar: its Lead and Agents are bottom sheets (CodePhone.tsx), and a console sent to the side bar
// opens as an editor tab instead. Code that would show the lead, the agents or a console pane in
// the side bar calls these, and gets whatever the screen has; everywhere else (a card, the Code
// view on a wider screen) they do what the side bar always did.

import type { Workspace } from './workspace';

export interface PhoneSheets {
  lead: () => void;
  agents: () => void;
}

const phones = new WeakMap<Workspace, PhoneSheets>();

/** The phone layout of a Code view, while it shows, says how to open its sheets (CodeStudio.tsx). */
export function registerPhoneSheets(ws: Workspace, sheets: PhoneSheets): () => void {
  phones.set(ws, sheets);
  return () => {
    if (phones.get(ws) === sheets) phones.delete(ws);
  };
}

/** No side bar on screen for this editor: the Code view on a phone. */
export const noSideBar = (ws: Workspace): boolean => phones.has(ws);

/** Show the project's lead: the side bar's Lead tab, or on a phone the Lead sheet. */
export function showLead(ws: Workspace) {
  const phone = phones.get(ws);
  if (phone) return phone.lead();
  ws.rightTab = 'lead';
  ws.rightOpen = true;
  ws.changed();
}

/** Show the agents list: the side bar's Agents tab, or on a phone the Agents sheet. */
export function showAgents(ws: Workspace) {
  const phone = phones.get(ws);
  if (phone) return phone.agents();
  ws.rightTab = 'agents';
  ws.rightOpen = true;
  ws.changed();
}

/**
 * Bring a console pane to the front where it is (ws.revealPane). On a phone a pane of the side bar
 * becomes a tab, since the side bar is not drawn. False when the pane is in neither.
 */
export function showPane(ws: Workspace, id: string): boolean {
  if (!phones.has(ws)) return ws.revealPane(id);
  if (!ws.paneTabAt(id) && !ws.rightPanes.includes(id)) return false;
  ws.openPane(id);
  return true;
}

/** Put a console pane in the side bar (ws.placePane 'side'); on a phone, a tab. */
export function placePaneSide(ws: Workspace, id: string) {
  if (phones.has(ws)) ws.openPane(id);
  else ws.placePane(id, 'side');
}
