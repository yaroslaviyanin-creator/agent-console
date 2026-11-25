import { useEffect, useId, useRef, useState } from 'react'
import { ChevronDown } from 'lucide-react'

export interface ProviderOption {
  id: string
  label: string
  available: boolean
}

export function ProviderSelect({
  value,
  options,
  disabled,
  onChange,
}: {
  value: string
  options: readonly ProviderOption[]
  disabled: boolean
  onChange: (id: string) => void
}) {
  const [open, setOpen] = useState(false)
  const wrapRef = useRef<HTMLDivElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const listboxId = useId()
  const active = options.find(o => o.id === value) ?? options[0]

  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('pointerdown', onPointerDown)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  const selectOption = (opt: ProviderOption) => {
    if (!opt.available || disabled) return
    setOpen(false)
    if (opt.id !== value) onChange(opt.id)
  }

  const moveFocus = (dir: 1 | -1 | 'first' | 'last') => {
    const buttons = Array.from(listRef.current?.querySelectorAll<HTMLButtonElement>('button[role="option"]:not(:disabled)') ?? [])
    if (!buttons.length) return
    const currentIndex = buttons.indexOf(document.activeElement as HTMLButtonElement)
    let nextIndex = 0
    if (dir === 'first') nextIndex = 0
    else if (dir === 'last') nextIndex = buttons.length - 1
    else if (currentIndex === -1) nextIndex = dir === 1 ? 0 : buttons.length - 1
    else nextIndex = (currentIndex + dir + buttons.length) % buttons.length
    buttons[nextIndex]?.focus()
  }

  return (
    <div className="select-wrap catalog-select provider-select" ref={wrapRef}>
      <span>Провайдер</span>
      <button
        type="button"
        className="provider-trigger"
        aria-label="Провайдер"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={listboxId}
        disabled={disabled}
        onClick={() => { if (!disabled) setOpen(v => !v) }}
        onKeyDown={e => {
          if (disabled) return
          if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault()
            setOpen(true)
            requestAnimationFrame(() => moveFocus(e.key === 'ArrowDown' ? 'first' : 'last'))
          }
        }}
      >
        <span>{active?.label ?? value}</span>
        <ChevronDown size={12} />
      </button>
      {open && (
        <div
          id={listboxId}
          ref={listRef}
          className="provider-menu"
          role="listbox"
          aria-label="Провайдер"
          onKeyDown={e => {
            if (e.key === 'ArrowDown') { e.preventDefault(); moveFocus(1) }
            else if (e.key === 'ArrowUp') { e.preventDefault(); moveFocus(-1) }
            else if (e.key === 'Home') { e.preventDefault(); moveFocus('first') }
            else if (e.key === 'End') { e.preventDefault(); moveFocus('last') }
            else if (e.key === 'Escape') { e.preventDefault(); setOpen(false) }
          }}
        >
          {options.map(opt => (
            <button
              key={opt.id}
              type="button"
              role="option"
              aria-selected={opt.id === value}
              aria-disabled={!opt.available}
              disabled={!opt.available}
              className={`provider-option${opt.id === value ? ' active' : ''}`}
              onClick={() => selectOption(opt)}
            >
              {opt.label}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
