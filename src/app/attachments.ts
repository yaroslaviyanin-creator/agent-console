import type { MessageAttachment } from '../protocol/types'

export const MAX_CHAT_ATTACHMENTS = 10
export const MAX_CHAT_TOTAL_ATTACHMENTS = 20
export const MAX_ATTACHMENT_MB = 100
export const MAX_IMAGE_MB = 20

export const CHAT_ATTACHMENT_ACCEPT = [
  '.jpg', '.jpeg', '.png', '.webp', '.gif',
  '.pdf', '.zip',
  '.docx', '.xlsx', '.pptx', '.odt', '.rtf', '.epub',
  '.txt', '.md', '.csv', '.tsv', '.json', '.yaml', '.yml', '.toml', '.ini', '.conf', '.log',
  '.html', '.htm', '.css', '.xml', '.svg',
  '.js', '.jsx', '.ts', '.tsx', '.py', '.go', '.c', '.cpp', '.cc', '.h', '.hpp',
  '.rs', '.java', '.sql', '.sh', '.cmake', '.dockerfile',
  'image/jpeg', 'image/png', 'image/webp', 'image/gif',
  'application/pdf', 'application/zip',
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet',
  'application/vnd.openxmlformats-officedocument.presentationml.presentation',
  'application/vnd.oasis.opendocument.text',
  'application/rtf', 'text/rtf',
  'application/epub+zip',
  'text/tab-separated-values',
].join(',')

const SAFE_RASTER_MIMES = new Set(['image/jpeg', 'image/png', 'image/webp', 'image/gif'])
const NON_IMAGE_EXTENSIONS = new Set(['.svg', '.html', '.htm', '.xml'])
const DOCUMENT_EXTENSIONS = new Set(['.docx', '.xlsx', '.pptx', '.odt', '.rtf', '.epub'])

export interface DraftAttachment {
  localId: string
  name: string
  sizeBytes: number
  kind: 'image' | 'text' | 'pdf' | 'archive' | 'document'
  status: 'uploading' | 'ready' | 'error'
  progress?: number
  previewUrl?: string
  remote?: MessageAttachment
  warning?: string
  error?: string
}

/**
 * Returns true only for raster image attachments so .svg/.html/.xml are never rendered via <img> or <iframe>.
 */
export function isRenderableImageAttachment(att: Pick<MessageAttachment, 'kind' | 'name' | 'mimeType' | 'url'>): boolean {
  if (att.kind !== 'image') return false
  const lower = att.name.trim().toLowerCase()
  const dot = lower.lastIndexOf('.')
  const ext = dot >= 0 ? lower.slice(dot) : ''
  if (NON_IMAGE_EXTENSIONS.has(ext)) return false
  if (att.mimeType) {
    const baseMime = (att.mimeType.split(';', 1)[0] ?? '').trim().toLowerCase()
    if (!SAFE_RASTER_MIMES.has(baseMime)) return false
  }
  return true
}

/**
 * Determines whether a client-side File may use a temporary blob: preview URL before upload completes.
 */
export function isSafeRasterPreviewFile(file: Pick<File, 'name' | 'type'>): boolean {
  const lower = file.name.trim().toLowerCase()
  const dot = lower.lastIndexOf('.')
  const ext = dot >= 0 ? lower.slice(dot) : ''
  if (NON_IMAGE_EXTENSIONS.has(ext)) return false
  return SAFE_RASTER_MIMES.has(file.type.toLowerCase())
}

export function inferDraftKind(file: Pick<File, 'name' | 'type'>): DraftAttachment['kind'] {
  if (isSafeRasterPreviewFile(file)) return 'image'
  const lower = file.name.trim().toLowerCase()
  const dot = lower.lastIndexOf('.')
  const ext = dot >= 0 ? lower.slice(dot) : ''
  if (lower.endsWith('.pdf') || file.type === 'application/pdf') return 'pdf'
  if (lower.endsWith('.zip') || file.type === 'application/zip' || file.type === 'application/x-zip-compressed') return 'archive'
  if (DOCUMENT_EXTENSIONS.has(ext)) return 'document'
  return 'text'
}

export function formatAttachmentSize(bytes?: number): string {
  if (bytes == null || !Number.isFinite(bytes) || bytes < 0) return ''
  if (bytes < 1024) return `${bytes} Б`
  if (bytes < 1024 * 1024) return `${Math.max(1, Math.round(bytes / 1024))} КБ`
  return `${(bytes / (1024 * 1024)).toFixed(1)} МБ`
}
