import type { DiffLine, ToolCall } from '../protocol/types'

export const projectFolder = '/demo/project'
const settings = projectFolder + '/settings.ts'
const filename = '[\\p{L}\\p{N}_-]+(?:\\.[\\p{L}\\p{N}_-]+)+'

type Intent =
  | { name: 'list_dir' | 'run_command' | 'unknown' }
  | { name: 'read_file'; file: string }
  | { name: 'grep_search'; pattern: string }
  | { name: 'write_file'; file: string; content: string }
  | { name: 'edit_file'; timeout: number }

// These are deterministic demo sentences. No prompt is interpreted by a shell.
export function parseDemoIntent(prompt: string): Intent {
  const text = prompt.trim()
  if (/^(npm\s+test|запусти тесты)[.!]?$/iu.test(text)) return { name: 'run_command' }
  if (/^(покажи файлы проекта|список файлов)[.!]?$/iu.test(text)) return { name: 'list_dir' }
  const read = text.match(new RegExp('^прочитай\\s+(' + filename + ')$', 'iu'))
  if (read) return { name: 'read_file', file: read[1]! }
  const write = text.match(new RegExp('^создай\\s+(' + filename + ')\\s+с текстом:\\s*([\\s\\S]+)$', 'iu'))
  if (write) return { name: 'write_file', file: write[1]!, content: write[2]! }
  const grep = text.match(/^найди\s+«([^»]+)»(?:\s+в проекте)?[.!]?$/iu)
  if (grep) return { name: 'grep_search', pattern: grep[1]! }
  if (/^предложи изменение settings\.ts[.!]?$/iu.test(text)) return { name: 'edit_file', timeout: 3000 }
  if (/тайм-аут/iu.test(text) && /settings\.ts/iu.test(text)) {
    const value = text.match(/(?:до|на)\s+(\d{1,8})\s*(мс|секунд[а-я]*)[.!]?$/iu)
    if (value) return { name: 'edit_file', timeout: Number(value[1]) * (value[2] === 'мс' ? 1 : 1000) }
  }
  return { name: 'unknown' }
}

type ToolOutcome = { ok: boolean; output: string; answer: string }
type Step = { tool: ToolCall; perform: () => ToolOutcome; approval: boolean }
const fail = (message: string): ToolOutcome => ({ ok: false, output: message, answer: message })
const ok = (output: string, answer: string): ToolOutcome => ({ ok: true, output, answer })

export class MockProject {
  private files = new Map([
    [settings, 'export const timeout = 1000'],
    [projectFolder + '/README.md', '# Демо-проект\nНастройка подключения находится в settings.ts.'],
  ]);

  *steps(intent: Intent): Generator<Step> {
    switch (intent.name) {
      case 'read_file': {
        yield this.read(projectFolder + '/' + intent.file)
        break
      }
      case 'list_dir':
        yield { tool: { name: 'list_dir', path: projectFolder }, approval: false, perform: () => {
          const names = [...this.files.keys()].map(p => p.slice(projectFolder.length + 1)).sort()
          return ok(names.join('\n'), names.map(n => '- `' + n + '`').join('\n'))
        } }
        break
      case 'grep_search':
        yield { tool: { name: 'grep_search', path: projectFolder, pattern: intent.pattern }, approval: false, perform: () => {
          const hits: string[] = []
          for (const [path, content] of this.files) {
            content.split('\n').forEach((line, i) => {
              if (line.includes(intent.pattern)) hits.push(path.slice(projectFolder.length + 1) + ':' + (i + 1) + '  ' + line)
            })
          }
          const output = hits.join('\n') || 'Совпадений нет.'
          return ok(output, hits.length ? 'Совпадений: **' + hits.length + '**.\n\n' + codeBlock(output) : output)
        } }
        break
      case 'write_file': {
        const path = projectFolder + '/' + intent.file
        const tool = { name: 'write_file' as const, path, content: intent.content }
        yield { tool, approval: !this.files.has(path), perform: () => {
          if (this.files.has(path)) return fail('Файл уже существует: ' + intent.file)
          this.files.set(path, intent.content)
          return ok('Создан ' + path, 'Создал файл `' + intent.file + '` в демо-проекте.')
        } }
        break
      }
      case 'edit_file': {
        const before = this.files.get(settings)!
        const after = 'export const timeout = ' + intent.timeout
        const read = this.read(settings)
        yield read
        if (before === after) return
        const diff: DiffLine[] = [{ kind: 'remove', text: before }, { kind: 'add', text: after }]
        yield { tool: { name: 'edit_file', path: settings, diff }, approval: true, perform: () => {
          if (this.files.get(settings) !== before) return fail('Файл изменился после подготовки diff. Запросите новую правку.')
          this.files.set(settings, after)
          return ok('Изменение сохранено.', 'В `settings.ts` установлен тайм-аут **' + intent.timeout + ' мс**.\n\n' + codeBlock(after, 'ts'))
        } }
        break
      }
      case 'run_command':
        yield { tool: { name: 'run_command', cwd: projectFolder, command: 'npm test' }, approval: true, perform: () => {
          const content = this.files.get(settings) ?? ''
          const checks = [
            [this.files.has(settings), 'settings.ts существует'],
            [content.startsWith('export const timeout = '), 'параметр timeout экспортируется'],
            [/^export const timeout = [1-9]\d*$/.test(content), 'тайм-аут — положительное целое число'],
            [this.files.has(projectFolder + '/README.md'), 'README.md существует'],
            [!!this.files.get(projectFolder + '/README.md')?.includes('settings.ts'), 'README.md ссылается на настройки'],
          ] as const
          const report = checks.map(([pass, name]) => (pass ? '✓ ' : '× ') + name).join('\n')
          if (checks.some(([pass]) => !pass)) return fail(report)
          return ok('Демо: 5 тестов пройдено.\n' + report, 'Демо-проверка завершена. **5 тестов пройдено.**')
        } }
        break
    }
  }
  private read(path: string): Step {
    return { tool: { name: 'read_file', path }, approval: false, perform: () => {
      const text = this.files.get(path)
      if (text === undefined) return fail('Файл не найден: ' + path)
      return ok(text, codeBlock(text, path.endsWith('.ts') ? 'ts' : ''))
    } }
  }
}

function codeBlock(text: string, language = '') {
  const ticks = text.match(/`+/g) ?? []
  const fence = '`'.repeat(ticks.reduce((length, run) => Math.max(length, run.length + 1), 3))
  return fence + language + '\n' + text + '\n' + fence
}
