// Status dot + label (DESIGN.md §3.7). Colour is never the only signal: the label
// is rendered as text (or at least as aria-label when `hideLabel`).

export type DotColor = 'busy' | 'success' | 'warning' | 'grey' | 'danger' | 'info' | 'needs';

interface StatusDotProps {
  color: DotColor;
  label: string;
  /** Only show the dot visually; the label stays available to assistive tech. */
  hideLabel?: boolean;
  className?: string;
}

export function StatusDot({ color, label, hideLabel, className }: StatusDotProps) {
  if (hideLabel) {
    return <span className={`dot dot-${color} ${className ?? ''}`} role="img" aria-label={label} title={label} />;
  }
  return (
    <span className={`status ${className ?? ''}`}>
      <span className={`dot dot-${color}`} aria-hidden="true" />
      <span>{label}</span>
    </span>
  );
}

const BADGE_FOR: Record<DotColor, string> = {
  busy: 'badge-success',
  success: 'badge-success',
  warning: 'badge-warning',
  grey: '',
  danger: 'badge-danger',
  info: 'badge-info',
  needs: 'badge-needs',
};

/** Pill badge with a dot and a label, tinted by status colour. */
export function StatusBadge({ color, label, title }: { color: DotColor; label: string; title?: string }) {
  return (
    <span className={`badge ${BADGE_FOR[color]}`} title={title}>
      <span className={`dot dot-${color}`} aria-hidden="true" />
      {label}
    </span>
  );
}
