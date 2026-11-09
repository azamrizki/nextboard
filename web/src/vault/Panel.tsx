// The working parts of the open vault, shared by the board card and the window the header opens:
// the list or the lock screen, the item form, the dialogs, and the menu that opens them. Each
// place puts its own head around them.

import { useEffect, useState, type ReactNode } from 'react';
import type { MenuEntry } from '../components/Menu';
import { VaultBrowser } from './Browser';
import { ChangePasswordDialog, ExportDialog, FolderDialog, GeneratorDialog, ImportDialog, PasskeyDialog, PinDialog, ResetDialog, SettingsDialog } from './Dialogs';
import { VaultGate } from './Gate';
import { ItemForm } from './ItemForm';
import { SitePasskeysDialog } from './PasskeyRequests';
import { LeaksDialog } from './Leaks';
import type { VaultItem } from './model';
import { useVault, type VaultState } from './store';

type DialogName = 'generator' | 'folder' | 'settings' | 'password' | 'pin' | 'passkey' | 'sitePasskeys' | 'import' | 'export' | 'reset' | 'leaks';

export interface VaultPanel {
  v: VaultState;
  open: boolean;
  /** "3 items", "Locked", or nothing while it loads. */
  meta: string;
  menu: MenuEntry[];
  newItem: () => void;
  body: ReactNode;
}

export function useVaultPanel(): VaultPanel {
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
    setDialog((name) => (name === 'reset' ? name : null));
  }, [open]);

  const count = open ? v.items.filter((i) => !i.deletedAt).length : 0;
  const meta = open ? (count === 1 ? '1 item' : `${count} items`) : v.status === 'locked' ? 'Locked' : '';
  const menu: MenuEntry[] = open
    ? [
        { label: 'Password generator…', icon: 'sparkle', onSelect: () => setDialog('generator') },
        { label: 'Check passwords for leaks…', icon: 'shieldAlert', onSelect: () => setDialog('leaks') },
        { label: 'New folder…', icon: 'folder', onSelect: () => setDialog('folder') },
        { label: 'Import…', icon: 'upload', onSelect: () => setDialog('import') },
        { label: 'Export…', icon: 'download', onSelect: () => setDialog('export') },
        'sep',
        { label: 'Vault settings…', icon: 'sliders', onSelect: () => setDialog('settings') },
        { label: 'PIN unlock…', icon: 'keyboard', onSelect: () => setDialog('pin') },
        { label: 'Passkey unlock…', icon: 'key', onSelect: () => setDialog('passkey') },
        { label: 'Passkeys for sites…', icon: 'key', onSelect: () => setDialog('sitePasskeys') },
        { label: 'Change master password…', icon: 'key', onSelect: () => setDialog('password') },
        { label: 'Reset the vault…', icon: 'alert', danger: true, onSelect: () => setDialog('reset') },
      ]
    : [];
  const close = () => setDialog(null);

  const body = (
    <>
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
      {open && dialog === 'leaks' && <LeaksDialog items={v.items} onShow={setShowId} onClose={close} />}
      {open && dialog === 'folder' && <FolderDialog onClose={close} />}
      {open && dialog === 'settings' && <SettingsDialog onClose={close} />}
      {open && dialog === 'password' && <ChangePasswordDialog onClose={close} />}
      {open && dialog === 'pin' && <PinDialog onClose={close} />}
      {open && dialog === 'passkey' && <PasskeyDialog onClose={close} />}
      {open && dialog === 'sitePasskeys' && <SitePasskeysDialog onClose={close} />}
      {open && dialog === 'import' && <ImportDialog onClose={close} />}
      {open && dialog === 'export' && <ExportDialog items={v.items} folders={v.folders} onClose={close} />}
      {dialog === 'reset' && <ResetDialog count={open ? v.items.length : null} onClose={close} />}
    </>
  );

  return { v, open, meta, menu, newItem: () => setForm('new'), body };
}
