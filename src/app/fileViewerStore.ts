import { create } from 'zustand'
import type { MessageAttachment } from '../protocol/types'
import { isRenderableImageAttachment } from './attachments'

export const DEFAULT_VIEWER_WIDTH_PCT = 45
export const MIN_VIEWER_WIDTH_PCT = 25
export const MAX_VIEWER_WIDTH_PCT = 65

export function clampViewerWidthPct(pct: number): number {
  if (!Number.isFinite(pct)) return DEFAULT_VIEWER_WIDTH_PCT
  return Math.max(MIN_VIEWER_WIDTH_PCT, Math.min(MAX_VIEWER_WIDTH_PCT, Math.round(pct * 10) / 10))
}

interface FileViewerState {
  file: MessageAttachment | null
  lightboxFile: MessageAttachment | null
  widthPct: number
  expanded: boolean
  openFile: (file: MessageAttachment) => void
  closeFile: () => void
  closeLightbox: () => void
  setWidthPct: (pct: number) => void
  toggleExpanded: () => void
}

export const useFileViewer = create<FileViewerState>(set => ({
  file: null,
  lightboxFile: null,
  widthPct: DEFAULT_VIEWER_WIDTH_PCT,
  expanded: false,
  openFile: file => {
    if (isRenderableImageAttachment(file)) {
      set({ lightboxFile: file })
    } else {
      set({ file })
    }
  },
  closeFile: () => set({ file: null, expanded: false }),
  closeLightbox: () => set({ lightboxFile: null }),
  setWidthPct: pct => set({ widthPct: clampViewerWidthPct(pct), expanded: false }),
  toggleExpanded: () => set(s => ({ expanded: !s.expanded })),
}))
