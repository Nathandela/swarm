# Publishing the privacy policy

`index.html` in this directory is a single self-contained static page — no external assets, no
CDN, no build step. Google Play needs it reachable at a public `https://` URL before a listing
can be submitted (`agents-tracker-pwc`).

The `Nathandela/swarm` repository is **public** (confirmed with `gh repo view Nathandela/swarm
--json visibility`), so GitHub Pages needs no extra access configuration.

## Steps (GitHub web UI — nobody has run these yet)

1. Go to `https://github.com/Nathandela/swarm/settings/pages`.
2. Under **Build and deployment → Source**, choose **Deploy from a branch**.
3. Under **Branch**, choose `main` and the folder `/docs`, then **Save**.
4. Wait for the deployment to finish — the same Pages settings page shows a banner
   ("Your site is live at …") once it has, usually within a minute or two. You can also watch
   it under the repo's **Actions** tab (a `pages build and deployment` run).
5. Open the resulting URL below and confirm the page renders and reads correctly on a phone.

No repository file changes are required beyond what is already committed: `docs/.nojekyll`
(added alongside this page) tells GitHub Pages to serve the `docs/` tree as plain static files
instead of running it through Jekyll, which would otherwise try to build every other `.md` file
under `docs/` into a themed page it was never meant to be.

## Resulting URL

```
https://nathandela.github.io/swarm/ops/privacy-policy/
```

This follows directly from the folder layout: with `/docs` as the Pages root, the site's path
mirrors the repo path below `docs/`, so `docs/ops/privacy-policy/index.html` is served at
`/ops/privacy-policy/` (the trailing slash resolves to `index.html`).

Note that `https://nathandela.github.io/swarm/` (the bare root) will most likely 404 — there is
no `docs/index.html` — but that does not matter for Play Console, which only needs the specific
policy URL above to resolve.

## Updating the page later

Edit `index.html` in place and push to `main`; GitHub Pages redeploys automatically from the
same branch/folder, at the same URL. Bump the "Last updated" date in the page whenever a
material change is made, per the page's own "Changes" clause.

## Recording the URL

Once this is live, replace the pending marker in
`docs/ops/play-closed-testing-application.md` (§9 and the App content declarations table in
§6) with the URL above, and close `agents-tracker-pwc`.
