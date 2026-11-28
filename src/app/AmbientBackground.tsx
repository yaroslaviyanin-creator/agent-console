import { useId } from 'react'
import { resolveTheme, useAppearance } from './appearanceStore'

/**
 * Ambient mesh gradient background with three GPU-animated orbs and anti-banding noise.
 * Light theme uses a clean flat surface without background gradients or orbs.
 */
export function AmbientBackground({ dimmed = false, active = false }: { dimmed?: boolean; active?: boolean }) {
  const ambientGradient = useAppearance(s => s.ambientGradient)
  const theme = useAppearance(s => s.theme)
  const filterId = useId()
  if (!ambientGradient || resolveTheme(theme) === 'light') return null
  return (
    <div
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
