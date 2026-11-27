import assert from 'node:assert/strict'
import { test } from 'node:test'
import { createElement } from 'react'
import { renderToStaticMarkup } from 'react-dom/server'
import { preprocessMathDelimiters, RichText } from '../src/app/Markdown'

test('RichText renders \\(...\\), \\[...\\], $...$, and $$...$$ as KaTeX', () => {
  const html = renderToStaticMarkup(createElement(RichText, {
    text: [
      'Строчная \\(L_1 = L_2 = \\operatorname{Lin}(e)\\) и $x^2$.',
      '',
      '\\[\\ker(A - \\rho I)\\]',
      '',
      '$$',
      '\\sum_i \\alpha_i',
      '$$',
    ].join('\n'),
  }))

  const katexMatches = html.match(/class="katex"/g) ?? []
  assert.equal(katexMatches.length, 4, `expected 4 .katex formulas, got ${katexMatches.length}`)
  assert.match(html, /class="katex-display"/)
  assert.ok(!html.includes('katex-error'), `unexpected katex-error in ${html}`)
})

test('preprocessMathDelimiters and RichText preserve \\(...\\) and \\[...\\] inside inline code and fenced code blocks', () => {
  const input = [
    'В коде: `grep "\\(foo\\)"` и ``\\[bar\\]``.',
    '',
    '```bash',
    'echo "\\(not math\\)"',
    'printf "\\[also not math\\]"',
    '```',
    '',
    'А здесь формула: \\(\\varphi^{-1}(X)\\).',
  ].join('\n')

  const preprocessed = preprocessMathDelimiters(input)
  assert.ok(preprocessed.includes('`grep "\\(foo\\)"`'))
  assert.ok(preprocessed.includes('``\\[bar\\]``'))
  assert.ok(preprocessed.includes('echo "\\(not math\\)"'))
  assert.ok(preprocessed.includes('printf "\\[also not math\\]"'))
  assert.ok(preprocessed.includes('$\\varphi^{-1}(X)$'))

  const html = renderToStaticMarkup(createElement(RichText, { text: input }))
  const katexMatches = html.match(/class="katex"/g) ?? []
  assert.equal(katexMatches.length, 1, 'only the outside formula should render as KaTeX')
  assert.ok(html.includes('grep &quot;\\(foo\\)&quot;') || html.includes('grep "\\(foo\\)"'))
})

test('RichText wraps GFM tables in .md-table-wrap for horizontal scroll', () => {
  const html = renderToStaticMarkup(createElement(RichText, {
    text: [
      '| Раздел | Формулировка |',
      '| --- | --- |',
      '| §1.6, теорема 15 | Если оператор линеен, то \\(\\ker(A)\\) инвариантно |',
      '| §2.1, леммы 4–5 | Блочная \\[\\ker(A - \\rho I)\\] |',
    ].join('\n'),
  }))

  assert.match(html, /<div class="md-table-wrap"><table>/)
  assert.match(html, /<\/table><\/div>/)
  const katexMatches = html.match(/class="katex"/g) ?? []
  assert.equal(katexMatches.length, 2, 'both inline and display-style math inside table cells should render')
})
