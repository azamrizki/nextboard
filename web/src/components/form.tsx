// Form primitives per DESIGN.md §3.2: field anatomy (label → control → hint/error),
// segmented control, radio cards, file picker, checkbox, section.

import {
  useId,
  useRef,
  type ChangeEvent,
  type InputHTMLAttributes,
  type KeyboardEvent,
  type ReactNode,
  type SelectHTMLAttributes,
} from 'react';
import { Icon, type IconName } from './Icon';

// ---- Field --------------------------------------------------------------------

/** Props a Field hands to its control so label/hint/error are wired up. */
export interface ControlA11y {
  id: string;
  'aria-describedby'?: string;
  'aria-invalid'?: true;
  /** Id of the label element (for radiogroups, which can't use <label for>). */
  labelId: string;
}

interface FieldProps {
  label: ReactNode;
  optional?: boolean;
  hint?: ReactNode;
  error?: string;
  /** "group" renders the label as text (for segmented controls / radio cards). */
  as?: 'control' | 'group';
  className?: string;
  children: (a11y: ControlA11y) => ReactNode;
}

export function Field({ label, optional, hint, error, as = 'control', className, children }: FieldProps) {
  const id = useId();
  const labelId = `${id}-label`;
  const hintId = `${id}-hint`;
  const errId = `${id}-err`;
  const describedBy = error ? errId : hint ? hintId : undefined;
  const labelInner = (
    <>
      {label}
      {optional && <span className="optional"> (optional)</span>}
    </>
  );
  return (
    <div
      className={`field ${className ?? ''}`}
      role={as === 'group' ? 'group' : undefined}
      aria-labelledby={as === 'group' ? labelId : undefined}
      aria-describedby={as === 'group' ? describedBy : undefined}
    >
      {as === 'group' ? (
        <span className="field-label" id={labelId}>
          {labelInner}
        </span>
      ) : (
        <label className="field-label" id={labelId} htmlFor={id}>
          {labelInner}
        </label>
      )}
      {children({ id, labelId, 'aria-describedby': describedBy, 'aria-invalid': error ? true : undefined })}
      {error ? (
        <span className="field-error" id={errId}>
          <Icon name="alertCircle" />
          {error}
        </span>
      ) : hint ? (
        <span className="field-hint" id={hintId}>
          {hint}
        </span>
      ) : null}
    </div>
  );
}

type InputProps = Omit<InputHTMLAttributes<HTMLInputElement>, 'id'>;

interface TextFieldProps extends InputProps {
  label: ReactNode;
  optional?: boolean;
  hint?: ReactNode;
  error?: string;
  mono?: boolean;
  fieldClassName?: string;
}

export function TextField({ label, optional, hint, error, mono, fieldClassName, className, ...input }: TextFieldProps) {
  return (
    <Field label={label} optional={optional} hint={hint} error={error} className={fieldClassName}>
      {({ labelId: _l, ...a11y }) => (
        <input className={`control ${mono ? 'mono' : ''} ${className ?? ''}`} {...a11y} {...input} />
      )}
    </Field>
  );
}

interface SelectFieldProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, 'id'> {
  label: ReactNode;
  optional?: boolean;
  hint?: ReactNode;
  error?: string;
  fieldClassName?: string;
}

export function SelectField({ label, optional, hint, error, fieldClassName, className, children, ...select }: SelectFieldProps) {
  return (
    <Field label={label} optional={optional} hint={hint} error={error} className={fieldClassName}>
      {({ labelId: _l, ...a11y }) => (
        <select className={`control ${className ?? ''}`} {...a11y} {...select}>
          {children}
        </select>
      )}
    </Field>
  );
}

export function FormSection({ title, children }: { title: string; children: ReactNode }) {
  const id = useId();
  return (
    <section className="form-section" aria-labelledby={id}>
      <h3 className="section-title" id={id}>
        {title}
      </h3>
      {children}
    </section>
  );
}

// ---- radiogroup keyboard helper -------------------------------------------------

interface Choice<T extends string> {
  value: T;
  disabled?: boolean;
}

/** Arrow keys move selection within a radiogroup (roving tabindex). */
function useRadioKeys<T extends string>(options: Choice<T>[], value: T, onChange: (v: T) => void) {
  const refs = useRef<(HTMLButtonElement | null)[]>([]);
  const enabled = options.map((o, i) => ({ o, i })).filter(({ o }) => !o.disabled);

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    const keys = ['ArrowRight', 'ArrowDown', 'ArrowLeft', 'ArrowUp', 'Home', 'End'];
    if (!keys.includes(e.key) || enabled.length === 0) return;
    e.preventDefault();
    const cur = enabled.findIndex(({ o }) => o.value === value);
    let next = cur;
    if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = enabled.length - 1;
    else if (e.key === 'ArrowRight' || e.key === 'ArrowDown') next = (cur + 1) % enabled.length;
    else next = (cur - 1 + enabled.length) % enabled.length;
    const target = enabled[next];
    onChange(target.o.value);
    refs.current[target.i]?.focus();
  };

  const hasChecked = options.some((o) => o.value === value && !o.disabled);
  const tabIndexFor = (i: number) => {
    const o = options[i];
    if (o.value === value) return 0;
    if (!hasChecked && enabled[0]?.i === i) return 0;
    return -1;
  };
  return { refs, onKeyDown, tabIndexFor };
}

// ---- Segmented control ------------------------------------------------------------

export interface SegmentOption<T extends string> extends Choice<T> {
  label: ReactNode;
  icon?: IconName;
  title?: string;
}

interface SegmentedProps<T extends string> {
  value: T;
  onChange: (v: T) => void;
  options: SegmentOption<T>[];
  /** aria-label, or pass labelledBy from a Field. */
  label?: string;
  labelledBy?: string;
  size?: 'md' | 'sm';
  block?: boolean;
  disabled?: boolean;
  /** Use tab semantics instead of radio (e.g. switching form sources). */
  className?: string;
}

export function SegmentedControl<T extends string>({
  value,
  onChange,
  options,
  label,
  labelledBy,
  size = 'md',
  block,
  disabled,
  className,
}: SegmentedProps<T>) {
  const opts = options.map((o) => ({ ...o, disabled: o.disabled || disabled }));
  const { refs, onKeyDown, tabIndexFor } = useRadioKeys(opts, value, onChange);
  return (
    <div
      role="radiogroup"
      aria-label={label}
      aria-labelledby={labelledBy}
      className={`segmented ${size === 'sm' ? 'sm' : ''} ${block ? 'block' : ''} ${className ?? ''}`}
    >
      {opts.map((o, i) => (
        <button
          key={o.value}
          ref={(el) => {
            refs.current[i] = el;
          }}
          type="button"
          role="radio"
          aria-checked={o.value === value}
          tabIndex={tabIndexFor(i)}
          disabled={o.disabled}
          title={o.title}
          className="segmented-item"
          onClick={() => onChange(o.value)}
          onKeyDown={onKeyDown}
        >
          {o.icon && <Icon name={o.icon} />}
          {o.label}
        </button>
      ))}
    </div>
  );
}

// ---- Radio cards -----------------------------------------------------------------

export interface RadioCardOption<T extends string> extends Choice<T> {
  title: ReactNode;
  description?: ReactNode;
}

interface RadioCardsProps<T extends string> {
  value: T;
  onChange: (v: T) => void;
  options: RadioCardOption<T>[];
  label?: string;
  labelledBy?: string;
  columns?: 1 | 2;
  disabled?: boolean;
  className?: string;
}

export function RadioCards<T extends string>({
  value,
  onChange,
  options,
  label,
  labelledBy,
  columns = 2,
  disabled,
  className,
}: RadioCardsProps<T>) {
  const opts = options.map((o) => ({ ...o, disabled: o.disabled || disabled }));
  const { refs, onKeyDown, tabIndexFor } = useRadioKeys(opts, value, onChange);
  return (
    <div
      role="radiogroup"
      aria-label={label}
      aria-labelledby={labelledBy}
      className={`radio-cards ${columns === 1 ? 'cols-1' : ''} ${className ?? ''}`}
    >
      {opts.map((o, i) => (
        <button
          key={o.value}
          ref={(el) => {
            refs.current[i] = el;
          }}
          type="button"
          role="radio"
          aria-checked={o.value === value}
          tabIndex={tabIndexFor(i)}
          disabled={o.disabled}
          className="radio-card"
          onClick={() => onChange(o.value)}
          onKeyDown={onKeyDown}
        >
          <span className="radio-dot" aria-hidden="true" />
          <span className="radio-card-body">
            <span className="radio-card-title">{o.title}</span>
            {o.description && <span className="radio-card-desc">{o.description}</span>}
          </span>
        </button>
      ))}
    </div>
  );
}

// ---- File picker -------------------------------------------------------------------

interface FilePickProps {
  buttonLabel?: string;
  fileName?: string;
  accept?: string;
  disabled?: boolean;
  onFile: (file: File) => void;
  /** Accessible description, e.g. "Load private key from a file". */
  ariaLabel?: string;
}

export function FilePick({ buttonLabel = 'Load from file', fileName, accept, disabled, onFile, ariaLabel }: FilePickProps) {
  const id = useId();
  const onChange = (e: ChangeEvent<HTMLInputElement>) => {
    const f = e.target.files?.[0];
    if (f) onFile(f);
    e.target.value = '';
  };
  return (
    <div className="file-pick">
      <input id={id} type="file" accept={accept} disabled={disabled} onChange={onChange} aria-label={ariaLabel ?? buttonLabel} />
      <label htmlFor={id} className="btn btn-secondary btn-sm" aria-hidden="true">
        <Icon name="upload" />
        {buttonLabel}
      </label>
      <span className="file-name truncate">{fileName || 'No file chosen'}</span>
    </div>
  );
}

// ---- Checkbox ------------------------------------------------------------------------

interface CheckboxProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  label: ReactNode;
  disabled?: boolean;
}

export function Checkbox({ checked, onChange, label, disabled }: CheckboxProps) {
  return (
    <label className="check">
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      {label}
    </label>
  );
}
