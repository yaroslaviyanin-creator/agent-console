import test from 'node:test'
import assert from 'node:assert/strict'
import { getFileViewerMode, parseDelimitedTable, sanitizeDocxHtml } from '../src/app/fileViewerUtils'
import { useFileViewer } from '../src/app/fileViewerStore'

test('getFileViewerMode maps extensions/MIME and forces .html/.htm/.svg/.xml strictly to code', () => {
  assert.equal(getFileViewerMode({ name: 'report.pdf', kind: 'pdf' }), 'pdf')
  assert.equal(getFileViewerMode({ name: 'contract.docx', kind: 'document' }), 'docx')
  assert.equal(getFileViewerMode({ name: 'notes.md', kind: 'text' }), 'markdown')
  assert.equal(getFileViewerMode({ name: 'readme.markdown', kind: 'text' }), 'markdown')
  assert.equal(getFileViewerMode({ name: 'data.csv', kind: 'text' }), 'table')
  assert.equal(getFileViewerMode({ name: 'data.tsv', kind: 'text' }), 'table')
  assert.equal(getFileViewerMode({ name: 'photo.jpg', kind: 'image', mimeType: 'image/jpeg' }), 'image')
  assert.equal(getFileViewerMode({ name: 'screen.png', kind: 'image', mimeType: 'image/png' }), 'image')
  assert.equal(getFileViewerMode({ name: 'archive.zip', kind: 'archive' }), 'binary')
  assert.equal(getFileViewerMode({ name: 'sheet.xlsx', kind: 'document' }), 'xlsx')

  // Security invariant: HTML/SVG/XML must NEVER resolve to image or HTML rendering.
  assert.equal(getFileViewerMode({ name: 'payload.html', kind: 'text', mimeType: 'text/html' }), 'code')
  assert.equal(getFileViewerMode({ name: 'payload.htm', kind: 'image', mimeType: 'text/html' }), 'code')
  assert.equal(getFileViewerMode({ name: 'vector.svg', kind: 'image', mimeType: 'image/svg+xml' }), 'code')
  assert.equal(getFileViewerMode({ name: 'feed.xml', kind: 'image', mimeType: 'application/xml' }), 'code')
})

test('useFileViewer opens raster images in lightbox and documents/svg in Split-View', () => {
  useFileViewer.getState().closeFile()
  useFileViewer.getState().closeLightbox()
  assert.equal(useFileViewer.getState().file, null)
  assert.equal(useFileViewer.getState().lightboxFile, null)

  const photo = { id: 'img1', name: 'photo.jpg', kind: 'image' as const, mimeType: 'image/jpeg', url: '/api/attachments/img1' }
  const png = { id: 'img2', name: 'shot.png', kind: 'image' as const, mimeType: 'image/png', url: '/api/attachments/img2' }
  const svg = { id: 'svg1', name: 'diagram.svg', kind: 'image' as const, mimeType: 'image/svg+xml', url: '/api/attachments/svg1' }
  const pdf = { id: 'b2', name: 'spec.pdf', kind: 'pdf' as const, url: '/api/attachments/b2' }
  const md = { id: 'm1', name: 'notes.md', kind: 'text' as const, url: '/api/attachments/m1' }

  // Raster images open in lightbox without touching Split-View
  useFileViewer.getState().openFile(photo)
  assert.deepEqual(useFileViewer.getState().lightboxFile, photo)
  assert.equal(useFileViewer.getState().file, null)

  useFileViewer.getState().openFile(png)
  assert.deepEqual(useFileViewer.getState().lightboxFile, png)
  useFileViewer.getState().closeLightbox()
  assert.equal(useFileViewer.getState().lightboxFile, null)

  // Documents and .svg open in Split-View, never in lightbox
  useFileViewer.getState().openFile(pdf)
  assert.deepEqual(useFileViewer.getState().file, pdf)
  assert.equal(useFileViewer.getState().lightboxFile, null)

  useFileViewer.getState().openFile(md)
  assert.deepEqual(useFileViewer.getState().file, md)

  useFileViewer.getState().openFile(svg)
  assert.deepEqual(useFileViewer.getState().file, svg)
  assert.equal(useFileViewer.getState().lightboxFile, null)

  useFileViewer.getState().closeFile()
  assert.equal(useFileViewer.getState().file, null)
  assert.equal(useFileViewer.getState().expanded, false)
})

test('sanitizeDocxHtml strips script tags, event handlers, and blocks external http(s) image loads', () => {
  const dirty = '<p onclick="alert(1)">Hello</p><script>alert(1)</script><img src="https://evil.example/track.png" onerror="alert(2)"><iframe src="https://evil.example"></iframe>'
  const fakePurify = {
    sanitize(html: string) {
      return html
        .replace(/<script[\s\S]*?<\/script>/gi, '')
        .replace(/<iframe[\s\S]*?<\/iframe>/gi, '')
        .replace(/\s+on[a-z]+="[^"]*"/gi, '')
    },
  }
  const clean = sanitizeDocxHtml(dirty, fakePurify)
  assert.equal(clean.includes('<script'), false)
  assert.equal(clean.includes('<iframe'), false)
  assert.equal(clean.includes('onerror'), false)
  assert.equal(clean.includes('onclick'), false)
  assert.equal(clean.includes('https://evil.example/track.png'), false)
  assert.match(clean, /<p>Hello<\/p>/)
})

test('parseDelimitedTable parses quoted CSV and TSV cleanly', () => {
  const csv = parseDelimitedTable('name,note\n"Alice, A.","line ""1"""\nBob,ok\n', ',')
  assert.deepEqual(csv, [
    ['name', 'note'],
    ['Alice, A.', 'line "1"'],
    ['Bob', 'ok'],
  ])
  const tsv = parseDelimitedTable('a\tb\n1\t2', '\t')
  assert.deepEqual(tsv, [['a', 'b'], ['1', '2']])
})
