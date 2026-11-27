import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import {
  CHAT_ATTACHMENT_ACCEPT,
  formatAttachmentSize,
  inferDraftKind,
  isRenderableImageAttachment,
  isSafeRasterPreviewFile,
  MAX_CHAT_ATTACHMENTS,
  MAX_CHAT_TOTAL_ATTACHMENTS,
} from '../src/app/attachments'

test('HTML, SVG, and XML are never treated as renderable <img> attachments', () => {
  assert.equal(isRenderableImageAttachment({ kind: 'image', name: 'photo.jpg', mimeType: 'image/jpeg' }), true)
  assert.equal(isRenderableImageAttachment({ kind: 'image', name: 'diagram.png', mimeType: 'image/png' }), true)
  assert.equal(isRenderableImageAttachment({ kind: 'image', name: 'anim.webp', mimeType: 'image/webp' }), true)

  assert.equal(isRenderableImageAttachment({ kind: 'image', name: 'evil.svg', mimeType: 'image/svg+xml' }), false)
  assert.equal(isRenderableImageAttachment({ kind: 'text', name: 'evil.svg', mimeType: 'text/plain; charset=utf-8' }), false)
  assert.equal(isRenderableImageAttachment({ kind: 'text', name: 'page.html', mimeType: 'text/plain; charset=utf-8' }), false)
  assert.equal(isRenderableImageAttachment({ kind: 'image', name: 'page.html', mimeType: 'text/html' }), false)
  assert.equal(isRenderableImageAttachment({ kind: 'image', name: 'feed.xml', mimeType: 'application/xml' }), false)

  assert.equal(isSafeRasterPreviewFile({ name: 'evil.svg', type: 'image/svg+xml' }), false)
  assert.equal(isSafeRasterPreviewFile({ name: 'page.html', type: 'text/html' }), false)
  assert.equal(isSafeRasterPreviewFile({ name: 'ok.png', type: 'image/png' }), true)
})

test('AttachmentViews renders non-raster files strictly as download cards without <iframe> or SVG <img>', () => {
  const source = readFileSync(new URL('../src/app/AttachmentViews.tsx', import.meta.url), 'utf8')
  assert.match(source, /isRenderableImageAttachment/)
  assert.ok(!source.includes('<iframe'))
  assert.ok(!source.includes('dangerouslySetInnerHTML'))

  assert.ok(CHAT_ATTACHMENT_ACCEPT.includes('.html'))
  assert.ok(CHAT_ATTACHMENT_ACCEPT.includes('.svg'))
  assert.ok(CHAT_ATTACHMENT_ACCEPT.includes('.zip'))
  assert.ok(CHAT_ATTACHMENT_ACCEPT.includes('.pdf'))
  assert.ok(CHAT_ATTACHMENT_ACCEPT.includes('.docx'))
  assert.ok(CHAT_ATTACHMENT_ACCEPT.includes('.xlsx'))
  assert.ok(CHAT_ATTACHMENT_ACCEPT.includes('.pptx'))
  assert.ok(CHAT_ATTACHMENT_ACCEPT.includes('.odt'))
  assert.ok(CHAT_ATTACHMENT_ACCEPT.includes('.rtf'))
  assert.ok(CHAT_ATTACHMENT_ACCEPT.includes('.epub'))
  assert.ok(CHAT_ATTACHMENT_ACCEPT.includes('.tsv'))
  assert.equal(MAX_CHAT_ATTACHMENTS, 10)
  assert.equal(MAX_CHAT_TOTAL_ATTACHMENTS, 20)
  assert.equal(inferDraftKind({ name: 'lecture.docx', type: '' }), 'document')
  assert.equal(inferDraftKind({ name: 'deck.pptx', type: '' }), 'document')
  assert.equal(inferDraftKind({ name: 'book.epub', type: '' }), 'document')
  assert.equal(formatAttachmentSize(512), '512 Б')
  assert.equal(formatAttachmentSize(1536), '2 КБ')
})

test('chatStore clears draft attachments on send and supports attachment-only messages', () => {
  const source = readFileSync(new URL('../src/app/chatStore.ts', import.meta.url), 'utf8')
  assert.match(source, /attachments:\s*source\s*\?\s*value\.attachments\s*:\s*\[\]/)
  assert.match(source, /attachmentIds\.length\s*\?\s*\{\s*attachmentIds\s*\}\s*:\s*\{\}/)
})
