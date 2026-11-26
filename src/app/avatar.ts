const SAFE_AVATAR_DATA_URL_RE = /^data:image\/(jpeg|png|webp);base64,[A-Za-z0-9+/]+={0,2}$/

/**
 * Validates that avatarData is strictly a base64 raster image data URL (jpeg, png, webp)
 * so untrusted SVGs, HTML, javascript: URLs, or whitespace-injected strings never reach <img src>.
 */
export function sanitizeAvatarDataUrl(value?: string | null): string | undefined {
  if (!value || !SAFE_AVATAR_DATA_URL_RE.test(value)) {
    return undefined
  }
  return value
}
