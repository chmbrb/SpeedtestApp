import { parseFilters, serializeFilters, type Filters } from '../shared/state/filters'

/** Filters live in the URL query string, so any view can be bookmarked or shared and Back/Forward work. */
class FilterStore {
  value = $state<Filters>(parseFilters(window.location.search))

  constructor() {
    window.addEventListener('popstate', () => { this.value = parseFilters(window.location.search) })
  }

  update = (patch: Partial<Filters>) => {
    const next = { ...this.value, ...patch }
    const url = window.location.pathname + serializeFilters(next)
    if (url !== window.location.pathname + window.location.search) window.history.pushState(null, '', url)
    this.value = next
  }

  reset = () => {
    window.history.pushState(null, '', window.location.pathname)
    this.value = parseFilters('')
  }
}

export const filters = new FilterStore()
