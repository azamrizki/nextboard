// The vault in a window, opened from the header on any page: the same vault as every Credentials
// card, with nothing to add to a board first. Closing the window does not lock it; the vault's own
// timer and the lock button do.

import { Button } from '../components/Button';
import { Menu } from '../components/Menu';
import { Modal } from '../components/Modal';
import { useVaultPanel } from './Panel';
import { lockVault, touchVault } from './store';

export function VaultWindow({ onClose }: { onClose: () => void }) {
  const p = useVaultPanel();
  return (
    <Modal
      title="Credentials"
      icon="key"
      size="lg"
      className="vault-window"
      onClose={onClose}
      actions={
        <>
          {p.meta && <span className="vault-window-meta tabular">{p.meta}</span>}
          {p.open && (
            <>
              <Button size="sm" variant="ghost" icon="plus" onClick={p.newItem}>
                New
              </Button>
              <Button size="sm" variant="ghost" iconOnly icon="lock" aria-label="Lock the vault" title="Lock the vault" onClick={() => lockVault(true)} />
              <Menu label="Vault actions" items={p.menu} />
            </>
          )}
        </>
      }
    >
      <div className="vault-body" onPointerDownCapture={touchVault} onKeyDownCapture={touchVault}>
        {p.body}
      </div>
    </Modal>
  );
}
