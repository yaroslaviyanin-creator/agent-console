import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertTriangle, Check, ChevronDown, ChevronLeft, ChevronUp, Copy, Download,
  FileArchive, FileCode, FileText, Image as ImageIcon, Maximize2, Minimize2,
  Paperclip, RotateCw, X, ZoomIn, ZoomOut,
} from 'lucide-react'
import hljs from 'highlight.js/lib/core'
import typescript from 'highlight.js/lib/languages/typescript'
import javascript from 'highlight.js/lib/languages/javascript'
import json from 'highlight.js/lib/languages/json'
import bash from 'highlight.js/lib/languages/bash'
import python from 'highlight.js/lib/languages/python'
import xml from 'highlight.js/lib/languages/xml'
import css from 'highlight.js/lib/languages/css'
import go from 'highlight.js/lib/languages/go'
import cpp from 'highlight.js/lib/languages/cpp'
import sql from 'highlight.js/lib/languages/sql'
import yaml from 'highlight.js/lib/languages/yaml'
import markdownLang from 'highlight.js/lib/languages/markdown'
import plaintext from 'highlight.js/lib/languages/plaintext'
import * as pdfjsLib from 'pdfjs-dist'
import pdfWorkerUrl from 'pdfjs-dist/build/pdf.worker.min.mjs?url'
import mammoth from 'mammoth'
import type { MessageAttachment } from '../protocol/types'
import type { RemoteTransport } from '../transport/RemoteTransport'
import { formatAttachmentSize } from './attachments'
import { RichText } from './Markdown'
import {
  getCodeLanguage, getFileExtension, getFileViewerMode,
  MAX_FILE_VIEWER_BYTES, parseDelimitedTable, sanitizeDocxHtml,
} from './fileViewerUtils'

pdfjsLib.GlobalWorkerOptions.workerSrc = pdfWorkerUrl

for (const [name, lang] of Object.entries({
  typescript, javascript, json, bash, python, xml, css, go, cpp, sql, yaml,
  markdown: markdownLang, plaintext,
})) {
  hljs.registerLanguage(name, lang)
}

function fileIcon(att: Pick<MessageAttachment, 'kind' | 'name'>) {
  const ext = getFileExtension(att.name)
  if (att.kind === 'archive' || ext === '.zip') return FileArchive
  if (att.kind === 'image' && ext !== '.svg') return ImageIcon
  if (att.kind === 'pdf' || att.kind === 'document' || ext === '.pdf' || ext === '.docx') return FileText
  if (att.kind === 'text' || ext) return FileCode
  return Paperclip
}

function CodeSourceView({ code, language }: { code: string; language: string }) {
  const lines = useMemo(() => code.split(/\r?\n/), [code])
  const highlighted = useMemo(() => {
    try {
      const lang = hljs.getLanguage(language) ? language : 'plaintext'
      return hljs.highlight(code, { language: lang }).value
    } catch {
      return code
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
    }
  }, [code, language])

  return (
    <div className="fv-code-scroll">
      <div className="fv-code-wrap">
        <div className="fv-code-gutter" aria-hidden="true">
          {lines.map((_, i) => <span key={i}>{i + 1}</span>)}
        </div>
        <pre className="fv-code-pre"><code className="hljs" dangerouslySetInnerHTML={{ __html: highlighted }} /></pre>
      </div>
    </div>
  )
}

function PdfPageCanvas({
  pdfDoc,
  pageNumber,
  scale,
  visible,
}: {
  pdfDoc: pdfjsLib.PDFDocumentProxy
  pageNumber: number
  scale: number
  visible: boolean
}) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [dims, setDims] = useState<{ width: number; height: number }>({ width: Math.round(595 * scale), height: Math.round(842 * scale) })
  const [renderedKey, setRenderedKey] = useState('')

  useEffect(() => {
    let cancelled = false
    void pdfDoc.getPage(pageNumber).then(page => {
      if (cancelled) return
      const vp = page.getViewport({ scale })
      setDims({ width: Math.round(vp.width), height: Math.round(vp.height) })
    }).catch(() => {})
    return () => { cancelled = true }
  }, [pdfDoc, pageNumber, scale])

  useEffect(() => {
    if (!visible) return
    const key = `${pageNumber}@${scale}`
    if (renderedKey === key) return
    let cancelled = false
    let renderTask: pdfjsLib.RenderTask | null = null

    void pdfDoc.getPage(pageNumber).then(page => {
      if (cancelled) return
      const canvas = canvasRef.current
      if (!canvas) return
      const dpr = Math.min(window.devicePixelRatio || 1, 2.5)
      const viewport = page.getViewport({ scale })
      const w = Math.max(1, Math.round(viewport.width))
      const h = Math.max(1, Math.round(viewport.height))
      setDims({ width: w, height: h })

      canvas.width = Math.round(w * dpr)
      canvas.height = Math.round(h * dpr)
      canvas.style.width = `${w}px`
      canvas.style.height = `${h}px`

      const ctx = canvas.getContext('2d')
      if (!ctx) return
      renderTask = page.render({
        canvas,
        canvasContext: ctx,
        viewport,
        transform: dpr !== 1 ? [dpr, 0, 0, dpr, 0, 0] : undefined,
      })
      return renderTask.promise
    }).then(() => {
      if (!cancelled) setRenderedKey(key)
    }).catch(() => {
      // Ignore cancelled render tasks when zooming or unmounting.
    })

    return () => {
      cancelled = true
      renderTask?.cancel()
    }
  }, [pdfDoc, pageNumber, scale, visible, renderedKey])

  return (
    <div
      className="fv-pdf-page-card"
      data-page={pageNumber}
      style={{ width: `${dims.width}px`, minHeight: `${dims.height}px` }}
    >
      <canvas ref={canvasRef} className="fv-pdf-canvas" aria-label={`Страница ${pageNumber}`} />
      {renderedKey !== `${pageNumber}@${scale}` && (
        <span className="fv-pdf-page-placeholder">Стр. {pageNumber}</span>
      )}
    </div>
  )
}

function PdfCanvasViewer({ buffer }: { buffer: ArrayBuffer }) {
  const [pdfDoc, setPdfDoc] = useState<pdfjsLib.PDFDocumentProxy | null>(null)
  const [numPages, setNumPages] = useState(0)
  const [currentPage, setCurrentPage] = useState(1)
  const [scale, setScale] = useState(1.1)
  const [visiblePages, setVisiblePages] = useState<Set<number>>(() => new Set([1, 2, 3]))
  const [pdfError, setPdfError] = useState('')
  const scrollRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    let cancelled = false
    setPdfDoc(null)
    setNumPages(0)
    setCurrentPage(1)
    setPdfError('')
    setVisiblePages(new Set([1, 2, 3]))

    const loadingTask = pdfjsLib.getDocument({ data: new Uint8Array(buffer) })
    void loadingTask.promise.then(doc => {
      if (cancelled) return
      setPdfDoc(doc)
      setNumPages(doc.numPages)
    }).catch(() => {
      if (!cancelled) setPdfError('Не удалось открыть PDF-документ.')
    })

    return () => {
      cancelled = true
      void loadingTask.destroy()
    }
  }, [buffer])

  useEffect(() => {
    const root = scrollRef.current
    if (!root || !pdfDoc || numPages === 0) return

    const observer = new IntersectionObserver(entries => {
      setVisiblePages(prev => {
        const next = new Set(prev)
        for (const entry of entries) {
          const p = Number((entry.target as HTMLElement).dataset.page)
          if (p > 0 && entry.isIntersecting) next.add(p)
        }
        return next
      })

      let bestPage = 0
      let bestRatio = 0
      for (const entry of entries) {
        const p = Number((entry.target as HTMLElement).dataset.page)
        if (p > 0 && entry.intersectionRatio > bestRatio) {
          bestRatio = entry.intersectionRatio
          bestPage = p
        }
      }
      if (bestPage > 0) setCurrentPage(bestPage)
    }, { root, rootMargin: '420px 0px', threshold: [0.01, 0.25, 0.6] })

    const cards = root.querySelectorAll<HTMLElement>('.fv-pdf-page-card')
    cards.forEach(card => observer.observe(card))
    return () => observer.disconnect()
  }, [pdfDoc, numPages, scale])

  const jumpToPage = (target: number) => {
    const clamped = Math.max(1, Math.min(numPages, target))
    setCurrentPage(clamped)
    const el = scrollRef.current?.querySelector<HTMLElement>(`.fv-pdf-page-card[data-page="${clamped}"]`)
    el?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }

  if (pdfError) {
    return (
      <div className="fv-state-card">
        <AlertTriangle size={24} />
        <p>{pdfError}</p>
      </div>
    )
  }

  if (!pdfDoc) {
    return (
      <div className="fv-loading">
        <div className="skeleton-line is-medium" />
        <div className="skeleton-line is-long" />
        <div className="skeleton-line is-short" />
      </div>
    )
  }

  return (
    <div className="fv-doc-shell">
      <div className="fv-doc-viewport" ref={scrollRef}>
        <div className="fv-pdf-stack">
          {Array.from({ length: numPages }, (_, idx) => idx + 1).map(pageNumber => (
            <PdfPageCanvas
              key={pageNumber}
              pdfDoc={pdfDoc}
              pageNumber={pageNumber}
              scale={scale}
              visible={visiblePages.has(pageNumber)}
            />
          ))}
        </div>
      </div>
      <div className="fv-floating-bar" role="toolbar" aria-label="Управление PDF">
        <button type="button" className="fv-pill-btn" onClick={() => jumpToPage(currentPage - 1)} disabled={currentPage <= 1} aria-label="Предыдущая страница">
          <ChevronUp size={14} />
        </button>
        <span className="fv-pill-text">Страница {currentPage} / {numPages}</span>
        <button type="button" className="fv-pill-btn" onClick={() => jumpToPage(currentPage + 1)} disabled={currentPage >= numPages} aria-label="Следующая страница">
          <ChevronDown size={14} />
        </button>
        <span className="fv-pill-sep" aria-hidden="true" />
        <button type="button" className="fv-pill-btn" onClick={() => setScale(s => Math.max(0.5, Math.round((s - 0.2) * 100) / 100))} aria-label="Уменьшить масштаб">
          <ZoomOut size={14} />
        </button>
        <button type="button" className="fv-pill-btn fv-pill-zoom" onClick={() => setScale(1.0)} title="Сбросить масштаб до 100%">
          {Math.round(scale * 100)}%
        </button>
        <button type="button" className="fv-pill-btn" onClick={() => setScale(s => Math.min(2.5, Math.round((s + 0.2) * 100) / 100))} aria-label="Увеличить масштаб">
          <ZoomIn size={14} />
        </button>
      </div>
    </div>
  )
}

function DocxSheetViewer({ html }: { html: string }) {
  const [zoom, setZoom] = useState(1)
  return (
    <div className="fv-doc-shell">
      <div className="fv-doc-viewport">
        <article
          className="fv-docx-sheet"
          style={{ fontSize: `${Math.round(15 * zoom * 10) / 10}px` }}
          dangerouslySetInnerHTML={{ __html: html }}
        />
      </div>
      <div className="fv-floating-bar" role="toolbar" aria-label="Масштаб документа">
        <button type="button" className="fv-pill-btn" onClick={() => setZoom(z => Math.max(0.6, Math.round((z - 0.15) * 100) / 100))} aria-label="Уменьшить масштаб">
          <ZoomOut size={14} />
        </button>
        <button type="button" className="fv-pill-btn fv-pill-zoom" onClick={() => setZoom(1)} title="Сбросить масштаб до 100%">
          {Math.round(zoom * 100)}%
        </button>
        <button type="button" className="fv-pill-btn" onClick={() => setZoom(z => Math.min(2, Math.round((z + 0.15) * 100) / 100))} aria-label="Увеличить масштаб">
          <ZoomIn size={14} />
        </button>
      </div>
    </div>
  )
}

function ImageStageViewer({ src, name }: { src: string; name: string }) {
  const [zoom, setZoom] = useState<'fit' | number>('fit')
  return (
    <div className="fv-doc-shell">
      <div className="fv-image-viewport">
        <img
          src={src}
          alt={name}
          className={`fv-image-el${zoom === 'fit' ? ' is-fit' : ''}`}
          style={zoom === 'fit' ? undefined : { width: `${Math.round(zoom * 100)}%`, maxWidth: 'none', maxHeight: 'none' }}
        />
      </div>
      <div className="fv-floating-bar" role="toolbar" aria-label="Масштаб изображения">
        <button
          type="button"
          className="fv-pill-btn"
          onClick={() => setZoom(z => Math.max(0.25, Math.round(((z === 'fit' ? 1 : z) - 0.25) * 100) / 100))}
          aria-label="Уменьшить"
        >
          <ZoomOut size={14} />
        </button>
        <button type="button" className={`fv-pill-btn${zoom === 'fit' ? ' is-active' : ''}`} onClick={() => setZoom('fit')}>
          Вписать
        </button>
        <button type="button" className={`fv-pill-btn fv-pill-zoom${zoom === 1 ? ' is-active' : ''}`} onClick={() => setZoom(1)}>
          {zoom === 'fit' ? '100%' : `${Math.round(zoom * 100)}%`}
        </button>
        <button
          type="button"
          className="fv-pill-btn"
          onClick={() => setZoom(z => Math.min(3, Math.round(((z === 'fit' ? 1 : z) + 0.25) * 100) / 100))}
          aria-label="Увеличить"
        >
          <ZoomIn size={14} />
        </button>
      </div>
    </div>
  )
}

export function FileViewer({
  file,
  remote,
  expanded,
  onToggleExpand,
  onClose,
}: {
  file: MessageAttachment
  remote: RemoteTransport | null
  expanded: boolean
  onToggleExpand: () => void
  onClose: () => void
}) {
  const mode = useMemo(() => getFileViewerMode(file), [file])
  const ext = useMemo(() => getFileExtension(file.name), [file.name])
  const Icon = useMemo(() => fileIcon(file), [file])
  const sizeLabel = formatAttachmentSize(file.sizeBytes)

  const [reloadNonce, setReloadNonce] = useState(0)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const [tooLarge, setTooLarge] = useState(false)
  const [textContent, setTextContent] = useState('')
  const [bufferContent, setBufferContent] = useState<ArrayBuffer | null>(null)
  const [docxHtml, setDocxHtml] = useState('')
  const [imageUrl, setImageUrl] = useState('')
  const [viewTab, setViewTab] = useState<'preview' | 'source'>('preview')
  const [copied, setCopied] = useState(false)
  const copyTimerRef = useRef<number | undefined>(undefined)

  useEffect(() => {
    setViewTab('preview')
    setCopied(false)
  }, [file.id, file.name, file.url])

  useEffect(() => () => {
    if (copyTimerRef.current !== undefined) window.clearTimeout(copyTimerRef.current)
  }, [])

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onClose])

  useEffect(() => {
    setError('')
    setTooLarge(false)
    setTextContent('')
    setBufferContent(null)
    setDocxHtml('')
    setImageUrl('')

    if (mode === 'binary') {
      setLoading(false)
      return
    }
    if ((file.sizeBytes ?? 0) > MAX_FILE_VIEWER_BYTES) {
      setTooLarge(true)
      setLoading(false)
      return
    }
    if (!remote || !file.url) {
      setError('Предпросмотр доступен при подключении к серверу.')
      setLoading(false)
      return
    }

    const controller = new AbortController()
    let objectUrl = ''
    setLoading(true)

    void remote.fetchAttachmentBlob(file.url, controller.signal).then(async blob => {
      if (controller.signal.aborted) return
      if (blob.size > MAX_FILE_VIEWER_BYTES) {
        setTooLarge(true)
        setLoading(false)
        return
      }
      if (mode === 'image') {
        objectUrl = URL.createObjectURL(blob)
        setImageUrl(objectUrl)
      } else if (mode === 'pdf') {
        const buf = await blob.arrayBuffer()
        if (!controller.signal.aborted) setBufferContent(buf)
      } else if (mode === 'docx') {
        const buf = await blob.arrayBuffer()
        const result = await mammoth.convertToHtml({ arrayBuffer: buf })
        if (!controller.signal.aborted) setDocxHtml(sanitizeDocxHtml(result.value))
      } else {
        const txt = await blob.text()
        if (!controller.signal.aborted) setTextContent(txt)
      }
      if (!controller.signal.aborted) setLoading(false)
    }).catch(err => {
      if (controller.signal.aborted) return
      setError(err instanceof Error ? err.message : 'Не удалось загрузить файл.')
      setLoading(false)
    })

    return () => {
      controller.abort()
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [file.url, file.sizeBytes, mode, remote, reloadNonce])

  const canCopy = (mode === 'code' || mode === 'markdown' || mode === 'table') && !loading && !error && !tooLarge && textContent.length > 0

  const copyContent = useCallback(async () => {
    if (!textContent) return
    try {
      await navigator.clipboard.writeText(textContent)
    } catch {
      const area = document.createElement('textarea')
      area.value = textContent
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
  }, [textContent])

  const tableRows = useMemo(() => {
    if (mode !== 'table' || !textContent) return []
    return parseDelimitedTable(textContent, ext === '.tsv' ? '\t' : ',')
  }, [mode, textContent, ext])

  return (
    <div className="fv-panel-inner">
      <header className="fv-head">
        <div className="fv-head-left">
          <button type="button" className="icon-btn fv-mobile-back" onClick={onClose} aria-label="Назад к чату">
            <ChevronLeft size={18} />
          </button>
          <span className="fv-head-icon" aria-hidden="true"><Icon size={16} /></span>
          <div className="fv-head-meta">
            <strong title={file.name}>{file.name}</strong>
            {sizeLabel && <small>{sizeLabel}</small>}
          </div>
        </div>
        <div className="fv-head-actions">
          {(mode === 'markdown' || mode === 'table') && !loading && !error && !tooLarge && (
            <div className="fv-segmented" role="tablist" aria-label="Режим отображения">
              <button
                type="button"
                role="tab"
                aria-selected={viewTab === 'preview'}
                className={viewTab === 'preview' ? 'is-active' : ''}
                onClick={() => setViewTab('preview')}
              >
                {mode === 'table' ? 'Таблица' : 'Просмотр'}
              </button>
              <button
                type="button"
                role="tab"
                aria-selected={viewTab === 'source'}
                className={viewTab === 'source' ? 'is-active' : ''}
                onClick={() => setViewTab('source')}
              >
                Исходник
              </button>
            </div>
          )}
          {canCopy && (
            <button type="button" className="icon-btn fv-action-btn" onClick={() => void copyContent()} title="Копировать содержимое" aria-label="Копировать содержимое">
              {copied ? <Check size={15} /> : <Copy size={15} />}
            </button>
          )}
          {file.url && remote && (
            <button
              type="button"
              className="icon-btn fv-action-btn"
              onClick={() => { void remote.downloadAttachment(file) }}
              title={`Скачать ${file.name}`}
              aria-label={`Скачать ${file.name}`}
            >
              <Download size={15} />
            </button>
          )}
          <button
            type="button"
            className="icon-btn fv-expand-btn"
            onClick={onToggleExpand}
            title={expanded ? 'Обычная ширина' : 'Развернуть шире'}
            aria-label={expanded ? 'Обычная ширина' : 'Развернуть шире'}
          >
            {expanded ? <Minimize2 size={15} /> : <Maximize2 size={15} />}
          </button>
          <button type="button" className="icon-btn fv-close-btn" onClick={onClose} title="Закрыть (Esc)" aria-label="Закрыть просмотрщик">
            <X size={16} />
          </button>
        </div>
      </header>

      <div className="fv-body">
        {loading ? (
          <div className="fv-loading" role="status" aria-label="Загрузка файла">
            <div className="skeleton-line is-medium" />
            <div className="skeleton-line is-long" />
            <div className="skeleton-line is-long" />
            <div className="skeleton-line is-short" />
          </div>
        ) : tooLarge ? (
          <div className="fv-state-card">
            <AlertTriangle size={26} />
            <h3>Файл слишком большой для встроенного просмотра</h3>
            <p>Файлы больше 25 МБ можно скачать и открыть локально.</p>
            {file.url && remote && (
              <button type="button" className="fv-primary-btn" onClick={() => { void remote.downloadAttachment(file) }}>
                <Download size={15} />
                <span>Скачать файл</span>
              </button>
            )}
          </div>
        ) : mode === 'binary' ? (
          <div className="fv-state-card">
            <Icon size={28} />
            <h3>{file.name}</h3>
            <p>{sizeLabel ? `Размер: ${sizeLabel}. ` : ''}Встроенный просмотр этого формата не поддерживается.</p>
            {file.warning && <p className="fv-warning-note">{file.warning}</p>}
            {file.url && remote && (
              <button type="button" className="fv-primary-btn" onClick={() => { void remote.downloadAttachment(file) }}>
                <Download size={15} />
                <span>Скачать файл</span>
              </button>
            )}
          </div>
        ) : error ? (
          <div className="fv-state-card" role="alert">
            <AlertTriangle size={26} />
            <h3>Не удалось загрузить файл</h3>
            <p>{error}</p>
            <div className="fv-state-actions">
              <button type="button" className="fv-primary-btn" onClick={() => setReloadNonce(n => n + 1)}>
                <RotateCw size={14} />
                <span>Повторить</span>
              </button>
              {file.url && remote && (
                <button type="button" className="alt-action" onClick={() => { void remote.downloadAttachment(file) }}>
                  <Download size={14} />
                  <span>Скачать</span>
                </button>
              )}
            </div>
          </div>
        ) : mode === 'pdf' && bufferContent ? (
          <PdfCanvasViewer buffer={bufferContent} />
        ) : mode === 'docx' ? (
          <DocxSheetViewer html={docxHtml} />
        ) : mode === 'image' && imageUrl ? (
          <ImageStageViewer src={imageUrl} name={file.name} />
        ) : mode === 'markdown' ? (
          viewTab === 'preview' ? (
            <div className="fv-markdown-scroll">
              <div className="fv-markdown-sheet">
                <RichText text={textContent} />
              </div>
            </div>
          ) : (
            <CodeSourceView code={textContent} language="markdown" />
          )
        ) : mode === 'table' ? (
          viewTab === 'preview' ? (
            <div className="fv-table-scroll">
              {tableRows.length === 0 ? (
                <div className="fv-state-card"><p>Таблица пуста</p></div>
              ) : (
                <table className="fv-table">
                  <thead>
                    <tr>
                      {tableRows[0]!.map((cell, i) => <th key={i}>{cell}</th>)}
                    </tr>
                  </thead>
                  <tbody>
                    {tableRows.slice(1).map((row, rIdx) => (
                      <tr key={rIdx}>
                        {row.map((cell, cIdx) => <td key={cIdx}>{cell}</td>)}
                      </tr>
                    ))}
                  </tbody>
                </table>
              )}
            </div>
          ) : (
            <CodeSourceView code={textContent} language="plaintext" />
          )
        ) : (
          <CodeSourceView code={textContent} language={getCodeLanguage(file.name)} />
        )}
      </div>
    </div>
  )
}
