import { useEffect, useId, useRef } from 'react'
import { resolveTheme, useAppearance } from './appearanceStore'

/**
 * Ambient mesh gradient background with three GPU-animated orbs and anti-banding noise.
 * Light theme uses a clean flat surface without background gradients or orbs.
 */
export function AmbientBackground({ dimmed = false, active = false }: { dimmed?: boolean; active?: boolean }) {
  const ambientGradient = useAppearance(s => s.ambientGradient)
  const theme = useAppearance(s => s.theme)
  const filterId = useId()
  const rootRef = useRef<HTMLDivElement>(null)
  const rateRef = useRef(active ? 1 : 0)

  const enabled = ambientGradient && resolveTheme(theme) !== 'light'

  useEffect(() => {
    if (!enabled) return
    const root = rootRef.current
    if (!root || typeof requestAnimationFrame !== 'function') return

    const orbs = root.querySelectorAll<HTMLElement>('.ambient-orb')
    const anims: Animation[] = []
    orbs.forEach(orb => {
      if (typeof orb.getAnimations === 'function') {
        anims.push(...orb.getAnimations())
      }
    })
    if (anims.length === 0) return

    const targetRate = active ? 1 : 0
    const startRate = rateRef.current
    if (active) {
      for (const anim of anims) {
        if (anim.playState === 'paused') anim.play()
      }
    }
    if (startRate === targetRate) {
      for (const anim of anims) {
        anim.playbackRate = targetRate
        if (!active) anim.pause()
      }
      return
    }

    const duration = active ? 550 : 1000
    const startTime = performance.now()
    let rafId = 0

    const step = (now: number) => {
      const t = Math.min(1, (now - startTime) / duration)
      const eased = 1 - Math.pow(1 - t, 3)
      const rate = startRate + (targetRate - startRate) * eased
      rateRef.current = rate
      for (const anim of anims) {
        anim.playbackRate = rate
      }
      if (t < 1) {
        rafId = requestAnimationFrame(step)
      } else if (!active) {
        for (const anim of anims) anim.pause()
      }
    }

    rafId = requestAnimationFrame(step)
    return () => {
      if (rafId) cancelAnimationFrame(rafId)
    }
  }, [active, enabled])

  if (!enabled) return null
  return (
    <div
      ref={rootRef}
      className="ambient-bg"
      data-mode={dimmed ? 'workspace' : 'auth'}
      data-state={active ? 'active' : 'idle'}
      aria-hidden="true"
    >
      <div className="ambient-orb ambient-orb-blue" />
      <div className="ambient-orb ambient-orb-violet" />
      <div className="ambient-orb ambient-orb-cyan" />
      <svg className="ambient-noise" xmlns="http://www.w3.org/2000/svg">
        <filter id={filterId}>
          <feTurbulence type="fractalNoise" baseFrequency="0.8" numOctaves="3" stitchTiles="stitch" />
          <feColorMatrix type="saturate" values="0" />
        </filter>
        <rect width="100%" height="100%" filter={`url(#${filterId})`} />
      </svg>
    </div>
  )
}
