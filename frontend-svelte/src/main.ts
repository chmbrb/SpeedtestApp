import '@fontsource-variable/instrument-sans'
import { mount } from 'svelte'
import './shared/styles.css'
import Gate from './components/Gate.svelte'
import { initInstall } from './shared/state/install'

initInstall() // the browser offers its install prompt early; catch it before the app has rendered

mount(Gate, { target: document.getElementById('root') as HTMLElement })
