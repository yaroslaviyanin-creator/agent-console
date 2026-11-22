import type { ConnectionMode as Mode } from './runtime'

export function ConnectionMode({ mode, disabled, change }: {
  mode: Mode
  disabled?: boolean
  change: (mode: Mode) => void
}) {
  return <div className="connection-mode" role="group" aria-label="Режим подключения">
    <button type="button" disabled={disabled} aria-pressed={mode === 'demo'} onClick={() => change('demo')}>Демо (Mock)</button>
    <button type="button" disabled={disabled} aria-pressed={mode === 'server'} onClick={() => change('server')}>Сервер</button>
  </div>
}
