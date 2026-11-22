import Markdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import hljs from 'highlight.js/lib/core'
import typescript from 'highlight.js/lib/languages/typescript'
import json from 'highlight.js/lib/languages/json'
import bash from 'highlight.js/lib/languages/bash'
import python from 'highlight.js/lib/languages/python'
import javascript from 'highlight.js/lib/languages/javascript'

for (const [name, language] of Object.entries({ typescript, json, bash, python, javascript })) hljs.registerLanguage(name, language)

export function RichText({ text }: { text: string }) {
  return <div className="markdown"><Markdown remarkPlugins={[remarkGfm]} skipHtml components={{
    img: ({ alt }) => <span>{alt}</span>,
    a: ({ children, href }) => <a href={href} target="_blank" rel="noopener noreferrer">{children}</a>,
    code: ({ className, children }) => {
      const lang = className?.replace(/^language-/, '')
      if (!lang || !hljs.getLanguage(lang)) return <code>{children}</code>
      const highlighted = hljs.highlight(String(children), { language: lang }).value
      return <code className="hljs" dangerouslySetInnerHTML={{ __html: highlighted }} />
    },
  }}>{text}</Markdown></div>
}
