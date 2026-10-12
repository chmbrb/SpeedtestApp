import { useEffect, useId, useRef, useState, type ReactNode } from 'react'

/* ---------- icons: 16px, 1.75 stroke, currentColor ---------- */
type IconProps = { size?: number; className?: string }
const svg = (children: ReactNode, { size = 16, className }: IconProps) => (
  <svg width={size} height={size} viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.75"
    strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" className={className}>{children}</svg>
)
export const CheckIcon = (p: IconProps) => svg(<path d="M3 8.5l3.2 3.2L13 4.8" />, p)
export const ChevronIcon = (p: IconProps) => svg(<path d="M4 6l4 4 4-4" />, p)
export const ArrowUpIcon = (p: IconProps) => svg(<path d="M8 13V3M3.5 7.5L8 3l4.5 4.5" />, p)
export const ArrowDownIcon = (p: IconProps) => svg(<path d="M8 3v10M3.5 8.5L8 13l4.5-4.5" />, p)
export const MinusIcon = (p: IconProps) => svg(<path d="M3.5 8h9" />, p)
export const AlertIcon = (p: IconProps) => svg(<><path d="M8 2.2l6.2 11H1.8z" /><path d="M8 6.8v3M8 12v.1" /></>, p)
export const OkIcon = (p: IconProps) => svg(<><circle cx="8" cy="8" r="6" /><path d="M5.4 8.2l1.8 1.8 3.4-3.6" /></>, p)
export const InfoIcon = (p: IconProps) => svg(<><circle cx="8" cy="8" r="6" /><path d="M8 7.4v3.4M8 5.2v.1" /></>, p)
export const MoonIcon = (p: IconProps) => svg(<path d="M13.2 9.6A5.6 5.6 0 016.4 2.8a5.6 5.6 0 106.8 6.8z" />, p)
export const SunIcon = (p: IconProps) => svg(<><circle cx="8" cy="8" r="2.6" /><path d="M8 1.6v1.4M8 13v1.4M1.6 8H3M13 8h1.4M3.5 3.5l1 1M11.5 11.5l1 1M3.5 12.5l1-1M11.5 4.5l1-1" /></>, p)
export const AutoIcon = (p: IconProps) => svg(<><circle cx="8" cy="8" r="5.6" /><path d="M8 2.4v11.2" /><path d="M8 2.4a5.6 5.6 0 010 11.2z" fill="currentColor" /></>, p)
export const OfflineIcon = (p: IconProps) => svg(<><path d="M2 6.2a9 9 0 0112 0M4.4 8.8a5.6 5.6 0 017.2 0" /><path d="M8 12.2v.1" /><path d="M2.5 2.5l11 11" /></>, p)

/* ---------- segmented control: a few mutually exclusive choices, all visible ---------- */
export function Segmented<T extends string>({ label, caption, value, options, onChange }: {
  label: string
  /** Visible label shown before the options (the accessible name is always `label`). */
  caption?: boolean
  value: T
  options: { value: T; label: string; icon?: ReactNode }[]
  onChange: (v: T) => void
}) {
  return (
    <div className="segmented-field">
      {caption && <span className="menu-label" aria-hidden="true">{label}</span>}
      <div role="radiogroup" aria-label={label} className="segmented">
        {options.map((o) => (
          <button key={o.value} type="button" role="radio" aria-checked={o.value === value}
            className="seg" onClick={() => onChange(o.value)}>
            {o.icon}
            <span>{o.label}</span>
          </button>
        ))}
      </div>
    </div>
  )
}

/* ---------- menu: a button that opens a list of options (single or multiple choice) ---------- */
export function Menu({ label, summary, multiple = false, options, selected, onToggle, footer, align = 'start' }: {
  label: string
  summary: string
  multiple?: boolean
  options: { value: string; label: string; hint?: string }[]
  selected: string[]
  onToggle: (value: string) => void
  footer?: ReactNode
  align?: 'start' | 'end'
}) {
  const [open, setOpen] = useState(false)
  const root = useRef<HTMLDivElement>(null)
  const id = useId()

  useEffect(() => {
    if (!open) return
    const onDown = (e: PointerEvent) => { if (!root.current?.contains(e.target as Node)) setOpen(false) }
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') { setOpen(false); root.current?.querySelector('button')?.focus() } }
    document.addEventListener('pointerdown', onDown)
    document.addEventListener('keydown', onKey)
    return () => { document.removeEventListener('pointerdown', onDown); document.removeEventListener('keydown', onKey) }
  }, [open])

  return (
    <div className="menu" ref={root}>
      <button type="button" className="menu-button" aria-haspopup="true" aria-expanded={open} aria-controls={id}
        onClick={() => setOpen((o) => !o)}>
        <span className="menu-label">{label}</span>
        <span className="menu-value">{summary}</span>
        <ChevronIcon className="menu-chevron" />
      </button>
      {open && (
        <div id={id} className={`menu-panel align-${align}`} role="group" aria-label={label}>
          {options.length === 0 && <p className="menu-empty">Nothing to choose yet.</p>}
          {options.map((o) => {
            const on = selected.includes(o.value)
            return (
              <button key={o.value} type="button" className="menu-option" role={multiple ? 'menuitemcheckbox' : 'menuitemradio'}
                aria-checked={on}
                onClick={() => { onToggle(o.value); if (!multiple) setOpen(false) }}>
                <span className="menu-check">{on && <CheckIcon />}</span>
                <span className="menu-option-label">{o.label}</span>
                {o.hint && <span className="menu-hint">{o.hint}</span>}
              </button>
            )
          })}
          {footer && <div className="menu-footer">{footer}</div>}
        </div>
      )}
    </div>
  )
}
