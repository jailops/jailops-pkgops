# PkgOps registry UI

Vue 3 + TypeScript + Vite. Optional: `pkgops serve` works fully without it.

| Command | Does |
|---|---|
| `npm ci` | install |
| `npm run dev` | dev server on :5173, `/api` proxied to `PKGOPS_API` (default http://127.0.0.1:39112) |
| `npm run build` | typecheck and build into `../internal/webui/dist` (embedded into the Go binary; commit the result) |
| `npm test` | vitest: API client (incl. admin calls), session (token memory only, Access probe), router, formatters, dialogs, `manage.ts` logic (filters, bulk selection, confirmation gating, state/phase mapping), Manage view polling |
| `npm run e2e` | Playwright against a real seeded registry (builds `pkgops`, signs a repo, runs `pkgops serve`); desktop light/dark and phone, with an accessibility audit (`e2e/a11y.ts`) that also covers the Manage view and its dialogs. `e2e/manage.spec.ts` uses the real local registry for the manual-mode cleanup; GitHub mode, Access identities and the archive use `page.route()` mocks (`e2e/mock.ts`, named "mocked API" in the test titles) |
| `npm run screenshots` | writes `e2e/screenshots/<project>/*.png` for review |

Rules: no inline `style=` attributes (strict CSP, `style-src 'self'`), no external
assets, the network is only touched from `src/api.ts`, the sign-in token never
leaves that module's memory (not storage, URL or cookies).

## Manage view (`#/manage`)

Readable by everyone who can read; actions (cleanup, republish, archive deletion) only for
admins. Sign-in: the UI first calls `/api/v1/session` without a token; 200 means an access
gateway (Cloudflare Access) identified the visitor (shown as "via Access", no token sign-in),
401 keeps the token sign-in, 403 shows "identity not allowed". A cleanup is always a dry run
first, then a typed `prune`; it never deletes immediately ("takes effect after the next deploy").
Archive deletion is a separate, loud dialog (`delete from archive`). The overview is polled every
15 s only while a row is `pending-prune`, and the timer ends with the view. Pure logic lives in
`src/manage.ts`.
