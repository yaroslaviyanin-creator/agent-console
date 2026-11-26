import { sanitizeAvatarDataUrl } from './avatar'

export function avatarInitial(name?: string | null): string {
  const trimmed = name?.trim() ?? ''
  if (!trimmed) return '?'
  const first = Array.from(trimmed)[0] ?? '?'
  return first.toLocaleUpperCase()
}

export function UserAvatar({
  name,
  avatarData,
  className = '',
  alt = '',
}: {
  name?: string | null | undefined
  avatarData?: string | null | undefined
  className?: string
  alt?: string
}) {
  const classes = className ? `user-avatar ${className}` : 'user-avatar'
  const safeSrc = sanitizeAvatarDataUrl(avatarData)
  return (
    <span className={classes}>
      {safeSrc ? <img src={safeSrc} alt={alt} /> : avatarInitial(name)}
    </span>
  )
}
