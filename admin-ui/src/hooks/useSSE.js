import { useEffect, useState, useRef, useCallback } from 'react'

// useSSE connects to the server's SSE stream and delivers flag update events.
//
// Returns:
//   lastUpdate  — the most recently received FlagState object
//   status      — 'connecting' | 'connected' | 'error'
//   error       — error message if status === 'error'
export function useSSE(environment) {
  const [lastUpdate, setLastUpdate] = useState(null)
  const [status, setStatus]         = useState('connecting')
  const [error, setError]           = useState(null)
  const esRef = useRef(null)
  const retryRef = useRef(null)
  const retryDelay = useRef(1000)

  const connect = useCallback(() => {
    if (esRef.current) {
      esRef.current.close()
    }
    setStatus('connecting')

    const es = new EventSource(`/api/v1/stream/${environment}`)
    esRef.current = es

    es.onopen = () => {
      setStatus('connected')
      setError(null)
      retryDelay.current = 1000 // reset backoff on success
    }

    // Default message handler (data events without a named event)
    es.onmessage = (e) => {
      try {
        const state = JSON.parse(e.data)
        // Skip ping / connected events (they don't have flag_key)
        if (state.flag_key) {
          setLastUpdate(state)
        }
      } catch { /* malformed JSON — ignore */ }
    }

    // Named "ping" event — keep alive, ignore data
    es.addEventListener('ping', () => { /* heartbeat, no-op */ })

    // Named "connected" event — server confirmed subscription
    es.addEventListener('connected', () => {
      setStatus('connected')
    })

    es.onerror = () => {
      setStatus('error')
      setError('SSE connection lost — retrying...')
      es.close()
      esRef.current = null

      // Exponential backoff: 1s → 2s → 4s → … → max 30s
      const delay = Math.min(retryDelay.current, 30000)
      retryDelay.current = delay * 2
      retryRef.current = setTimeout(connect, delay)
    }
  }, [environment])

  useEffect(() => {
    connect()
    return () => {
      if (esRef.current)   esRef.current.close()
      if (retryRef.current) clearTimeout(retryRef.current)
    }
  }, [connect])

  return { lastUpdate, status, error }
}
