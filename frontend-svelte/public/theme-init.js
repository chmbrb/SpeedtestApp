// Applies the saved theme before the first paint so there is no flash. Dark is the default.
// This is a separate file, not an inline script, so the page's Content-Security-Policy can forbid inline scripts.
try {
  var t = localStorage.getItem('theme')
  if (t === 'light' || t === 'dark') document.documentElement.dataset.theme = t
  else if (t === 'auto') document.documentElement.dataset.theme = matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
} catch (e) {}
