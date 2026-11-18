// The Vault tab of the code view (docs/API-studio.md §7, docs/API-vault.md): the hub's one vault,
// the same as every Credentials card and the header's window, with its own head. Closing the tab
// does not lock it; the lock button and the vault's own timer do.

import { Button } from '../../../components/Button';
import { Icon } from '../../../components/Icon';
import { Menu } from '../../../components/Menu';
import { useVaultPanel } from '../../../vault/Panel';
import { lockVault, touchVault } from '../../../vault/store';

export default function VaultTool() {
  const p = useVaultPanel();
  const status = p.v.status;
  return (
    <section className="tool-vault" aria-label="Vault">
      <header className="tool-head">
        <Icon name={p.open ? 'unlock' : 'lock'} />
        <h2>Vault</h2>
        {p.open ? (
          <span className="badge badge-success">Unlocked</span>
        ) : status === 'locked' ? (
          <span className="badge">Locked</span>
        ) : null}
        {p.open && p.meta && <span className="tool-head-meta tabular">{p.meta}</span>}
        <span className="spacer" />
        {p.open && (
          <>
            <Button size="sm" icon="plus" onClick={p.newItem}>
              New item
            </Button>
            <Button size="sm" variant="ghost" iconOnly icon="lock" aria-label="Lock the vault" title="Lock the vault" onClick={() => lockVault(true)} />
            <Menu label="Vault actions" items={p.menu} />
          </>
        )}
      </header>
      {/* Capture: a dialog stops its events on the way up, and working in one is still working here. */}
      <div className="vault-body" onPointerDownCapture={touchVault} onKeyDownCapture={touchVault}>
        {p.body}
      </div>
    </section>
  );
}
