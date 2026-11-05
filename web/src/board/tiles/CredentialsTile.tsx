// Credentials card (docs/API-vault.md): a password manager on the board, after Bitwarden. The
// vault belongs to the hub and not to the card, so every card shows the same one and deleting a
// card loses nothing. No secret ever goes in the node's data: the board is synced, snapshotted
// and readable by agents, and only a title is kept there.

import { memo, useEffect, useState } from 'react';
import type { NodeProps } from '@xyflow/react';
import { Button } from '../../components/Button';
import { ConfirmDialog } from '../../components/ConfirmDialog';
import type { MenuEntry } from '../../components/Menu';
import { VaultBrowser } from '../../vault/Browser';
import { ChangePasswordDialog, ExportDialog, FolderDialog, GeneratorDialog, ImportDialog, PinDialog, ResetDialog, SettingsDialog } from '../../vault/Dialogs';
import { VaultGate } from '../../vault/Gate';
import { ItemForm } from '../../vault/ItemForm';
import type { VaultItem } from '../../vault/model';
import { lockVault, touchVault, useVault } from '../../vault/store';
import type { FlowNode } from '../flow';
import { readCredentials } from '../kinds';
import { useBoardEnv } from '../ui';
import { InlineTitle, stopKeys, TileFrame, TileHead } from './common';

type DialogName = 'generator' | 'folder' | 'settings' | 'password' | 'pin' | 'import' | 'export' | 'reset' | 'deleteCard';

function CredentialsTileImpl({ id, data, selected }: NodeProps<FlowNode>) {
  const { ctl } = useBoardEnv();
  const d = readCredentials(data.node);
  const v = useVault();
  const [dialog, setDialog] = useState<DialogName | null>(null);
  const [form, setForm] = useState<'new' | VaultItem | null>(null);
  const [folderId, setFolderId] = useState('');
  const [showId, setShowId] = useState('');

  const open = v.status === 'unlocked';

  // Locked: what was on screen goes, plain text included, and does not come back at the next unlock.
  useEffect(() => {
    if (open) return;
    setForm(null);
    setShowId('');
    setFolderId('');
    setDialog((name) => (name === 'reset' || name === 'deleteCard' ? name : null));
  }, [open]);

  const count = open ? v.items.filter((i) => !i.deletedAt).length : 0;
  const menu: MenuEntry[] = open
    ? [
        { label: 'Password generator…', icon: 'sparkle', onSelect: () => setDialog('generator') },
        { label: 'New folder…', icon: 'folder', onSelect: () => setDialog('folder') },
        { label: 'Import…', icon: 'upload', onSelect: () => setDialog('import') },
        { label: 'Export…', icon: 'download', onSelect: () => setDialog('export') },
        'sep',
        { label: 'Vault settings…', icon: 'sliders', onSelect: () => setDialog('settings') },
        { label: 'PIN unlock…', icon: 'keyboard', onSelect: () => setDialog('pin') },
        { label: 'Change master password…', icon: 'key', onSelect: () => setDialog('password') },
        { label: 'Reset the vault…', icon: 'alert', danger: true, onSelect: () => setDialog('reset') },
      ]
    : [];
  const close = () => setDialog(null);

  return (
    <TileFrame id={id} kind="credentials" selected={!!selected} label={`Credentials ${d.title}`} minW={360} minH={380}>
      <TileHead
        id={id}
        icon="key"
        title={<InlineTitle value={d.title} label="Card title" maxLength={40} onCommit={(title) => ctl.updateData(id, { title })} />}
        meta={<span className="tile-meta tabular">{open ? (count === 1 ? '1 item' : `${count} items`) : v.status === 'locked' ? 'Locked' : ''}</span>}
        actions={
          open && (
            <>
              <Button size="sm" variant="ghost" icon="plus" className="nodrag" onClick={() => setForm('new')}>
                New
              </Button>
              <Button size="sm" variant="ghost" iconOnly icon="lock" className="nodrag" aria-label="Lock the vault" title="Lock the vault" onClick={() => lockVault(true)} />
            </>
          )
        }
        menu={menu}
        menuLabel={`Actions for ${d.title}`}
        deleteItem={{ label: 'Delete card…', icon: 'trash', danger: true, onSelect: () => setDialog('deleteCard') }}
      />
      {/* Capture: a dialog stops its events on the way up, and working in one is still working here. */}
      <div className="vault-body nodrag" onPointerDownCapture={touchVault} onKeyDownCapture={touchVault} onKeyDown={stopKeys}>
        {open ? (
          <VaultBrowser v={v} onNew={() => setForm('new')} onEdit={setForm} onImport={() => setDialog('import')} onFolderChange={setFolderId} showId={showId} />
        ) : (
          <VaultGate v={v} onReset={() => setDialog('reset')} />
        )}
        {open && form && (
          <ItemForm
            item={form === 'new' ? undefined : form}
            folders={v.folders}
            startFolder={folderId}
            onSaved={(item) => setShowId(item.id)}
            onClose={() => setForm(null)}
          />
        )}
        {open && dialog === 'generator' && <GeneratorDialog onClose={close} />}
        {open && dialog === 'folder' && <FolderDialog onClose={close} />}
        {open && dialog === 'settings' && <SettingsDialog onClose={close} />}
        {open && dialog === 'password' && <ChangePasswordDialog onClose={close} />}
        {open && dialog === 'pin' && <PinDialog onClose={close} />}
        {open && dialog === 'import' && <ImportDialog onClose={close} />}
        {open && dialog === 'export' && <ExportDialog items={v.items} folders={v.folders} onClose={close} />}
        {dialog === 'reset' && <ResetDialog count={open ? v.items.length : null} onClose={close} />}
        {dialog === 'deleteCard' && (
          <ConfirmDialog title={`Delete ${d.title}?`} confirmLabel="Delete card" danger onClose={close} onConfirm={() => ctl.deleteNodes([id])}>
            <p>Only the card goes. The vault stays on the hub, and any Credentials card shows it again.</p>
          </ConfirmDialog>
        )}
      </div>
    </TileFrame>
  );
}

export const CredentialsTile = memo(CredentialsTileImpl);
