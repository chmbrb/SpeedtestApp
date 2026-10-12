import '@fontsource-variable/instrument-sans'
import { mount } from 'svelte'
import './shared/styles.css'
import App from './App.svelte'
import { initInstall } from './shared/state/install'

initInstall() // the browser offers its install prompt early; catch it before the app has rendered

mount(App, { target: document.getElementById('root') as HTMLElement })
