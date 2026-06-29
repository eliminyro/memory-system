// app.js — Knowledge UI auth module (Task 4) + browse/search/read views (Task 5).
// marked global (loaded via /ui/vendor/marked.min.js): marked.parse(...)

let CONFIG = null;
async function loadConfig() {
  if (!CONFIG) CONFIG = await (await fetch("/ui/config.json")).json();
  return CONFIG;
}

function b64url(bytes) {
  return btoa(String.fromCharCode(...new Uint8Array(bytes)))
    .replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
async function sha256(s) {
  return new Uint8Array(await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s)));
}
function randomVerifier() {
  return b64url(crypto.getRandomValues(new Uint8Array(32)));
}

async function beginLogin() {
  const cfg = await loadConfig();
  const verifier = randomVerifier();
  const state = randomVerifier();
  sessionStorage.setItem("pkce_verifier", verifier);
  sessionStorage.setItem("oauth_state", state);
  const challenge = b64url(await sha256(verifier));
  const p = new URLSearchParams({
    response_type: "code",
    client_id: cfg.client_id,
    redirect_uri: cfg.redirect_uri,
    scope: "openid email",
    code_challenge: challenge,
    code_challenge_method: "S256",
    resource: cfg.resource,
    state,
  });
  window.location.href = `${cfg.issuer}/oauth/authorize?${p}`;
}

async function exchangeCode(code) {
  const cfg = await loadConfig();
  const verifier = sessionStorage.getItem("pkce_verifier");
  const body = new URLSearchParams({
    grant_type: "authorization_code",
    code,
    redirect_uri: cfg.redirect_uri,
    client_id: cfg.client_id,
    code_verifier: verifier,
    resource: cfg.resource,
  });
  const res = await fetch(`${cfg.issuer}/oauth/token`, {
    method: "POST",
    headers: { "Content-Type": "application/x-www-form-urlencoded" },
    body,
  });
  if (!res.ok) throw new Error("token exchange failed");
  const tok = await res.json();
  sessionStorage.setItem("access_token", tok.access_token);
  history.replaceState({}, "", "/ui/"); // strip ?code from the URL
}

// Call once on load: completes a redirect if ?code or ?error is present.
async function handleAuthRedirect() {
  const params = new URLSearchParams(window.location.search);
  if (params.has("error")) {
    const msg = params.get("error_description") || params.get("error");
    view.replaceChildren(el("p", { className: "state-msg state-err", textContent: `Login error: ${msg}` }));
    return;
  }
  if (params.has("code")) {
    const returnedState = params.get("state");
    const savedState = sessionStorage.getItem("oauth_state");
    if (returnedState !== savedState) {
      view.replaceChildren(el("p", { className: "state-msg state-err", textContent: "Login failed: state mismatch (possible CSRF)" }));
      return;
    }
    await exchangeCode(params.get("code"));
  }
}

async function apiFetch(path, opts = {}) {
  const token = sessionStorage.getItem("access_token");
  if (!token) { await beginLogin(); throw new Error("redirecting to login"); }
  const res = await fetch(`/api${path}`, {
    ...opts,
    headers: { ...(opts.headers || {}), Authorization: `Bearer ${token}` },
  });
  if (res.status === 401) { sessionStorage.removeItem("access_token"); await beginLogin(); throw new Error("re-auth"); }
  if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error || `HTTP ${res.status}`);
  return res.json();
}

// ── Task 5: Browse / search / read views ─────────────────────────────────────

const view = document.getElementById("view");

function el(tag, attrs = {}, ...kids) {
  const n = document.createElement(tag);
  Object.assign(n, attrs);
  for (const k of kids) n.append(k);
  return n;
}

function fmtDate(iso) {
  if (!iso) return "";
  return new Date(iso).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" });
}

// ── Document view ─────────────────────────────────────────────────────────────

async function showDocument(id) {
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  const doc = await apiFetch(`/documents/${id}`);
  view.replaceChildren();
  const wrap = el("div", { className: "doc-view" });
  wrap.append(el("h1", { textContent: doc.title }));
  wrap.append(el("div", {
    className: "meta",
    textContent: `${doc.doc_type || ""}  ·  ${doc.category}${doc.subcategory ? "/" + doc.subcategory : ""}/${doc.slug}  ·  updated ${fmtDate(doc.updated_at)}`,
  }));
  for (const s of doc.sections || []) {
    if (s.heading) wrap.append(el("h2", { textContent: s.heading }));
    if (s.status === "needs_verification") {
      wrap.append(el("div", { className: "stale" },
        el("p", { textContent: s.preview || "" }),
        el("p", { className: "hint", textContent: "stale — needs verification: " + (s.verify_hints || []).join(", ") }),
      ));
    } else {
      const box = el("div", { className: "md" });
      box.innerHTML = marked.parse(s.content || "");
      wrap.append(box);
    }
    if (s.verified_at) {
      wrap.append(el("p", { className: "meta", textContent: `verified ${fmtDate(s.verified_at)}` }));
    }
  }
  view.append(wrap);
}

// ── Search results view ───────────────────────────────────────────────────────

function renderSearchResults(results) {
  view.replaceChildren();
  if (!results.length) {
    view.append(el("p", { className: "state-msg", textContent: "no results" }));
    return;
  }
  const list = el("ul", { className: "doc-list" });
  for (const r of results) {
    const path = [r.category, r.subcategory, r.slug].filter(Boolean).join("/");
    const tierClass = r.relevance ? `tier-${r.relevance}` : "";
    const item = el("li", { className: `doc-item ${tierClass}`.trim() },
      el("h3", { textContent: r.doc_title }),
      el("div", { className: "doc-meta" },
        el("span", { textContent: path }),
        el("span", { textContent: r.doc_type ? ` · ${r.doc_type}` : "" }),
        r.relevance ? el("span", { className: `relevance-badge tier-${r.relevance}`, textContent: ` · ${r.relevance}` }) : "",
        r.verified_at ? el("span", { textContent: ` · verified ${fmtDate(r.verified_at)}` }) : "",
        r.status === "needs_verification" ? el("span", { className: "stale-badge", textContent: " · stale" }) : "",
      ),
    );
    item.addEventListener("click", () => showDocument(r.document_id));
    list.append(item);
  }
  view.append(list);
}

// ── Browse (index) view ───────────────────────────────────────────────────────

async function renderBrowse() {
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  const entries = await apiFetch("/index?depth=summary");
  view.replaceChildren();

  // Group by category
  const byCategory = new Map();
  for (const e of entries) {
    if (!byCategory.has(e.category)) byCategory.set(e.category, []);
    byCategory.get(e.category).push(e);
  }

  for (const [cat, rows] of byCategory) {
    const section = el("section");
    const heading = el("h2", { textContent: cat, style: "cursor:pointer" });
    const sub = el("ul", { className: "doc-list" });

    // summary rows: one per subcategory (or category-level)
    for (const row of rows) {
      const label = row.subcategory || cat;
      const countText = row.doc_count != null ? ` (${row.doc_count})` : "";
      const item = el("li", { className: "doc-item" },
        el("h3", { textContent: label + countText }),
        row.topics ? el("div", { className: "doc-meta", textContent: row.topics }) : "",
      );
      item.addEventListener("click", () => renderCategoryDocs(cat, row.subcategory || null));
      sub.append(item);
    }

    // clicking the heading also drills into the whole category
    heading.addEventListener("click", () => renderCategoryDocs(cat, null));
    section.append(heading, sub);
    view.append(section);
  }
}

async function renderCategoryDocs(category, subcategory) {
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  const params = new URLSearchParams({ category });
  if (subcategory) params.set("subcategory", subcategory);
  const docs = await apiFetch(`/documents?${params}`);
  view.replaceChildren();

  const back = el("button", { textContent: "← back", className: "back-btn" });
  back.addEventListener("click", renderBrowse);
  view.append(back);
  view.append(el("h2", { textContent: subcategory ? `${category} / ${subcategory}` : category }));

  if (!docs.length) {
    view.append(el("p", { className: "state-msg", textContent: "no documents" }));
    return;
  }
  const list = el("ul", { className: "doc-list" });
  for (const doc of docs) {
    const meta = [doc.subcategory, doc.slug, doc.doc_type].filter(Boolean).join(" · ");
    const item = el("li", { className: "doc-item" },
      el("h3", { textContent: doc.title || doc.slug }),
      el("div", { className: "doc-meta", textContent: meta }),
    );
    item.addEventListener("click", () => showDocument(doc.id));
    list.append(item);
  }
  view.append(list);
}

// ── Search wiring ─────────────────────────────────────────────────────────────

let _searchTimer = null;

async function runSearch(q) {
  view.replaceChildren(el("p", { className: "state-msg", textContent: "searching…" }));
  try {
    const results = await apiFetch(`/search?q=${encodeURIComponent(q)}&limit=20`);
    renderSearchResults(results);
  } catch (err) {
    view.replaceChildren(el("p", { className: "state-msg state-err", textContent: `search failed: ${err.message}` }));
  }
}

function wireSearch() {
  const q = document.getElementById("q");
  if (!q) return;
  q.addEventListener("input", () => {
    clearTimeout(_searchTimer);
    const val = q.value.trim();
    if (!val) {
      renderBrowse().catch(showError);
      return;
    }
    _searchTimer = setTimeout(() => runSearch(val).catch(showError), 250);
  });
}

// ── Error helper ──────────────────────────────────────────────────────────────

function showError(err) {
  view.replaceChildren(el("p", { className: "state-msg state-err", textContent: err.message || String(err) }));
}

// ── Entry point ───────────────────────────────────────────────────────────────

(async function init() {
  try {
    await handleAuthRedirect();
    if (!sessionStorage.getItem("access_token")) {
      await beginLogin();
      return;
    }
    wireSearch();
    await renderBrowse();
  } catch (err) {
    // beginLogin() throws "redirecting to login" — that's expected; ignore it.
    if (err.message !== "redirecting to login") showError(err);
  }
}());
