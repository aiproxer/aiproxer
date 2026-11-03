import { registerHooks } from 'node:module'

// Test-only module resolution: replace the external SDK query boundary with the
// deterministic fixture. Production sidecar files are executed unchanged.
if (typeof registerHooks !== 'function') {
  throw new Error('node:module registerHooks is required (Node 22.15 or newer)')
}

registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === '@anthropic-ai/claude-agent-sdk') {
      return { url: new URL('./sdk-fixture.mjs', import.meta.url).href, shortCircuit: true }
    }
    return nextResolve(specifier, context)
  },
})
