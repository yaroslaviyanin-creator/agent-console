import { useEffect, useState } from 'react'
import { AlertTriangle, Download, FileArchive, FileCode, FileSpreadsheet, FileText, Image as ImageIcon, LoaderCircle, Paperclip, X } from 'lucide-react'
import type { MessageAttachment } from '../protocol/types'
import type { RemoteTransport } from '../transport/RemoteTransport'
import { type DraftAttachment, formatAttachmentSize, isRenderableImageAttachment } from './attachments'
import { useFileViewer } from './fileViewerStore'

function attachmentFileIcon(att: Pick<MessageAttachment, 'kind' | 'name'>) {
  const lower = att.name.trim().toLowerCase()
  if (att.kind === 'archive' || lower.endsWith('.zip')) return FileArchive
  if (lower.endsWith('.xlsx') || lower.endsWith('.csv') || lower.endsWith('.tsv')) return FileSpreadsheet
  if (att.kind === 'pdf' || att.kind === 'document' || lower.endsWith('.pdf')) return FileText
  if (att.kind === 'text') return FileCode
  return Paperclip
}

function attachmentExtensionBadge(name: string): string {
  const dot = name.lastIndexOf('.')
  if (dot < 0 || dot === name.length - 1) return 'FILE'
  return name.slice(dot + 1).toUpperCase().slice(0, 6)
}

function AuthenticatedImage({
  att,
  remote,
  onPreview,
}: {
  att: MessageAttachment
  remote: RemoteTransport | null
  onPreview: (att: MessageAttachment) => void
}) {
  const [blobUrl, setBlobUrl] = useState<string>('')
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    if (!remote || !att.url) return
    const controller = new AbortController()
    let objectUrl = ''
    void remote.fetchAttachmentBlobUrl(att.url, controller.signal).then(url => {
      objectUrl = url
      setBlobUrl(url)
    }).catch(() => {
      if (!controller.signal.aborted) setFailed(true)
    })
    return () => {
      controller.abort()
      if (objectUrl) URL.revokeObjectURL(objectUrl)
    }
  }, [remote, att.url])

  if (failed || !att.url || !remote) {
    return (
      <span className="att-image-fallback" title={att.name}>
        <ImageIcon size={14} aria-hidden="true" />
        <span>{att.name}</span>
      </span>
    )
  }
  if (!blobUrl) {
    return (
      <span className="att-image-loading" aria-label={`Загрузка ${att.name}`}>
        <LoaderCircle size={15} className="att-spin" aria-hidden="true" />
      </span>
    )
  }
  return (
    <button
      type="button"
      className="att-image-thumb-btn"
      onClick={() => onPreview(att)}
      title={`Открыть ${att.name}`}
    >
      <img src={blobUrl} alt={att.name} className="att-image-thumb" />
    </button>
  )
}

export function TurnAttachments({
  attachments,
  remote,
}: {
  attachments: MessageAttachment[]
  remote: RemoteTransport | null
}) {
  const openFile = useFileViewer(s => s.openFile)
  const activeFile = useFileViewer(s => s.file)

  const images = attachments.filter(att => isRenderableImageAttachment(att) && !!att.url)
  const files = attachments.filter(att => !(isRenderableImageAttachment(att) && !!att.url))

  return (
    <>
      {images.length > 0 && (
        <div className="message-image-grid" aria-label="Прикреплённые изображения">
          {images.map((att, index) => (
            <AuthenticatedImage
              key={att.id ?? `${att.name}-${index}`}
              att={att}
              remote={remote}
              onPreview={openFile}
            />
          ))}
        </div>
      )}
      {files.length > 0 && (
        <ul className="message-attachments" aria-label="Вложения">
          {files.map((att, index) => {
            const Icon = att.kind === 'image' ? ImageIcon : attachmentFileIcon(att)
            const sizeLabel = formatAttachmentSize(att.sizeBytes)
            const isSelected = !!activeFile && (activeFile.id ? activeFile.id === att.id : activeFile.name === att.name && activeFile.url === att.url)
            return (
              <li
                key={att.id ?? `${att.name}-${index}`}
                className={`is-clickable${isSelected ? ' is-selected' : ''}`}
                role="button"
                tabIndex={0}
                title={att.warning ? `${att.name}: ${att.warning}` : `Открыть ${att.name}`}
                onClick={() => openFile(att)}
                onKeyDown={e => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault()
                    openFile(att)
                  }
                }}
              >
                <Icon size={14} aria-hidden="true" />
                <span className="att-file-name">{att.name}</span>
                {sizeLabel && <small className="att-file-size">{sizeLabel}</small>}
                {att.warning && (
                  <span className="att-warning-badge" title={att.warning} aria-label={att.warning}>
                    <AlertTriangle size={12} aria-hidden="true" />
                  </span>
                )}
                {att.url && remote && (
                  <button
                    type="button"
                    className="att-download-btn"
                    aria-label={`Скачать ${att.name}`}
                    title={`Скачать ${att.name}`}
                    onClick={e => {
                      e.stopPropagation()
                      void remote.downloadAttachment(att)
                    }}
                  >
                    <Download size={13} aria-hidden="true" />
                  </button>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </>
  )
}

export function AssistantArtifacts({
  attachments,
  remote,
}: {
  attachments: MessageAttachment[]
  remote: RemoteTransport | null
}) {
  const openFile = useFileViewer(s => s.openFile)
  const activeFile = useFileViewer(s => s.file)

  if (attachments.length === 0) return null

  return (
    <div className="assistant-artifacts" aria-label="Созданные файлы">
      {attachments.map((att, index) => {
        const Icon = isRenderableImageAttachment(att) ? ImageIcon : attachmentFileIcon(att)
        const ext = attachmentExtensionBadge(att.name)
        const sizeLabel = formatAttachmentSize(att.sizeBytes)
        const isSelected = !!activeFile && (activeFile.id ? activeFile.id === att.id : activeFile.name === att.name && activeFile.url === att.url)
        return (
          <div
            key={att.id ?? `${att.name}-${index}`}
            className={`artifact-card${isSelected ? ' is-selected' : ''}`}
            role="button"
            tabIndex={0}
            title={att.warning ? `${att.name}: ${att.warning}` : `Открыть ${att.name}`}
            onClick={() => openFile(att)}
            onKeyDown={e => {
              if (e.key === 'Enter' || e.key === ' ') {
                e.preventDefault()
                openFile(att)
              }
            }}
          >
            <span className="artifact-card-icon" aria-hidden="true">
              <Icon size={17} />
            </span>
            <div className="artifact-card-body">
              <div className="artifact-card-title-row">
                <strong className="artifact-card-name">{att.name}</strong>
                {att.warning && (
                  <span className="att-warning-badge" title={att.warning} aria-label={att.warning}>
                    <AlertTriangle size={12} aria-hidden="true" />
                  </span>
                )}
              </div>
              <div className="artifact-card-meta">
                <span className="artifact-ext-badge">{ext}</span>
                {sizeLabel && <span className="artifact-size">{sizeLabel}</span>}
              </div>
            </div>
            <div className="artifact-card-actions">
              <span className="artifact-open-label">Открыть</span>
              {att.url && remote && (
                <button
                  type="button"
                  className="artifact-download-btn"
                  aria-label={`Скачать ${att.name}`}
                  title={`Скачать ${att.name}`}
                  onClick={e => {
                    e.stopPropagation()
                    void remote.downloadAttachment(att)
                  }}
                >
                  <Download size={14} aria-hidden="true" />
                </button>
              )}
            </div>
          </div>
        )
      })}
    </div>
  )
}

export function DraftAttachmentsBar({
  attachments,
  error,
  disabled,
  onRemove,
  onDismissError,
}: {
  attachments: DraftAttachment[]
  error?: string | undefined
  disabled: boolean
  onRemove: (localId: string) => void
  onDismissError: () => void
}) {
  const warnings = attachments.filter(a => a.status === 'ready' && !!a.warning)
  if (attachments.length === 0 && !error) return null
  return (
    <div className="composer-attachments-wrap">
      {error && (
        <div className="composer-attachment-error" role="alert">
          <span>{error}</span>
          <button type="button" className="icon-btn" aria-label="Скрыть ошибку" onClick={onDismissError}>
            <X size={13} />
          </button>
        </div>
      )}
      {warnings.length > 0 && (
        <div className="composer-attachment-warning" role="status">
          <AlertTriangle size={13} aria-hidden="true" />
          <span>{warnings.map(w => `${w.name}: ${w.warning}`).join(' · ')}</span>
        </div>
      )}
      {attachments.length > 0 && (
        <ul className="composer-attachments" aria-label="Прикреплённые файлы">
          {attachments.map(att => {
            const Icon = attachmentFileIcon(att)
            const sizeLabel = formatAttachmentSize(att.sizeBytes)
            const progressLabel = att.status === 'uploading'
              ? (att.progress != null && att.progress > 0 && att.progress < 100
                  ? `Загрузка ${att.progress}%…`
                  : att.progress === 100
                    ? 'Обработка…'
                    : 'Загрузка…')
              : sizeLabel
            return (
              <li key={att.localId} className={`composer-att-chip is-${att.status}`} title={att.warning ?? att.name}>
                {att.kind === 'image' && att.previewUrl ? (
                  <img src={att.previewUrl} alt={att.name} className="composer-att-thumb" />
                ) : (
                  <span className="composer-att-icon">
                    {att.status === 'uploading'
                      ? <LoaderCircle size={14} className="att-spin" aria-hidden="true" />
                      : <Icon size={14} aria-hidden="true" />}
                  </span>
                )}
                <span className="composer-att-meta">
                  <strong title={att.name}>{att.name}</strong>
                  <small>{progressLabel}</small>
                  {att.status === 'uploading' && (
                    <span className="composer-att-progress" aria-hidden="true">
                      <span style={{ width: `${att.progress ?? 0}%` }} />
                    </span>
                  )}
                </span>
                {att.warning && (
                  <span className="att-warning-badge" title={att.warning} aria-label={att.warning}>
                    <AlertTriangle size={12} aria-hidden="true" />
                  </span>
                )}
                <button
                  type="button"
                  className="composer-att-remove"
                  disabled={disabled}
                  aria-label={`Убрать ${att.name}`}
                  onClick={() => onRemove(att.localId)}
                >
                  <X size={13} />
                </button>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}
