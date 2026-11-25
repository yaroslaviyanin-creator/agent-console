import { useState } from 'react'
import { ArrowRight, CodeXml, QrCode, Terminal } from 'lucide-react'
import { useTelegramBack } from '../platform/telegram'
import { login, restoreConnection, setConnectionMode, useChats } from './chatStore'
import { ConnectionMode } from './ConnectionMode'

const connection = btoa('mock://agent-console')
export function Login() {
  const { mode, address, restoring, connectionError } = useChats()
  const demo = mode === 'demo'
  const [mockAddress, setMockAddress] = useState(connection)
  const [server, setServer] = useState(address)
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirmation, setConfirmation] = useState('')
  const [register, setRegister] = useState(false)
  const registering = !demo && register
  const [device, setDevice] = useState('Рабочий ноутбук')
  const [qr, setQr] = useState(false)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  useTelegramBack(demo && qr, () => setQr(false))
  async function enter(quick: boolean) {
    setError('')
    if (registering && password !== confirmation) {
      setError('Пароли не совпадают.')
      return
    }
    setBusy(true)
    try {
      await login({
        connection: demo ? quick ? connection : mockAddress : server,
        login: quick ? 'demo' : username, password: quick ? 'demo' : password,
        deviceName: quick ? device.trim() || 'Рабочий ноутбук' : device,
      }, registering)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Не удалось подключиться.')
    } finally {
      setBusy(false)
    }
  }
  return <main className="login-screen"><section className="login-card"><div className="auth-brand"><span className="brand-logo"><CodeXml size={21}/></span>Agent Console</div>
    <h1>Подключение</h1>
    <ConnectionMode mode={mode} disabled={busy || restoring} change={next => {
      if (next === mode) return
      setError('')
      setPassword('')
      setConfirmation('')
      setRegister(false)
      setQr(false)
      setConnectionMode(next)
    }}/>
    {restoring ? <p className="form-help" role="status">Восстанавливаю сессию…</p> : <>
      {demo && <p className="form-help">Демо-сервер работает локально. Используйте учебную учётную запись.</p>}
      {!demo && <div className="auth-switch" role="group" aria-label="Действие с аккаунтом">
        {[false, true].map(value => <button key={String(value)} type="button" className="alt-action" aria-pressed={register === value} disabled={busy} onClick={() => {
          setRegister(value)
          setError('')
          setPassword('')
          setConfirmation('')
        }}>{value ? 'Регистрация' : 'Вход'}</button>)}
      </div>}
      <form className="panel-form" onSubmit={e => { e.preventDefault(); void enter(false) }}>
        {demo ? <label>Строка подключения<div className="input-action"><input required aria-label="Строка подключения" spellCheck={false} value={mockAddress} onChange={e => setMockAddress(e.target.value)}/><button type="button" title="Демо QR" aria-label="Демо QR" onClick={() => setQr(!qr)}><QrCode size={18}/></button></div></label>
          : <label>Адрес сервера<input type="url" required spellCheck={false} autoCapitalize="none" value={server} onChange={e => setServer(e.target.value)}/></label>}
        {demo && qr && <div className="qr-preview"><QrCode size={32}/><div>Демонстрация QR-кода<button type="button" onClick={() => { setMockAddress(connection); setQr(false) }}>Подставить строку подключения</button></div></div>}
        <div className="form-pair">
          <label>Логин<input required autoComplete={demo ? 'off' : 'username'} autoCapitalize="none" value={username} onChange={e => setUsername(e.target.value)} placeholder={demo ? 'demo' : undefined}/></label>
          <label>Пароль<input required type="password" autoComplete={demo ? 'off' : registering ? 'new-password' : 'current-password'} value={password} onChange={e => setPassword(e.target.value)}/></label>
        </div>
        {registering && <label>Подтверждение пароля<input required type="password" autoComplete="new-password" value={confirmation} onChange={e => setConfirmation(e.target.value)}/></label>}
        <label>Название устройства<input required maxLength={80} value={device} onChange={e => setDevice(e.target.value)}/></label>
        {(error || connectionError) && <p role="alert">{error || connectionError}</p>}
        <button className="primary-action" disabled={busy}>{busy ? 'Подключаюсь…' : registering ? 'Зарегистрироваться' : 'Подключиться'}<ArrowRight size={15}/></button>
        {demo && <button type="button" className="alt-action" disabled={busy} onClick={() => void enter(true)}><Terminal size={15}/>Быстрый демо-вход</button>}
        {!demo && connectionError && <button type="button" className="alt-action" disabled={busy} onClick={() => void restoreConnection()}>Восстановить сессию</button>}
      </form>
      {demo && <p className="demo-accounts">Демо-логины: <code>demo</code>, <code>alex</code>, <code>maria</code>. Пароль для всех: <code>demo</code>.</p>}
    </>}
  </section></main>
}
