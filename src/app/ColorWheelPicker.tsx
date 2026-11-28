import { useEffect, useRef, useState } from 'react'
import { HEX_COLOR_RE } from './appearanceStore'

/** Pure HSV <-> HEX conversion utilities exported for unit tests as well. */
export function hsvToHex(h: number, s: number, v: number): string {
  const hue = ((h % 360) + 360) % 360
  const sat = Math.max(0, Math.min(1, s))
  const val = Math.max(0, Math.min(1, v))
  const c = val * sat
  const x = c * (1 - Math.abs(((hue / 60) % 2) - 1))
  const m = val - c
  let r = 0, g = 0, b = 0
  if (hue < 60) [r, g, b] = [c, x, 0]
  else if (hue < 120) [r, g, b] = [x, c, 0]
  else if (hue < 180) [r, g, b] = [0, c, x]
  else if (hue < 240) [r, g, b] = [0, x, c]
  else if (hue < 300) [r, g, b] = [x, 0, c]
  else [r, g, b] = [c, 0, x]
  const toHex = (n: number) => Math.round((n + m) * 255).toString(16).padStart(2, '0')
  return `#${toHex(r)}${toHex(g)}${toHex(b)}`
}

export function hexToHsv(hex: string): { h: number; s: number; v: number } {
  const clean = HEX_COLOR_RE.test(hex) ? hex : '#7c5cff'
  const r = parseInt(clean.slice(1, 3), 16) / 255
  const g = parseInt(clean.slice(3, 5), 16) / 255
  const b = parseInt(clean.slice(5, 7), 16) / 255
  const max = Math.max(r, g, b)
  const min = Math.min(r, g, b)
  const d = max - min
  let h = 0
  if (d > 0) {
    if (max === r) h = 60 * (((g - b) / d) % 6)
    else if (max === g) h = 60 * ((b - r) / d + 2)
    else h = 60 * ((r - g) / d + 4)
  }
  if (h < 0) h += 360
  const s = max === 0 ? 0 : d / max
  return { h, s, v: max }
}

interface ColorWheelPickerProps {
  value: string
  onChange: (hex: string) => void
  disabled?: boolean
  label?: string
}

const WHEEL_SIZE = 148

export function ColorWheelPicker({ value, onChange, disabled = false, label = 'HEX-код цвета' }: ColorWheelPickerProps) {
  const canvasRef = useRef<HTMLCanvasElement | null>(null)
  const [hsv, setHsv] = useState(() => hexToHsv(value))
  const [hexInput, setHexInput] = useState(value.toUpperCase())
  const [hexError, setHexError] = useState(false)

  useEffect(() => {
    setHsv(hexToHsv(value))
    setHexInput(value.toUpperCase())
    setHexError(false)
  }, [value])

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    const radius = WHEEL_SIZE / 2
    const image = ctx.createImageData(WHEEL_SIZE, WHEEL_SIZE)
    const data = image.data
    for (let y = 0; y < WHEEL_SIZE; y++) {
      for (let x = 0; x < WHEEL_SIZE; x++) {
        const dx = x - radius + 0.5
        const dy = y - radius + 0.5
        const dist = Math.hypot(dx, dy)
        const idx = (y * WHEEL_SIZE + x) * 4
        if (dist > radius) {
          data[idx + 3] = 0
          continue
        }
        const angle = (Math.atan2(dy, dx) * 180) / Math.PI
        const h = angle < 0 ? angle + 360 : angle
        const s = Math.min(1, dist / radius)
        const hex = hsvToHex(h, s, hsv.v)
        data[idx] = parseInt(hex.slice(1, 3), 16)
        data[idx + 1] = parseInt(hex.slice(3, 5), 16)
        data[idx + 2] = parseInt(hex.slice(5, 7), 16)
        data[idx + 3] = 255
      }
    }
    ctx.putImageData(image, 0, 0)
  }, [hsv.v])

  const pickAt = (clientX: number, clientY: number) => {
    const canvas = canvasRef.current
    if (!canvas || disabled) return
    const rect = canvas.getBoundingClientRect()
    const radius = rect.width / 2
    const dx = clientX - rect.left - radius
    const dy = clientY - rect.top - radius
    const dist = Math.min(radius, Math.hypot(dx, dy))
    const angle = (Math.atan2(dy, dx) * 180) / Math.PI
    const h = angle < 0 ? angle + 360 : angle
    const s = radius > 0 ? dist / radius : 0
    const nextHex = hsvToHex(h, s, hsv.v)
    setHsv({ h, s, v: hsv.v })
    setHexInput(nextHex.toUpperCase())
    setHexError(false)
    onChange(nextHex)
  }

  const rad = (hsv.h * Math.PI) / 180
  const markerR = (hsv.s * WHEEL_SIZE) / 2
  const markerX = WHEEL_SIZE / 2 + Math.cos(rad) * markerR
  const markerY = WHEEL_SIZE / 2 + Math.sin(rad) * markerR

  return (
    <div className={`color-wheel-picker${disabled ? ' is-disabled' : ''}`}>
      <div
        className="color-wheel-stage"
        onPointerDown={e => {
          if (disabled) return
          e.currentTarget.setPointerCapture(e.pointerId)
          pickAt(e.clientX, e.clientY)
        }}
        onPointerMove={e => {
          if (disabled || e.buttons === 0) return
          pickAt(e.clientX, e.clientY)
        }}
      >
        <canvas
          ref={canvasRef}
          width={WHEEL_SIZE}
          height={WHEEL_SIZE}
          className="color-wheel-canvas"
          aria-label="Цветовой круг"
        />
        <span
          className="color-wheel-thumb"
          style={{ left: `${markerX}px`, top: `${markerY}px`, background: value }}
          aria-hidden="true"
        />
      </div>
      <div className="color-wheel-controls">
        <label className="color-wheel-slider-row">
          <span>Яркость</span>
          <input
            type="range"
            min={15}
            max={100}
            value={Math.round(hsv.v * 100)}
            disabled={disabled}
            aria-label="Яркость цвета"
            onChange={e => {
              const v = Number(e.target.value) / 100
              const nextHex = hsvToHex(hsv.h, hsv.s, v)
              setHsv({ ...hsv, v })
              setHexInput(nextHex.toUpperCase())
              setHexError(false)
              onChange(nextHex)
            }}
          />
        </label>
        <label className="color-wheel-hex-row">
          <span>HEX</span>
          <div className="color-hex-field">
            <span className="color-hex-preview" style={{ background: value }} aria-hidden="true" />
            <input
              type="text"
              value={hexInput}
              disabled={disabled}
              aria-label={label}
              maxLength={7}
              spellCheck={false}
              className={hexError ? 'is-invalid' : undefined}
              onChange={e => {
                const raw = e.target.value.trim()
                const formatted = raw.startsWith('#') ? raw : `#${raw}`
                setHexInput(formatted.toUpperCase())
                if (HEX_COLOR_RE.test(formatted)) {
                  setHexError(false)
                  setHsv(hexToHsv(formatted))
                  onChange(formatted.toLowerCase())
                } else {
                  setHexError(true)
                }
              }}
              onBlur={() => {
                if (!HEX_COLOR_RE.test(hexInput)) {
                  setHexInput(value.toUpperCase())
                  setHexError(false)
                }
              }}
            />
          </div>
        </label>
      </div>
    </div>
  )
}
