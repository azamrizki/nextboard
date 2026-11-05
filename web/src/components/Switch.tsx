// Switch (DESIGN.md §3.2: 36×20) in a settings row: title + hint on the left, the
// switch on the right. role="switch" with the title as its label.

import { useId, type ReactNode } from 'react';

interface SwitchRowProps {
  checked: boolean;
  onChange: (v: boolean) => void;
  title: ReactNode;
  hint?: ReactNode;
  disabled?: boolean;
  busy?: boolean;
}

export function SwitchRow({ checked, onChange, title, hint, disabled, busy }: SwitchRowProps) {
  const id = useId();
  return (
    <div className="setting-row">
      <div className="setting-text">
        <span className="setting-title" id={`${id}-t`}>
          {title}
        </span>
        {hint && (
          <span className="setting-hint" id={`${id}-h`}>
            {hint}
          </span>
        )}
      </div>
      <button
        type="button"
        role="switch"
        className="switch"
        aria-checked={checked}
        aria-labelledby={`${id}-t`}
        aria-describedby={hint ? `${id}-h` : undefined}
        aria-busy={busy || undefined}
        disabled={disabled}
        onClick={() => !busy && onChange(!checked)}
      >
        <span className="switch-knob" aria-hidden="true" />
      </button>
    </div>
  );
}

/** Settings row with any control on the right. */
export function SettingRow({ title, hint, children, htmlFor }: { title: ReactNode; hint?: ReactNode; children: ReactNode; htmlFor?: string }) {
  return (
    <div className="setting-row">
      <div className="setting-text">
        {htmlFor ? (
          <label className="setting-title" htmlFor={htmlFor}>
            {title}
          </label>
        ) : (
          <span className="setting-title">{title}</span>
        )}
        {hint && <span className="setting-hint">{hint}</span>}
      </div>
      <div className="setting-control">{children}</div>
    </div>
  );
}
