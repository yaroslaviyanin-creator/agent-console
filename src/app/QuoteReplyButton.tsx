import { useEffect, useState } from 'react'
import type { RefObject } from 'react'
import { Quote } from 'lucide-react'

export function formatQuoteMarkdown(selected: string): string {
  const clean = selected.replace(/\r\n/g, '\n').trim()
  if (!clean) return ''
  const quoted = clean
    .split('\n')
    .map(line => `> ${line}`)
    .join('\n')
  return `${quoted}\n\n`
}

export function QuoteReplyButton({
  feedRef,
  enabled,
  onQuote,
}: {
  feedRef: RefObject<HTMLElement | null>
  enabled: boolean
  onQuote: (quotedText: string) => void
}) {
  const [popup, setPopup] = useState<{ text: string; top: number; left: number } | null>(null)

  useEffect(() => {
    if (!enabled) {
      setPopup(null)
      return
    }

    const updateSelection = () => {
      const sel = window.getSelection()
      const container = feedRef.current
      if (!sel || sel.isCollapsed || !container || sel.rangeCount === 0) {
        setPopup(null)
        return
      }
      const activeEl = document.activeElement
      if (activeEl && (activeEl.tagName === 'TEXTAREA' || activeEl.tagName === 'INPUT')) {
        setPopup(null)
        return
      }
      const range = sel.getRangeAt(0)
      const common = range.commonAncestorContainer
      if (!container.contains(common)) {
        setPopup(null)
        return
      }
      const rawText = sel.toString().trim()
      if (!rawText) {
        setPopup(null)
        return
      }
      const rect = range.getBoundingClientRect()
      if (rect.width === 0 && rect.height === 0) {
        setPopup(null)
        return
      }
      const left = Math.min(Math.max(70, rect.left + rect.width / 2), window.innerWidth - 90)
      const top = Math.max(12, rect.top - 42)
      setPopup({ text: rawText, top, left })
    }

    const hide = () => setPopup(null)
    document.addEventListener('selectionchange', updateSelection)
    window.addEventListener('scroll', hide, true)
    window.addEventListener('resize', hide)
    return () => {
      document.removeEventListener('selectionchange', updateSelection)
      window.removeEventListener('scroll', hide, true)
      window.removeEventListener('resize', hide)
    }
  }, [feedRef, enabled])

  if (!popup) return null

  return (
    <button
      type="button"
      className="quote-reply-btn"
      style={{ top: popup.top, left: popup.left }}
      onMouseDown={e => e.preventDefault()}
      onClick={() => {
        const formatted = formatQuoteMarkdown(popup.text)
        if (formatted) onQuote(formatted)
        window.getSelection()?.removeAllRanges()
        setPopup(null)
      }}
    >
      <Quote size={13} aria-hidden="true" />
      <span>Спросить об этом</span>
    </button>
  )
}
