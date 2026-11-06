// Credentials card (docs/API-vault.md): a password manager on the board, after Bitwarden. The
// vault belongs to the hub and not to the card, so every card shows the same one, the header's
// window shows it too, and deleting a card loses nothing. No secret ever goes in the node's data:
// the board is synced, snapshotted and readable by agents, and only a title is kept there.

import { memo, useState } from 'react';
import type { NodeProps } from '@xyflow/react';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import { useVaultPanel } from '../../vault/Panel';
import { lockVault, touchVault } from '../../vault/store';
import type { FlowNode } from '../flow';
import { readCredentials } from '../kinds';
import { useBoardEnv } from '../ui';
import { InlineTitle, stopKeys, TileFrame, TileHead } from './common';

function CredentialsTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl } = useBoardEnv();
  const d = readCredentials(data.node);
  const p = useVaultPanel();
  const [deleting, setDeleting] = useState(false);

  return (
    <TileFrame id={id} kind="credentials" selected={!!selected} label={`Credentials ${d.title}`} minW={360} minH={380}>
      <TileHead
        id={id}
        icon="key"
        title={<InlineTitle value={d.title} label="Card title" maxLength={40} onCommit={(title) => ctl.updateData(id, { title })} />}
        meta={<span className="tile-meta tabular">{p.meta}</span>}
        actions={
          p.open && (
            <>
              <Button size="sm" variant="ghost" icon="plus" className="nodrag" onClick={p.newItem}>
                New
              </Button>
              <Button size="sm" variant="ghost" iconOnly icon="lock" className="nodrag" aria-label="Lock the vault" title="Lock the vault" onClick={() => lockVault(true)} />
            </>
          )
        }
        menu={p.menu}
        menuLabel={`Actions for ${d.title}`}
        deleteItem={{ label: 'Delete card…', icon: 'trash', danger: true, onSelect: () => setDeleting(true) }}
      />
      {/* Capture: a dialog stops its events on the way up, and working in one is still working here. */}
      <div className="vault-body nodrag" onPointerDownCapture={touchVault} onKeyDownCapture={touchVault} onKeyDown={stopKeys}>
        {p.body}
        {deleting && (
          <ConfirmDialog title={`Delete ${d.title}?`} confirmLabel="Delete card" danger onClose={() => setDeleting(false)} onConfirm={() => ctl.deleteNodes([id])}>
            <p>Only the card goes. The vault stays on the hub, and the key button in the header opens it too.</p>
          </ConfirmDialog>
        )}
      </div>
    </TileFrame>
  );
}

export const CredentialsTile = memo(CredentialsTileImpl);
