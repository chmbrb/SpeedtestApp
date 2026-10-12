import { useCallback, useEffect, useState } from 'react'
import { parseFilters, serializeFilters, type Filters } from '../shared/state/filters'

/** Filters live in the URL query string, so any view can be bookmarked or shared and Back/Forward work. */
export function useFilters(): [Filters, (patch: Partial<Filters>) => void, () => void] {
  const [filters, setFilters] = useState<Filters>(() => parseFilters(window.location.search))

  useEffect(() => {
    const onPop = () => setFilters(parseFilters(window.location.search))
    window.addEventListener('popstate', onPop)
    return () => window.removeEventListener('popstate', onPop)
  }, [])

  const update = useCallback((patch: Partial<Filters>) => {
    setFilters((prev) => {
      const next = { ...prev, ...patch }
      const url = window.location.pathname + serializeFilters(next)
      if (url !== window.location.pathname + window.location.search) window.history.pushState(null, '', url)
      return next
    })
  }, [])

  const reset = useCallback(() => {
    window.history.pushState(null, '', window.location.pathname)
    setFilters(parseFilters(''))
  }, [])

  return [filters, update, reset]
}
