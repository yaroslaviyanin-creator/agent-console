import { isValidElement, useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import hljs from 'highlight.js/lib/core'
import typescript from 'highlight.js/lib/languages/typescript'
import json from 'highlight.js/lib/languages/json'
import bash from 'highlight.js/lib/languages/bash'
import python from 'highlight.js/lib/languages/python'
import javascript from 'highlight.js/lib/languages/javascript'
import { Check, Copy } from 'lucide-react'

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

function CodeBlock({ children }: { children?: ReactNode }) {
  const { lang, raw } = extractCodeInfo(children)
  const [copied, setCopied] = useState(false)
  const timerRef = useRef<number | undefined>(undefined)

  useEffect(() => () => {
    if (timerRef.current !== undefined) window.clearTimeout(timerRef.current)
  }, [])

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

export function RichText({ text }: { text: string }) {
  return <div className="markdown"><Markdown remarkPlugins={[remarkGfm]} skipHtml components={{
    img: ({ alt }) => <span>{alt}</span>,
    a: ({ children, href }) => <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>,
    pre: ({ children }) => <CodeBlock>{children}</CodeBlock>,
    code: ({ className, children }) => {
      const lang = className?.replace(/^language-/, '')
      if (!lang || !hljs.getLanguage(lang)) return <code className={className}>{children}</code>
      const highlighted = hljs.highlight(String(children), { language: lang }).value
      return <code className={`hljs ${className ?? ''}`} dangerouslySetInnerHTML={{ __html: highlighted }} />
    },
  }}>{text}</Markdown></div>
}
