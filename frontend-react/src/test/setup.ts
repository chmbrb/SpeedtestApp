import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterEach } from 'vitest'

// Testing Library only cleans up by itself when vitest has globals on, which it does not here.
afterEach(cleanup)
