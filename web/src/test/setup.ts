// Vitest setup file.
//
// Loaded by `vite.config.ts` → `test.setupFiles` before every test file.
// Currently we only register `@testing-library/jest-dom`'s custom matchers
// (`.toBeInTheDocument()`, `.toHaveTextContent()` etc.) so individual
// tests don't have to import them.
import '@testing-library/jest-dom/vitest';
