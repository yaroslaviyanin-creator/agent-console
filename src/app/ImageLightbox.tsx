import { useEffect, useState } from 'react'
import { AlertTriangle, ChevronLeft, ChevronRight, Download, LoaderCircle, X, ZoomIn, ZoomOut } from 'lucide-react'
import type { MessageAttachment } from '../protocol/types'
import type { RemoteTransport } from '../transport/RemoteTransport'
import { formatAttachmentSize } from './attachments'

export function ImageLightbox({
  file,
  images,
  remote,
  onSelect,
  onClose,
}: {
  file: MessageAttachment
  images: MessageAttachment[]
  remote: RemoteTransport | null
  onSelect: (att: MessageAttachment) => void
  onClose: () => void
}) {
  const [blobUrl, setBlobUrl] = useState('')
  const [error, setError] = useState('')
  const [zoom, setZoom] = useState<'fit' | number>('fit')

  const currentIndex = images.findIndex(img => (
    file.id ? img.id === file.id : img.name === file.name && img.url === file.url
  ))
  const hasPrev = currentIndex > 0
  const hasNext = currentIndex >= 0 && currentIndex < images.length - 1

  useEffect(() => {
    setZoom('fit')
  }, [file.id, file.url, file.name])

  useEffect(() => {
    setBlobUrl('')
    setError('')
    if (!remote || !file.url) {
      setError('Предпросмотр доступен при подключении к серверу.')
      return
    }
    const controller = new AbortController()
    let objectUrl = ''
    void remote.fetchAttachmentBlob(file.url, controller.signal).then(blob => {
      if (controller.signal.aborted) return
      objectUrl = URL.createObjectURL(blob)
      setBlobUrl(objectUrl)
    }).catch(err => {
      if (!controller.signal.aborted) {
        setError(err instanceof Error ? err.message : 'Не удалось загрузить изображение.')
      }
    })
    return () => {
      controller.abort()
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [remote, file.url])

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        onClose()
      } else if (e.key === 'ArrowLeft' && hasPrev) {
        e.preventDefault()
        onSelect(images[currentIndex - 1]!)
      } else if (e.key === 'ArrowRight' && hasNext) {
        e.preventDefault()
        onSelect(images[currentIndex + 1]!)
      }
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onClose, onSelect, images, currentIndex, hasPrev, hasNext])

  const sizeLabel = formatAttachmentSize(file.sizeBytes)

  return (
    <div
      className="img-lightbox-backdrop"
      role="dialog"
      aria-modal="true"
      aria-label={file.name}
      onClick={e => {
        if (e.target === e.currentTarget) onClose()
      }}
    >
      <header className="img-lightbox-top" onClick={e => e.stopPropagation()}>
        <div className="img-lightbox-meta">
          <strong title={file.name}>{file.name}</strong>
          {sizeLabel && <small>{sizeLabel}</small>}
        </div>
        <div className="img-lightbox-actions">
          {file.url && remote && (
            <button
              type="button"
              className="icon-btn"
              onClick={() => { void remote.downloadAttachment(file) }}
              title={`Скачать ${file.name}`}
              aria-label={`Скачать ${file.name}`}
            >
              <Download size={16} />
            </button>
          )}
          <button
            type="button"
            className="icon-btn"
            onClick={onClose}
            title="Закрыть (Esc)"
            aria-label="Закрыть"
          >
            <X size={18} />
          </button>
        </div>
      </header>

      <div
        className="img-lightbox-stage"
        onClick={e => {
          if (e.target === e.currentTarget) onClose()
        }}
      >
        {hasPrev && (
          <button
            type="button"
            className="img-lightbox-nav is-prev"
            onClick={e => {
              e.stopPropagation()
              onSelect(images[currentIndex - 1]!)
            }}
            aria-label="Предыдущее изображение"
          >
            <ChevronLeft size={22} />
          </button>
        )}

        {error ? (
          <div className="fv-state-card" onClick={e => e.stopPropagation()}>
            <AlertTriangle size={24} />
            <p>{error}</p>
          </div>
        ) : !blobUrl ? (
          <div className="img-lightbox-loading" aria-label="Загрузка изображения">
            <LoaderCircle size={26} className="att-spin" />
          </div>
        ) : (
          <img
            src={blobUrl}
            alt={file.name}
            className={`img-lightbox-img${zoom === 'fit' ? ' is-fit' : ''}`}
            style={zoom === 'fit' ? undefined : { width: `${Math.round(zoom * 100)}%`, maxWidth: 'none', maxHeight: 'none' }}
            onClick={e => e.stopPropagation()}
          />
        )}

        {hasNext && (
          <button
            type="button"
            className="img-lightbox-nav is-next"
            onClick={e => {
              e.stopPropagation()
              onSelect(images[currentIndex + 1]!)
            }}
            aria-label="Следующее изображение"
          >
            <ChevronRight size={22} />
          </button>
        )}
      </div>

      {blobUrl && (
        <div className="fv-floating-bar img-lightbox-zoom" role="toolbar" aria-label="Масштаб изображения" onClick={e => e.stopPropagation()}>
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
      )}
    </div>
  )
}
