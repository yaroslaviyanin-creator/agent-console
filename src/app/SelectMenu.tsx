import { useEffect, useId, useRef, useState } from 'react'
import { ChevronDown } from 'lucide-react'

export interface SelectOption<T extends string = string> {
  value: T
  label: string
  disabled?: boolean
}

export function SelectMenu<T extends string = string>({
  value,
  options,
  disabled = false,
  ariaLabel,
  placeholder,
  variant = 'inline',
  placement = 'auto',
  className,
  id,
  onChange,
}: {
  value: T
  options: readonly SelectOption<T>[]
  disabled?: boolean
  ariaLabel?: string
  placeholder?: string
  variant?: 'inline' | 'field'
  placement?: 'auto' | 'top' | 'bottom'
  className?: string
  id?: string
  onChange: (value: T) => void
}) {
  const [open, setOpen] = useState(false)
  const [openUp, setOpenUp] = useState(false)
  const [alignRight, setAlignRight] = useState(false)
  const wrapRef = useRef<HTMLDivElement>(null)
  const triggerRef = useRef<HTMLButtonElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  const listboxId = useId()
  const active = options.find(o => o.value === value)
  const displayLabel = active?.label ?? placeholder ?? value

  useEffect(() => {
    if (disabled) setOpen(false)
  }, [disabled])

  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: PointerEvent) => {
      if (!wrapRef.current?.contains(e.target as Node)) setOpen(false)
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        setOpen(false)
        triggerRef.current?.focus()
      }
    }
    window.addEventListener('pointerdown', onPointerDown)
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('pointerdown', onPointerDown)
      window.removeEventListener('keydown', onKeyDown)
    }
  }, [open])

  const updateMenuPosition = () => {
    const rect = wrapRef.current?.getBoundingClientRect()
    if (!rect) return
    if (placement === 'top') setOpenUp(true)
    else if (placement === 'bottom') setOpenUp(false)
    else {
      const spaceBelow = window.innerHeight - rect.bottom
      setOpenUp(spaceBelow < 220 && rect.top > spaceBelow)
    }
    setAlignRight(rect.left + 190 > window.innerWidth && rect.right > 190)
  }

  const toggleOpen = () => {
    if (disabled) return
    setOpen(prev => {
      if (!prev) updateMenuPosition()
      return !prev
    })
  }

  const selectOption = (opt: SelectOption<T>) => {
    if (opt.disabled || disabled) return
    setOpen(false)
    if (opt.value !== value) onChange(opt.value)
  }

  const moveFocus = (dir: 1 | -1 | 'first' | 'last') => {
    const buttons = Array.from(
      listRef.current?.querySelectorAll<HTMLButtonElement>('button[role="option"]:not(:disabled)') ?? []
    )
    if (!buttons.length) return
    const currentIndex = buttons.indexOf(document.activeElement as HTMLButtonElement)
    let nextIndex = 0
    if (dir === 'first') nextIndex = 0
    else if (dir === 'last') nextIndex = buttons.length - 1
    else if (currentIndex === -1) nextIndex = dir === 1 ? 0 : buttons.length - 1
    else nextIndex = (currentIndex + dir + buttons.length) % buttons.length
    buttons[nextIndex]?.focus()
  }

  const wrapClasses = [
    'select-control',
    variant === 'field' ? 'select-control-field' : 'select-control-inline',
    className,
  ].filter(Boolean).join(' ')

  const menuClasses = [
    'select-menu',
    openUp ? 'open-up' : '',
    alignRight ? 'align-right' : '',
  ].filter(Boolean).join(' ')

  return (
    <div className={wrapClasses} ref={wrapRef}>
      <button
        id={id}
        ref={triggerRef}
        type="button"
        className={`select-trigger${variant === 'field' ? ' select-trigger-field' : ''}`}
        aria-label={ariaLabel}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={listboxId}
        disabled={disabled}
        onClick={toggleOpen}
        onKeyDown={e => {
          if (disabled) return
          if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
            e.preventDefault()
            updateMenuPosition()
            setOpen(true)
            requestAnimationFrame(() => moveFocus(e.key === 'ArrowDown' ? 'first' : 'last'))
          }
        }}
      >
        <span className="select-trigger-label">{displayLabel}</span>
        <ChevronDown size={variant === 'field' ? 14 : 12} />
      </button>
      {open && (
        <div
          id={listboxId}
          ref={listRef}
          className={menuClasses}
          role="listbox"
          aria-label={ariaLabel}
          onKeyDown={e => {
            if (e.key === 'ArrowDown') { e.preventDefault(); moveFocus(1) }
            else if (e.key === 'ArrowUp') { e.preventDefault(); moveFocus(-1) }
            else if (e.key === 'Home') { e.preventDefault(); moveFocus('first') }
            else if (e.key === 'End') { e.preventDefault(); moveFocus('last') }
            else if (e.key === 'Escape') {
              e.preventDefault()
              setOpen(false)
              triggerRef.current?.focus()
            }
          }}
        >
          {options.map(opt => (
            <button
              key={opt.value}
              type="button"
              role="option"
              aria-selected={opt.value === value}
              aria-disabled={!!opt.disabled}
              disabled={!!opt.disabled}
              className={`select-option${opt.value === value ? ' active' : ''}`}
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
