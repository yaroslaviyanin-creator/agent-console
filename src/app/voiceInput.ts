import { useCallback, useEffect, useRef, useState } from 'react'

interface SpeechRecognitionResultAlternativeLike {
  transcript: string
}

interface SpeechRecognitionResultLike {
  isFinal: boolean
  0: SpeechRecognitionResultAlternativeLike
}

interface SpeechRecognitionEventLike {
  resultIndex: number
  results: ArrayLike<SpeechRecognitionResultLike>
}

interface SpeechRecognitionInstanceLike {
  lang: string
  interimResults: boolean
  continuous: boolean
  onresult: ((ev: SpeechRecognitionEventLike) => void) | null
  onerror: ((ev: { error?: string }) => void) | null
  onend: (() => void) | null
  start: () => void
  stop: () => void
  abort: () => void
}

type SpeechRecognitionCtor = new () => SpeechRecognitionInstanceLike

export function getSpeechRecognitionCtor(): SpeechRecognitionCtor | null {
  if (typeof window === 'undefined') return null
  const w = window as unknown as {
    SpeechRecognition?: SpeechRecognitionCtor
    webkitSpeechRecognition?: SpeechRecognitionCtor
  }
  return w.SpeechRecognition ?? w.webkitSpeechRecognition ?? null
}

export function useVoiceDictation({
  lang = 'ru-RU',
  onUpdateText,
  getCurrentText,
}: {
  lang?: string
  getCurrentText: () => string
  onUpdateText: (nextText: string) => void
}) {
  const supported = getSpeechRecognitionCtor() !== null
  const [listening, setListening] = useState(false)
  const recRef = useRef<SpeechRecognitionInstanceLike | null>(null)
  const baseTextRef = useRef('')

  const stop = useCallback(() => {
    const rec = recRef.current
    recRef.current = null
    if (rec) {
      try { rec.stop() } catch { /* ignore */ }
    }
    setListening(false)
  }, [])

  useEffect(() => () => {
    if (recRef.current) {
      try { recRef.current.abort() } catch { /* ignore */ }
      recRef.current = null
    }
  }, [])

  const toggle = useCallback(() => {
    if (listening) {
      stop()
      return
    }
    const Ctor = getSpeechRecognitionCtor()
    if (!Ctor) return
    const rec = new Ctor()
    rec.lang = lang
    rec.interimResults = true
    rec.continuous = true
    const current = getCurrentText()
    baseTextRef.current = current && !/\s$/.test(current) ? `${current} ` : current
    let finalized = ''

    rec.onresult = ev => {
      let interim = ''
      for (let i = ev.resultIndex; i < ev.results.length; i++) {
        const res = ev.results[i]
        if (!res?.[0]) continue
        const chunk = res[0].transcript
        if (res.isFinal) {
          finalized += chunk
        } else {
          interim += chunk
        }
      }
      onUpdateText(baseTextRef.current + finalized + interim)
    }
    rec.onerror = () => {
      recRef.current = null
      setListening(false)
    }
    rec.onend = () => {
      recRef.current = null
      setListening(false)
    }

    try {
      rec.start()
      recRef.current = rec
      setListening(true)
    } catch {
      recRef.current = null
      setListening(false)
    }
  }, [listening, stop, lang, getCurrentText, onUpdateText])

  return { supported, listening, toggle, stop }
}
