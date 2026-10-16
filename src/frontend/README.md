# Frontend

Next.js frontend for the e-waste platform. Login uses the workflow API. After login the UI shows **only that user’s role** — the role pill is a label, not a switcher.

From the repo root:

```bash
cd src/frontend
cp .env.example .env.local
npm install
npm test
npm run dev
```

If you are already in this folder, skip `cd`. If you are in WSL and `npm` errors with `\\wsl.localhost\...` or `C:\Users\...`, you are using Windows npm. Check with `which npm`. It should be a Linux path such as `~/.nvm/versions/node/.../bin/npm`. Load nvm (`source ~/.bashrc`), then `rm -rf node_modules` and `npm install` again. Do not use `--legacy-peer-deps` for this.

Open http://localhost:3000 and sign in with a seeded account such as `donor1@ewaste.test`. The accounts and their password are listed in [database/README.md](../../database/README.md#seeded-synthetic-accounts).

`.env.example` points `API_PROXY_TARGET` at the Azure DEV API. To use a local backend instead, set it to `http://localhost:8080` in `.env.local`.

The browser calls `/api/v1/*` on the Next.js origin. Next rewrites that to `API_PROXY_TARGET` so you do not depend on Go CORS. Restart `npm run dev` after changing env. Do not set `API_PROXY_TARGET` to the Azure UI host.

Copy `.env.example` at the repo root for the API base URL. Do not commit `.env` or `.env.local`.

## Redirects and session expiry

- `/` sends signed-out users to `/login` and signed-in users to `/home`.
- `/home` requires a session; otherwise you are sent to `/login` (not treated as expiry).
- `/login` with a session sends you to `/home` (including when you type `/login` in the address bar).
- Access JWT expiry (`expires_in` from the login response) refreshes silently. Refresh JWT expiry or revocation sends you to `/login?expired=1` with **Your session expired. Please sign in again.**
- **Logout** returns to `/login` with no expiry banner.
- **F5 / reload** keeps you on `/home` if the session is still valid. Closing the tab ends the session. This is not `localStorage`; a tab-only copy is used.

## PWA / offline shell

The app can be installed and keeps a cached shell (`/home`, `/login`), so it opens without a network. The service worker only runs in **production builds**. `npm run dev` never registers it.

To try it locally:

```bash
npm run build && npm start
```

- **Chrome / Edge:** open http://localhost:3000, then go to DevTools > Application. *Manifest* shows the name, the icons and the install prompt. *Service workers* shows `/sw.js?v=<build>`. *Cache storage* shows the caches. Tick *Offline* under Network and reload `/home` to see the cached shell.
- **iOS Safari:** Share > **Add to Home Screen**. This needs HTTPS, so use a deployed environment. iOS reads the `apple-touch-icon` and `apple-mobile-web-app-*` tags set in `layout.tsx`.

How it works:

- `src/app/manifest.ts` is served at `/manifest.webmanifest` (`start_url` `/home`, standalone, theme `#134e4a`). The theme colour is repeated in `viewport` in `layout.tsx`.
- `src/lib/offline/register-sw.ts` registers `public/sw.js` as `/sw.js?v=<BUILD_ID>`. `next.config.ts` takes `BUILD_ID` from the `BUILD_ID` env var. Without it, the build uses the git commit, then a timestamp. In Docker, pass `--build-arg BUILD_ID=<sha>`.
- There are two caches. `shell-<build>` holds the two pages, the manifest, the icons and the favicon. `static-<build>` holds `/_next/static/*`. When a new build activates, it deletes the old caches.
- A new version installs in the background. It **does not take over until the app is closed and reopened**, so the UI never changes in the middle of a shift.
- Pages come from the network first, with the cached copy as the offline fallback. `/_next/static/*` comes from the cache first. `/api/*` and RSC requests are never cached or intercepted.
- After sign-in, `precacheLoadedStatic()` asks the worker to cache the chunks the tab has already loaded.

The icons live in `public/icons`. To change the artwork, edit `scripts/generate-pwa-icons.mjs` and run `node scripts/generate-pwa-icons.mjs` (it needs no image libraries). Then commit the new PNGs.
