import { useEffect, useRef, useState } from 'react'
import { CornerDownLeft, LoaderCircle, MessageSquare, Search, X } from 'lucide-react'
import type { SearchResult } from '../protocol/types'
import { jumpToSearchResult, searchChatsAndMessages } from './chatStore'

const shortDate = new Intl.DateTimeFormat('ru', { day: '2-digit', month: 'short' })

function HighlightedSnippet({ text, query }: { text: string; query: string }) {
  const q = query.trim()
  if (!q) return <>{text}</>
  const lowerText = text.toLowerCase()
  const lowerQ = q.toLowerCase()
  const parts: Array<{ text: string; match: boolean }> = []
  let pos = 0
  while (pos < text.length) {
    const idx = lowerText.indexOf(lowerQ, pos)
    if (idx < 0) {
      parts.push({ text: text.slice(pos), match: false })
      break
    }
    if (idx > pos) {
      parts.push({ text: text.slice(pos, idx), match: false })
    }
    parts.push({ text: text.slice(idx, idx + q.length), match: true })
    pos = idx + q.length
  }
  return (
    <>
      {parts.map((part, i) =>
        part.match
          ? <mark key={i} className="search-mark">{part.text}</mark>
          : <span key={i}>{part.text}</span>,
      )}
    </>
  )
}

export function SearchPalette({
  open,
  onClose,
  onAfterSelect,
}: {
  open: boolean
  onClose: () => void
  onAfterSelect?: () => void
}) {
  const [query, setQuery] = useState('')
  const [results, setResults] = useState<SearchResult[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [selectedIndex, setSelectedIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLUListElement>(null)

  useEffect(() => {
    if (!open) return
    setSelectedIndex(0)
    const id = window.setTimeout(() => {
      inputRef.current?.focus()
      inputRef.current?.select()
    }, 10)
    return () => window.clearTimeout(id)
  }, [open])

  useEffect(() => {
    if (!open) return
    const trimmed = query.trim()
    if (!trimmed) {
      setResults([])
      setLoading(false)
      setError('')
      setSelectedIndex(0)
      return
    }
    const controller = new AbortController()
    setLoading(true)
    setError('')
    const timer = window.setTimeout(() => {
      void searchChatsAndMessages(trimmed, controller.signal)
        .then(items => {
          if (controller.signal.aborted) return
          setResults(items)
          setSelectedIndex(0)
          setLoading(false)
        })
        .catch(err => {
          if (controller.signal.aborted) return
          setError(err instanceof Error ? err.message : 'Не удалось выполнить поиск.')
          setLoading(false)
        })
    }, 250)

    return () => {
      window.clearTimeout(timer)
      controller.abort()
    }
  }, [open, query])

  useEffect(() => {
    const activeEl = listRef.current?.children[selectedIndex] as HTMLElement | undefined
    activeEl?.scrollIntoView({ block: 'nearest' })
  }, [selectedIndex])

  if (!open) return null

  const choose = (hit: SearchResult) => {
    jumpToSearchResult(hit)
    onClose()
    onAfterSelect?.()
  }

  return (
    <div
      className="search-palette-backdrop"
      role="presentation"
      onClick={e => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <div
        className="search-palette-dialog"
        role="dialog"
        aria-modal="true"
        aria-label="Поиск по чатам и сообщениям"
        onKeyDown={e => {
          if (e.key === 'Escape') {
            e.preventDefault()
            onClose()
          } else if (e.key === 'ArrowDown') {
            e.preventDefault()
            if (results.length > 0) {
              setSelectedIndex(i => (i + 1) % results.length)
            }
          } else if (e.key === 'ArrowUp') {
            e.preventDefault()
            if (results.length > 0) {
              setSelectedIndex(i => (i - 1 + results.length) % results.length)
            }
          } else if (e.key === 'Enter' && results[selectedIndex]) {
            e.preventDefault()
            choose(results[selectedIndex]!)
          }
        }}
      >
        <div className="search-palette-head">
          <Search size={16} className="search-palette-icon" aria-hidden="true" />
          <input
            ref={inputRef}
            type="text"
            className="search-palette-input"
            placeholder="Поиск по чатам и сообщениям…"
            aria-label="Поисковый запрос"
            value={query}
            onChange={e => setQuery(e.target.value)}
          />
          {loading && <LoaderCircle size={15} className="att-spin search-palette-spin" aria-hidden="true" />}
          {query && (
            <button
              type="button"
              className="icon-btn"
              aria-label="Очистить поиск"
              onClick={() => {
                setQuery('')
                inputRef.current?.focus()
              }}
            >
              <X size={14} />
            </button>
          )}
          <kbd className="search-kbd">Esc</kbd>
        </div>

        <div className="search-palette-body">
          {!query.trim() ? (
            <div className="search-palette-empty">
              Введите слово или фразу для поиска по названиям чатов и тексту переписки.
            </div>
          ) : error ? (
            <div className="search-palette-empty is-error" role="alert">{error}</div>
          ) : !loading && results.length === 0 ? (
            <div className="search-palette-empty">Ничего не найдено по запросу «{query.trim()}».</div>
          ) : (
            <ul className="search-palette-list" ref={listRef} role="listbox" aria-label="Результаты поиска">
              {results.map((hit, idx) => {
                const roleLabel = hit.role === 'user' ? 'Запрос' : hit.role === 'assistant' ? 'Ответ' : 'Чат'
                return (
                  <li
                    key={`${hit.chatId}-${hit.messageId || 'title'}-${idx}`}
                    role="option"
                    aria-selected={idx === selectedIndex}
                    className={`search-palette-item${idx === selectedIndex ? ' is-active' : ''}`}
                    onMouseEnter={() => setSelectedIndex(idx)}
                    onClick={() => choose(hit)}
                  >
                    <div className="search-item-top">
                      <span className="search-item-chat">
                        <MessageSquare size={13} aria-hidden="true" />
                        <strong>{hit.chatTitle}</strong>
                      </span>
                      <span className="search-item-meta">
                        <span className={`search-role-badge is-${hit.role}`}>{roleLabel}</span>
                        {hit.createdAt > 0 && <time>{shortDate.format(hit.createdAt)}</time>}
                      </span>
                    </div>
                    <p className="search-item-snippet">
                      <HighlightedSnippet text={hit.snippet} query={query} />
                    </p>
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        <div className="search-palette-foot">
          <span><kbd>↑</kbd><kbd>↓</kbd> выбор</span>
          <span><CornerDownLeft size={11} aria-hidden="true" /> открыть</span>
          <span><kbd>Esc</kbd> закрыть</span>
        </div>
      </div>
    </div>
  )
}
