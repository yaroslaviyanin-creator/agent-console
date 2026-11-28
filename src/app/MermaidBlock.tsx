import { useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { Check, Code2, Copy, Maximize2, Workflow, X, ZoomIn, ZoomOut } from 'lucide-react'
import { resolveTheme, useAppearance } from './appearanceStore'

export function getMermaidInitConfig(theme: 'dark' | 'light') {
  return {
    startOnLoad: false,
    securityLevel: 'strict' as const,
    theme: theme === 'light' ? ('default' as const) : ('dark' as const),
  }
}

export function MermaidBlock({ code, streaming = false }: { code: string; streaming?: boolean }) {
  const themePref = useAppearance(s => s.theme)
  const resolvedTheme = resolveTheme(themePref)
  const rawId = useId().replace(/[^a-zA-Z0-9_-]/g, '')
  const [tab, setTab] = useState<'diagram' | 'code'>('diagram')
  const [svg, setSvg] = useState('')
  const [error, setError] = useState('')
  const [copied, setCopied] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const [zoom, setZoom] = useState(1)
  const copyTimerRef = useRef<number | undefined>(undefined)

  useEffect(() => () => {
    if (copyTimerRef.current !== undefined) window.clearTimeout(copyTimerRef.current)
  }, [])

  useEffect(() => {
    if (streaming || !code.trim()) {
      setSvg('')
      setError('')
      return
    }
    let cancelled = false
    ;(async () => {
      try {
        const mod = await import('mermaid')
        const mermaid = mod.default
        mermaid.initialize(getMermaidInitConfig(resolvedTheme))
        const renderId = `mmd-${rawId}-${Date.now()}`
        const result = await mermaid.render(renderId, code)
        if (!cancelled) {
          setSvg(result.svg)
          setError('')
        }
      } catch (err) {
        if (!cancelled) {
          setSvg('')
          setError(err instanceof Error ? err.message : 'Ошибка синтаксиса диаграммы')
        }
      }
    })()
    return () => {
      cancelled = true
    }
  }, [code, resolvedTheme, streaming, rawId])

  useEffect(() => {
    if (!expanded) return
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setExpanded(false)
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [expanded])

  async function copyCode() {
    if (!code) return
    try {
      await navigator.clipboard.writeText(code)
    } catch {
      const area = document.createElement('textarea')
      area.value = code
      area.style.position = 'fixed'
      area.style.opacity = '0'
      document.body.appendChild(area)
      area.select()
      document.execCommand('copy')
      document.body.removeChild(area)
    }
    setCopied(true)
    if (copyTimerRef.current !== undefined) window.clearTimeout(copyTimerRef.current)
    copyTimerRef.current = window.setTimeout(() => setCopied(false), 1800)
  }

  const showDiagram = !streaming && tab === 'diagram' && !!svg && !error

  return (
    <div className="mermaid-block">
      <div className="code-block-head mermaid-block-head">
        <span className="code-block-lang">
          <Workflow size={12} aria-hidden="true" />
          <span>mermaid</span>
        </span>
        <div className="mermaid-head-actions">
          {!streaming && svg && (
            <div className="mermaid-tabs" role="tablist" aria-label="Вид диаграммы">
              <button
                type="button"
                role="tab"
                aria-selected={tab === 'diagram'}
                className={`mermaid-tab-btn${tab === 'diagram' ? ' is-active' : ''}`}
                onClick={() => setTab('diagram')}
              >
                Диаграмма
              </button>
              <button
                type="button"
                role="tab"
                aria-selected={tab === 'code'}
                className={`mermaid-tab-btn${tab === 'code' ? ' is-active' : ''}`}
                onClick={() => setTab('code')}
              >
                <Code2 size={11} aria-hidden="true" />
                Код
              </button>
            </div>
          )}
          {showDiagram && (
            <button
              type="button"
              className="code-copy-btn"
              onClick={() => { setZoom(1); setExpanded(true) }}
              aria-label="Развернуть диаграмму"
              title="Развернуть"
            >
              <Maximize2 size={12} />
              <span>Развернуть</span>
            </button>
          )}
          <button type="button" className="code-copy-btn" onClick={() => void copyCode()} aria-label="Копировать код диаграммы">
            {copied ? <><Check size={12} /><span>Скопировано</span></> : <><Copy size={12} /><span>Копировать</span></>}
          </button>
        </div>
      </div>

      {showDiagram ? (
        <div className="mermaid-canvas" dangerouslySetInnerHTML={{ __html: svg }} />
      ) : (
        <>
          {error && !streaming && (
            <div className="mermaid-error-note" role="status">
              Не удалось отрисовать диаграмму: показан исходный код.
            </div>
          )}
          <pre><code>{code}</code></pre>
        </>
      )}

      {expanded && svg && createPortal(
        <div className="modal-backdrop mermaid-lightbox-backdrop" role="presentation" onClick={e => { if (e.target === e.currentTarget) setExpanded(false) }}>
          <div className="mermaid-lightbox" role="dialog" aria-modal="true" aria-label="Диаграмма Mermaid">
            <div className="mermaid-lightbox-head">
              <span>Диаграмма Mermaid</span>
              <div className="mermaid-zoom-controls">
                <button type="button" className="icon-btn" aria-label="Уменьшить" onClick={() => setZoom(z => Math.max(0.4, +(z - 0.2).toFixed(2)))}>
                  <ZoomOut size={15} />
                </button>
                <button type="button" className="alt-action" onClick={() => setZoom(1)}>
                  {Math.round(zoom * 100)}%
                </button>
                <button type="button" className="icon-btn" aria-label="Увеличить" onClick={() => setZoom(z => Math.min(3, +(z + 0.2).toFixed(2)))}>
                  <ZoomIn size={15} />
                </button>
                <button type="button" className="icon-btn" aria-label="Закрыть просмотр" onClick={() => setExpanded(false)}>
                  <X size={16} />
                </button>
              </div>
            </div>
            <div className="mermaid-lightbox-body">
              <div
                className="mermaid-lightbox-svg"
                style={{ transform: `scale(${zoom})`, transformOrigin: 'center center' }}
                dangerouslySetInnerHTML={{ __html: svg }}
              />
            </div>
          </div>
        </div>,
        document.body,
      )}
    </div>
  )
}
