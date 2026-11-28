import DOMPurify from 'dompurify'
import type { MessageAttachment } from '../protocol/types'
import { isRenderableImageAttachment } from './attachments'

export const MAX_FILE_VIEWER_BYTES = 25 * 1024 * 1024

export type FileViewerMode = 'pdf' | 'docx' | 'markdown' | 'table' | 'image' | 'code' | 'binary'

const MARKDOWN_EXTENSIONS = new Set(['.md', '.markdown'])
const TABLE_EXTENSIONS = new Set(['.csv', '.tsv'])
const STRICT_SOURCE_EXTENSIONS = new Set(['.html', '.htm', '.svg', '.xml'])
const CODE_EXTENSIONS = new Set([
  '.txt', '.py', '.cpp', '.cc', '.c', '.h', '.hpp', '.go', '.ts', '.tsx', '.js', '.jsx',
  '.json', '.yaml', '.yml', '.toml', '.ini', '.conf', '.sh', '.sql', '.css',
  '.html', '.htm', '.svg', '.xml', '.log', '.rs', '.java', '.cmake', '.dockerfile',
])

export function getFileExtension(name: string): string {
  const trimmed = name.trim().toLowerCase()
  const dot = trimmed.lastIndexOf('.')
  return dot >= 0 ? trimmed.slice(dot) : ''
}

/**
 * Determines how an attachment should be rendered inside the File Viewer.
 * Security invariant: .html, .htm, .svg, and .xml ALWAYS resolve to 'code' (plain highlighted source).
 */
export function getFileViewerMode(att: Pick<MessageAttachment, 'kind' | 'name' | 'mimeType' | 'url'>): FileViewerMode {
  const ext = getFileExtension(att.name)
  const mime = (att.mimeType?.split(';', 1)[0] ?? '').trim().toLowerCase()

  if (STRICT_SOURCE_EXTENSIONS.has(ext) || mime === 'text/html' || mime === 'image/svg+xml' || mime === 'application/xml' || mime === 'text/xml') {
    return 'code'
  }
  if (isRenderableImageAttachment(att)) {
    return 'image'
  }
  if (ext === '.pdf' || att.kind === 'pdf' || mime === 'application/pdf') {
    return 'pdf'
  }
  if (ext === '.docx' || mime === 'application/vnd.openxmlformats-officedocument.wordprocessingml.document') {
    return 'docx'
  }
  if (MARKDOWN_EXTENSIONS.has(ext) || mime === 'text/markdown') {
    return 'markdown'
  }
  if (TABLE_EXTENSIONS.has(ext) || mime === 'text/csv' || mime === 'text/tab-separated-values') {
    return 'table'
  }
  if (CODE_EXTENSIONS.has(ext) || att.kind === 'text' || mime.startsWith('text/')) {
    return 'code'
  }
  return 'binary'
}

export function getCodeLanguage(name: string): string {
  const ext = getFileExtension(name)
  switch (ext) {
    case '.ts':
    case '.tsx':
      return 'typescript'
    case '.js':
    case '.jsx':
      return 'javascript'
    case '.json':
      return 'json'
    case '.py':
      return 'python'
    case '.sh':
      return 'bash'
    case '.html':
    case '.htm':
    case '.svg':
    case '.xml':
      return 'xml'
    case '.css':
      return 'css'
    case '.go':
      return 'go'
    case '.c':
    case '.cc':
    case '.cpp':
    case '.h':
    case '.hpp':
      return 'cpp'
    case '.sql':
      return 'sql'
    case '.yaml':
    case '.yml':
      return 'yaml'
    case '.md':
    case '.markdown':
      return 'markdown'
    default:
      return 'plaintext'
  }
}

export function parseDelimitedTable(raw: string, delimiter: ',' | '\t'): string[][] {
  const text = raw.replace(/^\uFEFF/, '')
  const rows: string[][] = []
  let row: string[] = []
  let cell = ''
  let inQuotes = false

  for (let i = 0; i < text.length; i++) {
    const ch = text[i]!
    if (inQuotes) {
      if (ch === '"') {
        if (text[i + 1] === '"') {
          cell += '"'
          i++
        } else {
          inQuotes = false
        }
      } else {
        cell += ch
      }
      continue
    }
    if (ch === '"') {
      inQuotes = true
    } else if (ch === delimiter) {
      row.push(cell)
      cell = ''
    } else if (ch === '\r') {
      if (text[i + 1] === '\n') i++
      row.push(cell)
      rows.push(row)
      row = []
      cell = ''
    } else if (ch === '\n') {
      row.push(cell)
      rows.push(row)
      row = []
      cell = ''
    } else {
      cell += ch
    }
  }
  if (cell.length > 0 || row.length > 0) {
    row.push(cell)
    rows.push(row)
  }
  return rows.filter(r => r.some(c => c.length > 0))
}

export interface PurifyLike {
  sanitize: (dirty: string, config?: Record<string, unknown>) => string
}

/**
 * Sanitizes HTML produced by mammoth for .docx files, stripping dangerous tags/attributes
 * and external http(s) image/link URLs where applicable.
 */
export function sanitizeDocxHtml(html: string, purify: PurifyLike = DOMPurify as unknown as PurifyLike): string {
  const cleaned = purify.sanitize(html, {
    FORBID_TAGS: ['script', 'iframe', 'object', 'embed', 'form', 'style'],
    FORBID_ATTR: ['onerror', 'onload', 'style'],
  })
  // Block external http(s) image loads inside embedded Word documents.
  return cleaned.replace(
    /(<img\b[^>]*?\bsrc\s*=\s*["']?)https?:\/\/[^"'\s>]+(["']?[^>]*>)/gi,
    '$1data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///ywAAAAAAQABAAACAUwAOw==$2',
  )
}
