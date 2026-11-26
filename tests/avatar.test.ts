import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { sanitizeAvatarDataUrl } from '../src/app/avatar'

test('sanitizeAvatarDataUrl allows only base64 jpeg, png, and webp data URLs', () => {
  const jpeg = 'data:image/jpeg;base64,/9j/4AAQSkZJRg=='
  const png = 'data:image/png;base64,iVBORw0KGgoAAAANSUhEUg=='
  const webp = 'data:image/webp;base64,UklGRh4AAABXRUJQVlA4'

  assert.equal(sanitizeAvatarDataUrl(jpeg), jpeg)
  assert.equal(sanitizeAvatarDataUrl(png), png)
  assert.equal(sanitizeAvatarDataUrl(webp), webp)
})

test('sanitizeAvatarDataUrl rejects svg, javascript:, html, and malformed strings', () => {
  const rejected = [
    '',
    null,
    undefined,
    'data:image/svg+xml;base64,PHN2ZyBvbmxvYWQ9ImFsZXJ0KDEpIj48L3N2Zz4=',
    'data:image/svg+xml,<svg onload="alert(1)"/>',
    'javascript:alert(1)',
    'data:text/html;base64,PHNjcmlwdD5hbGVydCgxKTwvc2NyaXB0Pg==',
    'https://evil.example/avatar.png',
    'data:image/jpeg;base64,/9j/4AAQ" onerror="alert(1)',
    'data:image/png;base64, iVBORw0KGgo=',
    'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7',
  ]

  for (const input of rejected) {
    assert.equal(sanitizeAvatarDataUrl(input), undefined, `expected rejection for ${String(input)}`)
  }
})

test('UserAvatar renders avatarData exclusively via sanitized <img src>', () => {
  const source = readFileSync(new URL('../src/app/UserAvatar.tsx', import.meta.url), 'utf8')
  assert.match(source, /sanitizeAvatarDataUrl/)
  assert.match(source, /<img\s+src=\{safeSrc\}/)
  assert.ok(!source.includes('dangerouslySetInnerHTML'))
  assert.ok(!source.includes('<object'))
})
