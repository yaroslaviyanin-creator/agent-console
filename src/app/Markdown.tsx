import { isValidElement, useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import remarkMath from 'remark-math'
import rehypeKatex from 'rehype-katex'
import hljs from 'highlight.js/lib/core'
import typescript from 'highlight.js/lib/languages/typescript'
import json from 'highlight.js/lib/languages/json'
import bash from 'highlight.js/lib/languages/bash'
import python from 'highlight.js/lib/languages/python'
import javascript from 'highlight.js/lib/languages/javascript'
import { Check, Copy } from 'lucide-react'
import { MermaidBlock } from './MermaidBlock'

for (const [name, language] of Object.entries({ typescript, json, bash, python, javascript })) hljs.registerLanguage(name, language)

function extractCodeInfo(children: ReactNode): { lang: string; raw: string } {
  if (isValidElement<{ className?: string; children?: ReactNode }>(children)) {
    const lang = children.props.className?.replace(/^language-/, '').trim() || ''
    const raw = typeof children.props.children === 'string'
      ? children.props.children
      : Array.isArray(children.props.children)
        ? children.props.children.map(c => (typeof c === 'string' ? c : '')).join('')
        : ''
    return { lang, raw: raw.replace(/\n$/, '') }
  }
  return { lang: '', raw: typeof children === 'string' ? children.replace(/\n$/, '') : '' }
}

function CodeBlock({ children, streaming = false }: { children?: ReactNode; streaming?: boolean }) {
  const { lang, raw } = extractCodeInfo(children)
  const [copied, setCopied] = useState(false)
  const timerRef = useRef<number | undefined>(undefined)

  useEffect(() => () => {
    if (timerRef.current !== undefined) window.clearTimeout(timerRef.current)
  }, [])

  if (lang.toLowerCase() === 'mermaid') {
    return <MermaidBlock code={raw} streaming={streaming} />
  }

  async function copyCode() {
    if (!raw) return
    try {
      await navigator.clipboard.writeText(raw)
    } catch {
      const area = document.createElement('textarea')
      area.value = raw
      area.style.position = 'fixed'
      area.style.opacity = '0'
      document.body.appendChild(area)
      area.select()
      document.execCommand('copy')
      document.body.removeChild(area)
    }
    setCopied(true)
    if (timerRef.current !== undefined) window.clearTimeout(timerRef.current)
    timerRef.current = window.setTimeout(() => setCopied(false), 1800)
  }

  return (
    <div className="code-block">
      <div className="code-block-head">
        <span className="code-block-lang">{lang || 'text'}</span>
        <button type="button" className="code-copy-btn" onClick={() => void copyCode()} aria-label="Копировать код">
          {copied ? <><Check size={12} /><span>Скопировано</span></> : <><Copy size={12} /><span>Копировать</span></>}
        </button>
      </div>
      <pre>{children}</pre>
    </div>
  )
}

function findClosingFenceEnd(src: string, afterOpenLine: number, fenceChar: string, fenceLen: number): number {
  let pos = afterOpenLine
  while (pos < src.length) {
    const nl = src.indexOf('\n', pos)
    const lineEnd = nl === -1 ? src.length : nl
    const line = src.slice(pos, lineEnd)
    const match = line.match(/^[ ]{0,3}(`{3,}|~{3,})[ \t]*$/)
    if (match && match[1]![0] === fenceChar && match[1]!.length >= fenceLen) {
      return nl === -1 ? src.length : nl + 1
    }
    if (nl === -1) break
    pos = nl + 1
  }
  return src.length
}

function findClosingInlineMath(src: string, start: number): number {
  let i = start
  while (i < src.length) {
    if (src[i] === '\\') {
      if (src[i + 1] === ')') return i
      if (src[i + 1] === '\\') {
        i += 2
        continue
      }
    }
    if (src[i] === '\n' && src[i + 1] === '\n') return -1
    i += 1
  }
  return -1
}

function findClosingDisplayMath(src: string, start: number): number {
  let i = start
  while (i < src.length) {
    if (src[i] === '\\') {
      if (src[i + 1] === ']') return i
      if (src[i + 1] === '\\') {
        i += 2
        continue
      }
    }
    i += 1
  }
  return -1
}

/**
 * Converts LaTeX \(...\) and \[...\] delimiters to $...$ and $$...$$ outside code blocks and inline code.
 */
export function preprocessMathDelimiters(src: string): string {
  let out = ''
  let i = 0
  while (i < src.length) {
    if (i === 0 || src[i - 1] === '\n') {
      const nl = src.indexOf('\n', i)
      const lineEnd = nl === -1 ? src.length : nl
      const line = src.slice(i, lineEnd)
      const fence = line.match(/^[ ]{0,3}(`{3,}|~{3,})(.*)$/)
      if (fence && !(fence[1]![0] === '`' && fence[2]!.includes('`'))) {
        const afterOpen = nl === -1 ? src.length : nl + 1
        const blockEnd = findClosingFenceEnd(src, afterOpen, fence[1]![0]!, fence[1]!.length)
        out += src.slice(i, blockEnd)
        i = blockEnd
        continue
      }
    }

    if (src[i] === '`') {
      let tickLen = 0
      while (src[i + tickLen] === '`') tickLen += 1
      const tickRun = '`'.repeat(tickLen)
      let searchFrom = i + tickLen
      let closeIdx = -1
      while (searchFrom < src.length) {
        const found = src.indexOf(tickRun, searchFrom)
        if (found === -1) break
        const beforeOk = src[found - 1] !== '`'
        const afterOk = src[found + tickLen] !== '`'
        if (beforeOk && afterOk) {
          closeIdx = found
          break
        }
        searchFrom = found + 1
      }
      if (closeIdx !== -1) {
        out += src.slice(i, closeIdx + tickLen)
        i = closeIdx + tickLen
      } else {
        out += tickRun
        i += tickLen
      }
      continue
    }

    if (src[i] === '\\') {
      const next = src[i + 1]
      if (next === '\\') {
        out += '\\\\'
        i += 2
        continue
      }
      if (next === '[') {
        const closeIdx = findClosingDisplayMath(src, i + 2)
        if (closeIdx !== -1) {
          const inner = src.slice(i + 2, closeIdx).trim()
          const lineStart = src.lastIndexOf('\n', i - 1) + 1
          const inTableRow = /^[ \t]*\|/.test(src.slice(lineStart, i))
          if (inTableRow && !inner.includes('\n')) {
            out += `$\\displaystyle ${inner}$`
          } else {
            out += `\n$$\n${inner}\n$$\n`
          }
          i = closeIdx + 2
          continue
        }
      }
      if (next === '(') {
        const closeIdx = findClosingInlineMath(src, i + 2)
        if (closeIdx !== -1) {
          const inner = src.slice(i + 2, closeIdx).trim()
          out += `$${inner}$`
          i = closeIdx + 2
          continue
        }
      }
    }

    out += src[i]
    i += 1
  }
  return out
}

export function RichText({ text, streaming = false }: { text: string; streaming?: boolean }) {
  const prepared = preprocessMathDelimiters(text)
  return <div className="markdown"><Markdown
    remarkPlugins={[remarkGfm, remarkMath]}
    rehypePlugins={[[rehypeKatex, { strict: 'ignore', throwOnError: false, trust: false }]]}
    skipHtml
    components={{
      img: ({ alt }) => <span>{alt}</span>,
      a: ({ children, href }) => <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>,
      table: ({ children }) => <div className="md-table-wrap"><table>{children}</table></div>,
      pre: ({ children }) => <CodeBlock streaming={streaming}>{children}</CodeBlock>,
      code: ({ className, children }) => {
        const lang = className?.replace(/^language-/, '')
        if (!lang || !hljs.getLanguage(lang)) return <code className={className}>{children}</code>
        const highlighted = hljs.highlight(String(children), { language: lang }).value
        return <code className={`hljs ${className ?? ''}`} dangerouslySetInnerHTML={{ __html: highlighted }} />
      },
    }}>{prepared}</Markdown></div>
}
