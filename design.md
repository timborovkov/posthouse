# Product direction

Posthouse should feel like a quiet switchboard: terse, predictable, and safe under automation. JSON is the canonical CLI output; human presentation belongs in the full-screen TUI. Names, categories, labels, exact connections, and acting identities remain visible at every write boundary.

The interaction hierarchy is connection → capability → item → prepared operation → execution. Read operations may fan out across a selector and return partial source errors. Every mail or CalDAV write resolves to exactly one connection, is previewed, and is then idempotently executed. Calendar generation stays provider-independent; CalDAV mutation is a separate prepared operation.

Terminal presentation uses a neutral monochrome base, color only for state and risk, visible focus, complete keyboard control, cancellable asynchronous loading, and no animation that delays input.

Sensitive provider content is encrypted before SQLite or attachment-blob persistence. Plain indexing fields are restricted to connection IDs, opaque provider IDs, timestamps, flags, sizes, and sync state. The cache is bounded and evicts attachments before bodies. Live providers remain canonical; the cache only supports explicit offline reads and live-first stale fallback.

The encrypted key-check marker makes a wrong cache key a readiness failure. Rekeying holds SQLite's write lock while every encrypted writer verifies that marker, preventing stale processes from persisting old-key ciphertext. Headless operators must replace `POSTHOUSE_CACHE_KEY` with the requested new value immediately after a successful rekey.

Provider credentials are attached only to the configured CalDAV origin. Mutable event hrefs must normalize beneath the exact discovered collection selected in the prepared operation; cross-origin redirects and cross-collection object paths are rejected before authentication. Transport errors redact configured feed and CalDAV URLs so secret URL material cannot cross the CLI/MCP boundary.
