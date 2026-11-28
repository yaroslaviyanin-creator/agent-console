import { useEffect, useRef, useState } from 'react'
import { FileText, Paperclip, Trash2, Upload, X } from 'lucide-react'
import type { ChatFolder } from '../protocol/types'
import { CHAT_ATTACHMENT_ACCEPT, formatAttachmentSize } from './attachments'
import { loadFolderAttachments, removeFolderFile, updateChatFolder, uploadFolderFiles, useChats } from './chatStore'
import { useFileViewer } from './fileViewerStore'

const PROJECT_COLORS = ['#6366f1', '#10b981', '#0ea5e9', '#f59e0b', '#ec4899', '#8b5cf6', '#64748b'] as const

export function ProjectSettingsModal({
  folder,
  onClose,
}: {
  folder: ChatFolder
  onClose: () => void
}) {
  const remote = useChats(s => s.remote)
  const files = useChats(s => s.folderAttachmentsById[folder.id])
  const openFile = useFileViewer(s => s.openFile)

  const [name, setName] = useState(folder.name)
  const [color, setColor] = useState(folder.color)
  const [systemPrompt, setSystemPrompt] = useState(folder.systemPrompt ?? '')
  const [saving, setSaving] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [error, setError] = useState('')
  const fileInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    setName(folder.name)
    setColor(folder.color)
    setSystemPrompt(folder.systemPrompt ?? '')
  }, [folder.id, folder.name, folder.color, folder.systemPrompt])

  useEffect(() => {
    if (!remote) return
    void loadFolderAttachments(folder.id).catch(() => {})
  }, [folder.id, remote])

  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => window.removeEventListener('keydown', onKeyDown)
  }, [onClose])

  async function save(e: React.FormEvent) {
    e.preventDefault()
    const trimmed = name.trim()
    if (!trimmed || trimmed.length > 60) {
      setError('Название проекта должно быть от 1 до 60 символов.')
      return
    }
    if (new TextEncoder().encode(systemPrompt).length > 8192) {
      setError('Системные инструкции не должны превышать 8 КБ.')
      return
    }
    setSaving(true)
    setError('')
    try {
      await updateChatFolder(folder.id, { name: trimmed, color, systemPrompt })
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Не удалось сохранить настройки проекта.')
    } finally {
      setSaving(false)
    }
  }

  async function handleUpload(list: FileList | null) {
    if (!list || list.length === 0) return
    setUploading(true)
    setError('')
    try {
      await uploadFolderFiles(folder.id, Array.from(list))
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Не удалось загрузить файл.')
    } finally {
      setUploading(false)
    }
  }

  return (
    <div className="modal-backdrop" role="presentation" onClick={e => { if (e.target === e.currentTarget) onClose() }}>
      <div className="modal-card project-modal" role="dialog" aria-modal="true" aria-labelledby="project-modal-title">
        <div className="modal-head">
          <h2 id="project-modal-title">Настройки проекта</h2>
          <button type="button" className="icon-btn" aria-label="Закрыть настройки проекта" onClick={onClose}>
            <X size={16} />
          </button>
        </div>

        <form className="project-modal-form" onSubmit={e => void save(e)}>
          <div className="field-group">
            <label htmlFor="project-name-input">Название проекта</label>
            <input
              id="project-name-input"
              type="text"
              value={name}
              maxLength={60}
              disabled={saving}
              onChange={e => setName(e.target.value)}
            />
          </div>

          <div className="field-group">
            <span>Цвет папки</span>
            <div className="folder-color-row" role="group" aria-label="Цвет проекта">
              {PROJECT_COLORS.map(c => (
                <button
                  key={c}
                  type="button"
                  aria-label={`Выбрать цвет ${c}`}
                  className={`folder-color-swatch${color.toLowerCase() === c ? ' is-active' : ''}`}
                  style={{ backgroundColor: c }}
                  onClick={() => setColor(c)}
                />
              ))}
            </div>
          </div>

          <div className="field-group">
            <label htmlFor="project-prompt-input">Системные инструкции проекта</label>
            <small className="form-help">Применяются автоматически ко всем вашим личным чатам внутри этой папки.</small>
            <textarea
              id="project-prompt-input"
              rows={5}
              value={systemPrompt}
              disabled={saving}
              placeholder="Например: Отвечай строго в стиле техдокументации, учитывай стек Go + React..."
              onChange={e => setSystemPrompt(e.target.value)}
            />
          </div>

          {remote && (
            <div className="field-group">
              <div className="project-files-head">
                <span>Файлы проекта ({(files ?? []).length})</span>
                <input
                  ref={fileInputRef}
                  type="file"
                  multiple
                  accept={CHAT_ATTACHMENT_ACCEPT}
                  className="sr-only"
                  onChange={e => {
                    void handleUpload(e.target.files)
                    e.target.value = ''
                  }}
                />
                <button
                  type="button"
                  className="alt-action"
                  disabled={uploading || saving}
                  onClick={() => fileInputRef.current?.click()}
                >
                  <Upload size={13} />
                  <span>{uploading ? 'Загрузка…' : 'Добавить файл'}</span>
                </button>
              </div>
              <small className="form-help">Содержимое этих файлов автоматически подключается в контекст чатов проекта.</small>
              {(files ?? []).length > 0 ? (
                <ul className="project-files-list">
                  {(files ?? []).map(att => (
                    <li key={att.id ?? att.name} className="project-file-row">
                      <button
                        type="button"
                        className="project-file-open"
                        onClick={() => openFile(att)}
                        title={`Открыть ${att.name}`}
                      >
                        <FileText size={14} aria-hidden="true" />
                        <span className="project-file-name">{att.name}</span>
                        {att.sizeBytes != null && <small>{formatAttachmentSize(att.sizeBytes)}</small>}
                      </button>
                      {att.id && (
                        <button
                          type="button"
                          className="icon-btn"
                          aria-label={`Удалить ${att.name}`}
                          title="Удалить из проекта"
                          onClick={() => void removeFolderFile(folder.id, att.id!)}
                        >
                          <Trash2 size={13} />
                        </button>
                      )}
                    </li>
                  ))}
                </ul>
              ) : (
                <div className="project-files-empty">
                  <Paperclip size={13} aria-hidden="true" />
                  <span>Нет закреплённых файлов</span>
                </div>
              )}
            </div>
          )}

          {error && <p className="error-text" role="alert">{error}</p>}

          <div className="form-actions">
            <button type="button" className="alt-action" onClick={onClose} disabled={saving}>Отмена</button>
            <button type="submit" className="primary-action" disabled={saving}>{saving ? 'Сохранение…' : 'Сохранить'}</button>
          </div>
        </form>
      </div>
    </div>
  )
}
