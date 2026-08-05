// app.js — Knowledge UI auth module (Task 4) + browse/search/read views (Task 5).
// marked global (loaded via /ui/vendor/marked.min.js): marked.parse(...)
// DOMPurify global (loaded via /ui/vendor/purify.min.js): DOMPurify.sanitize(...)

// renderMarkdown converts untrusted markdown to HTML and sanitizes it before
// it is ever assigned to innerHTML. Memory section content is attacker-
// controllable, so marked's output must pass through DOMPurify — otherwise an
// <img onerror>/<script>/javascript: payload stored in a section would execute
// in the reader's session (stored XSS, audit #3). Returns inert HTML.
function renderMarkdown(md) {
  return DOMPurify.sanitize(marked.parse(md || ""));
}

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
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    const err = new Error(body.error || `HTTP ${res.status}`);
    err.status = res.status; // let callers distinguish 403 (e.g. non-admin) from other failures
    throw err;
  }
  if (res.status === 204 || res.headers.get("content-length") === "0") return null;
  const ct = res.headers.get("content-type") || "";
  return ct.includes("application/json") ? res.json() : null;
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

// ── Navigation history ────────────────────────────────────────────────────────
// A stack of thunks that re-render the previous view. Drilling into a view
// pushes the way back; the back button pops and re-renders it. The two roots
// (browse, search results) reset the stack so back never escapes them.
const navStack = [];
function goBack() {
  (navStack.pop() || renderBrowse)();
}
function backBtn() {
  const b = el("button", { textContent: "←", className: "back-btn", title: "Back" });
  b.addEventListener("click", goBack);
  return b;
}

// ── Document view ─────────────────────────────────────────────────────────────

// splitFrontmatter separates a leading YAML frontmatter block (delimited by a
// pair of "---" fence lines) from the markdown body. Returns the raw
// frontmatter text (without the fences) and the body. If there is no opening
// fence on the first line, or no closing fence, the whole input is the body.
function splitFrontmatter(content) {
  const text = content || "";
  const lines = text.split("\n");
  if (lines[0].trim() !== "---") return { frontmatter: "", body: text };
  for (let i = 1; i < lines.length; i++) {
    if (lines[i].trim() === "---") {
      return {
        frontmatter: lines.slice(1, i).join("\n"),
        body: lines.slice(i + 1).join("\n").replace(/^\n+/, ""),
      };
    }
  }
  return { frontmatter: "", body: text };
}

// inlineEdit turns a display element (h1/h2/placeholder) into a click-to-edit
// single-line field: click -> input prefilled with `current`; blur -> if the
// value changed, await onSave(value); Escape -> cancel. onSave returns the
// value to display (e.g. the server's canonical value). Same blur-commit model
// as section content editing.
function inlineEdit(displayEl, current, onSave) {
  displayEl.classList.add("editable-text");
  displayEl.title = "Click to edit";
  displayEl.addEventListener("click", () => {
    const input = el("input", { className: "inline-edit-input", value: current || "", type: "text" });
    let settled = false;

    async function commit() {
      if (settled) return;
      settled = true;
      const next = input.value;
      if (next === (current || "")) { input.replaceWith(displayEl); return; }
      try {
        const shown = await onSave(next);
        current = shown != null ? shown : next;
        displayEl.textContent = current || displayEl.dataset.placeholder || "";
        displayEl.classList.toggle("placeholder", !current);
        input.replaceWith(displayEl);
      } catch (err) {
        settled = false;
        alert("Save failed: " + err.message);
        input.focus();
      }
    }

    input.addEventListener("blur", commit);
    input.addEventListener("keydown", (e) => {
      if (e.key === "Escape") { e.preventDefault(); settled = true; input.replaceWith(displayEl); }
      if (e.key === "Enter") { e.preventDefault(); input.blur(); }
    });

    displayEl.replaceWith(input);
    input.focus();
    input.select();
  });
}

// Build a single section container. The read view renders frontmatter as a dim
// metadata block and the body as markdown. Clicking the read view (for
// editable sections) swaps it for a textarea; blurring it saves and returns to
// the read view. needs_verification sections show a preview and can only be
// verified, not edited.
function sectionEl(doc, s) {
  const container = el("div", { className: "section-block" + (s.status === "needs_verification" ? " stale" : "") });

  const editable = s.status !== "needs_verification";

  // Heading — editable; sections without one get a faint "add heading" placeholder.
  if (editable) {
    const headingEl = el("h2", {
      textContent: s.heading || "Add heading…",
      className: s.heading ? "" : "placeholder",
    });
    headingEl.dataset.placeholder = "Add heading…";
    inlineEdit(headingEl, s.heading || "", async (heading) => {
      const updated = await apiFetch("/sections/" + s.id, {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ heading }),
      });
      s.heading = updated.heading || "";
      return s.heading;
    });
    container.append(headingEl);
  } else if (s.heading) {
    container.append(el("h2", { textContent: s.heading }));
  }

  // Read-view content box.
  const mdBox = el("div", { className: "md" + (editable ? " editable" : "") });

  function renderRead() {
    mdBox.replaceChildren();
    if (!editable) {
      mdBox.append(
        el("p", { textContent: s.preview || "" }),
        el("p", { className: "hint", textContent: "stale — needs verification: " + (s.verify_hints || []).join(", ") }),
      );
      return;
    }
    const { frontmatter, body } = splitFrontmatter(s.content);
    if (frontmatter) mdBox.append(el("div", { className: "frontmatter", textContent: frontmatter }));
    const bodyDiv = el("div", { className: "md-body" });
    bodyDiv.innerHTML = renderMarkdown(body);
    mdBox.append(bodyDiv);
  }
  renderRead();

  // Click-to-edit: swap the read view for a textarea; blur commits.
  function enterEdit() {
    const ta = el("textarea", { className: "sec-edit-ta", value: s.content || "" });
    let settled = false;

    async function commit() {
      if (settled) return;
      settled = true;
      if (ta.value === (s.content || "")) { ta.replaceWith(mdBox); return; } // unchanged
      try {
        const result = await apiFetch("/sections/" + s.id, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ content: ta.value }),
        });
        s.content = result.content;
        container.classList.remove("stale");
        renderRead();
        ta.replaceWith(mdBox);
      } catch (err) {
        settled = false; // keep editing so the user can retry
        alert("Save failed: " + err.message);
        ta.focus();
      }
    }

    function cancel() {
      if (settled) return;
      settled = true;
      ta.replaceWith(mdBox);
    }

    ta.addEventListener("blur", commit);
    ta.addEventListener("keydown", (e) => {
      if (e.key === "Escape") { e.preventDefault(); cancel(); }
    });

    mdBox.replaceWith(ta);
    ta.focus();
  }

  if (editable) {
    mdBox.title = "Click to edit";
    mdBox.addEventListener("click", (e) => {
      if (e.target.closest("a")) return; // let links work normally
      enterEdit();
    });
  }

  container.append(mdBox);

  if (s.verified_at) {
    container.append(el("p", { className: "meta", textContent: `verified ${fmtDate(s.verified_at)}` }));
  }

  // Controls row — Verify only. Editing is click-to-edit (no Edit button).
  const controls = el("div", { className: "section-controls" });
  const verifyBtn = el("button", { textContent: "Verify", className: "sec-btn" });
  verifyBtn.addEventListener("click", async () => {
    verifyBtn.disabled = true;
    try {
      await apiFetch("/sections/" + s.id + "/verify", { method: "POST" });
      container.classList.remove("stale");
      verifyBtn.textContent = "Verified";
      verifyBtn.classList.add("sec-btn-verified");
      setTimeout(() => { verifyBtn.textContent = "Verify"; verifyBtn.classList.remove("sec-btn-verified"); verifyBtn.disabled = false; }, 2000);
    } catch (err) {
      verifyBtn.disabled = false;
      alert("Verify failed: " + err.message);
    }
  });
  controls.append(verifyBtn);
  container.append(controls);
  return container;
}

async function showDocument(id) {
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  const doc = await apiFetch(`/documents/${id}`);
  view.replaceChildren();
  const wrap = el("div", { className: "doc-view" });

  // Header row: back + title + delete button
  const hdr = el("div", { className: "doc-hdr" });
  hdr.append(backBtn());
  const titleEl = el("h1", { textContent: doc.title });
  inlineEdit(titleEl, doc.title, async (title) => {
    const updated = await apiFetch("/documents/" + doc.id, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title }),
    });
    doc.title = updated.title;
    return updated.title;
  });
  hdr.append(titleEl);
  const delBtn = el("button", { textContent: "Delete", className: "sec-btn sec-btn-danger", title: "Delete document" });
  delBtn.addEventListener("click", async () => {
    if (!confirm("Delete this document?")) return;
    delBtn.disabled = true;
    try {
      await apiFetch("/documents/" + doc.id, { method: "DELETE" });
      renderBrowse();
    } catch (err) {
      delBtn.disabled = false;
      alert("Delete failed: " + err.message);
    }
  });
  hdr.append(delBtn);
  wrap.append(hdr);

  wrap.append(el("div", {
    className: "meta",
    textContent: `${doc.doc_type || ""}  ·  ${doc.category}${doc.subcategory ? "/" + doc.subcategory : ""}/${doc.slug}  ·  updated ${fmtDate(doc.updated_at)}`,
  }));

  for (const s of doc.sections || []) {
    wrap.append(sectionEl(doc, s));
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
  const legend = legendFor(results);
  if (legend) view.append(legend);
  const list = el("ul", { className: "doc-list" });
  for (const r of results) {
    const path = [r.category, r.subcategory, r.slug].filter(Boolean).join("/");
    const tierClass = r.relevance ? `tier-${r.relevance}` : "";
    const meta = el("div", { className: "doc-meta" },
      el("span", { textContent: path }),
      el("span", { textContent: r.doc_type ? ` · ${r.doc_type}` : "" }),
      r.relevance ? el("span", { className: `relevance-badge tier-${r.relevance}`, textContent: ` · ${r.relevance}` }) : "",
      r.verified_at ? el("span", { textContent: ` · verified ${fmtDate(r.verified_at)}` }) : "",
      r.status === "needs_verification" ? el("span", { className: "stale-badge", textContent: " · stale" }) : "",
    );
    if (r.tenant_id) meta.append(tenantBadge(r.tenant_id, r.tenant_name));
    const item = el("li", { className: `doc-item ${tierClass}`.trim() },
      el("h3", { textContent: r.doc_title }),
      meta,
    );
    colorByTenant(item, r.tenant_id);
    item.addEventListener("click", () => {
      navStack.push(() => renderSearchResults(results));
      showDocument(r.document_id);
    });
    list.append(item);
  }
  view.append(list);
}

// ── Browse (index) view ───────────────────────────────────────────────────────

async function renderBrowse() {
  navStack.length = 0; // root view — clear history
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  // A freshly-bootstrapped tenant with zero documents can yield JSON null here
  // (and older servers still do); coerce to [] so we never iterate a non-array.
  const tf = memFilter ? `&tenant_id=${encodeURIComponent(memFilter.id)}` : "";
  const entries = (await apiFetch(`/index?depth=summary${tf}`)) || [];
  view.replaceChildren();

  if (!entries.length) {
    view.append(el("p", {
      className: "state-msg",
      textContent: "No memories yet — import an archive from a tenant's page (Tenants tab), or add memories via your MCP client / the API.",
    }));
    return;
  }

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
      item.addEventListener("click", () => {
        navStack.push(renderBrowse);
        renderCategoryDocs(cat, row.subcategory || null);
      });
      sub.append(item);
    }

    // clicking the heading also drills into the whole category
    heading.addEventListener("click", () => {
      navStack.push(renderBrowse);
      renderCategoryDocs(cat, null);
    });
    section.append(heading, sub);
    view.append(section);
  }
}

async function renderCategoryDocs(category, subcategory) {
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  const params = new URLSearchParams({ category });
  if (subcategory) params.set("subcategory", subcategory);
  if (memFilter) params.set("tenant_id", memFilter.id);
  const docs = await apiFetch(`/documents?${params}`);
  view.replaceChildren();

  const hdr = el("div", { className: "cat-hdr" });
  hdr.append(backBtn());
  hdr.append(el("h2", { textContent: subcategory ? `${category} / ${subcategory}` : category }));
  view.append(hdr);

  if (!docs.length) {
    view.append(el("p", { className: "state-msg", textContent: "no documents" }));
    return;
  }
  const legend = legendFor(docs);
  if (legend) view.append(legend);
  const list = el("ul", { className: "doc-list" });
  for (const doc of docs) {
    const meta = [doc.subcategory, doc.slug, doc.doc_type].filter(Boolean).join(" · ");
    const metaRow = el("div", { className: "doc-meta", textContent: meta });
    if (doc.tenant_id) metaRow.append(tenantBadge(doc.tenant_id, doc.tenant_name));
    const item = el("li", { className: "doc-item" },
      el("h3", { textContent: doc.title || doc.slug }),
      metaRow,
    );
    colorByTenant(item, doc.tenant_id);
    item.addEventListener("click", () => {
      navStack.push(() => renderCategoryDocs(category, subcategory));
      showDocument(doc.id);
    });
    list.append(item);
  }
  view.append(list);
}

// ── Search wiring ─────────────────────────────────────────────────────────────

let _searchTimer = null;

async function runSearch(q) {
  navStack.length = 0; // root view — clear history
  view.replaceChildren(el("p", { className: "state-msg", textContent: "searching…" }));
  try {
    const tf = memFilter ? `&tenant_id=${encodeURIComponent(memFilter.id)}` : "";
    const results = await apiFetch(`/search?q=${encodeURIComponent(q)}&limit=20${tf}`);
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

// ── Memories tab (aggregated, color-coded, tenant-filterable) ─────────────────

// memFilter narrows the Memories view to a single tenant via the read APIs'
// ?tenant_id= filter. null = the aggregated view across all readable tenants.
// The per-tenant panel's "view memories" link sets it; the chip's ✕ clears it.
let memFilter = null;

// tenantColor derives a stable color from a tenant id so the same tenant always
// renders in the same hue across results, legend, chip and panel badge.
function tenantColor(id) {
  let h = 0;
  const s = String(id || "");
  for (let i = 0; i < s.length; i++) h = (h * 31 + s.charCodeAt(i)) >>> 0;
  return `hsl(${h % 360} 60% 42%)`;
}

// tenantBadge is a small pill showing a result's owning tenant, colored by id.
function tenantBadge(id, name) {
  const b = el("span", { className: "tenant-badge", textContent: name || id || "?" });
  const c = tenantColor(id);
  b.style.color = c;
  b.style.borderColor = c;
  return b;
}

// tenantLegend maps colors → tenant names; shown when results span >1 tenant.
function tenantLegend(items) {
  const wrap = el("div", { className: "tenant-legend" });
  for (const t of items) {
    const sw = el("span", { className: "tenant-swatch" });
    sw.style.background = tenantColor(t.id);
    wrap.append(el("span", { className: "legend-item" }, sw, document.createTextNode(t.name || t.id)));
  }
  return wrap;
}

// colorByTenant paints a result row's left edge with its tenant color (no-op for
// rows the read API didn't tag with a tenant id).
function colorByTenant(node, id) {
  if (id) node.style.borderLeft = `4px solid ${tenantColor(id)}`;
}

// legendFor collects the distinct tenants present in a result set and returns a
// legend node when more than one tenant is represented, else null.
function legendFor(rows) {
  const seen = new Map();
  for (const r of rows) if (r.tenant_id) seen.set(r.tenant_id, r.tenant_name || r.tenant_id);
  if (seen.size <= 1) return null;
  return tenantLegend([...seen].map(([id, name]) => ({ id, name })));
}

// renderMemFilterChip fills #mem-filter-chip with the "viewing: <tenant> ✕" chip
// when a tenant filter is active, or clears it when viewing the aggregate.
function renderMemFilterChip() {
  const c = document.getElementById("mem-filter-chip");
  if (!c) return;
  c.replaceChildren();
  if (!memFilter) return;
  const sw = el("span", { className: "tenant-swatch" });
  sw.style.background = tenantColor(memFilter.id);
  const x = el("button", { type: "button", className: "chip-x", textContent: "✕", title: "Clear tenant filter" });
  x.addEventListener("click", () => { memFilter = null; renderMemFilterChip(); renderMemories().catch(showError); });
  c.append(el("span", { className: "tenant-chip" }, sw, document.createTextNode(`viewing: ${memFilter.name || memFilter.id} `), x));
}

// renderMemories is the default view: the live search sits in the persistent
// #memsearch bar (shown by route only here); results render into #view as
// browse (empty query) or search (non-empty), both color-coded by tenant.
async function renderMemories() {
  navStack.length = 0;
  renderMemFilterChip();
  const q = document.getElementById("q");
  const term = q ? q.value.trim() : "";
  if (term) await runSearch(term);
  else await renderBrowse();
}

// slashFocus focuses the Memories search on "/" when Memories is active and the
// user isn't already typing in a field.
function slashFocus(e) {
  if (e.key !== "/") return;
  const h = location.hash;
  if (!(h === "" || h === "#memories")) return;
  const a = document.activeElement;
  if (a && (a.tagName === "INPUT" || a.tagName === "TEXTAREA" || a.isContentEditable)) return;
  const q = document.getElementById("q");
  if (q) { e.preventDefault(); q.focus(); }
}

// ── Error helper ──────────────────────────────────────────────────────────────

// showError surfaces a clean, human-readable banner to the user and keeps the
// technical detail in the console. Errors thrown by apiFetch carry an HTTP
// `status` and a server-provided message — those are safe and useful to show.
// Unexpected throws (e.g. a TypeError like "X is not iterable") have no status
// and are meaningless to a user, so we show a generic sentence instead of the
// raw exception string.
function showError(err) {
  console.error("memory UI error:", err);
  const msg = err && err.status
    ? `Couldn't load this view: ${err.message}`
    : "Something went wrong loading this view. Try reloading the page; if it persists, check the server logs.";
  view.replaceChildren(el("p", { className: "state-msg state-err", textContent: msg }));
}

// ── Admin ───────────────────────────────────────────────────────────────────
// Gated on GET /admin/whoami. The admin affordance is only ever created for
// admins: a 403 means "not an admin" and is swallowed (distinct from 401, which
// apiFetch already turns into a re-login). Every call goes through
// apiFetch("/admin/..."). Reachable via the header "Admin" button / "#admin" hash.

let isAdmin = false;

// checkAdmin probes /admin/whoami. Returns true (and mounts the header entry
// point) when the caller is an admin; returns false silently on 403 or any other
// failure, so non-admins never receive the admin UI.
async function checkAdmin() {
  try {
    const r = await apiFetch("/admin/whoami");
    if (!r || !r.admin) return false;
  } catch (err) {
    // 403 => not an admin: do not surface admin, do not re-login. apiFetch has
    // already handled 401 via re-login. Any other error: also stay silent.
    return false;
  }
  isAdmin = true;
  return true;
}

// writableTenants caches the last /tenants/writable result (design.md §9):
// empty for a caller with no delegated tenant, non-empty for a system admin
// (every tenant) or a tenant#manager (their managed tenants). route() uses its
// length to gate the Tenants tab / #tenants routes for non-admins the same way
// isAdmin gates the Admin tab.
let writableTenants = [];

// checkWritable probes GET /tenants/writable — not adminOnly, so every
// logged-in caller can reach it. Its result (empty for a plain user, non-empty
// for a system admin or a delegated manager) gates the Tenants tab and the
// #tenants routes the same way isAdmin gates the Admin tab.
async function checkWritable() {
  try {
    writableTenants = (await apiFetch("/tenants/writable")) || [];
  } catch (err) {
    writableTenants = [];
  }
}

// route renders the top-level view for the current hash and refreshes the tab
// bar's active state. Routes: #memories (default), #tenants, #tenants/<id>,
// #admin, #connect. The superseded #acl/#import routes silently redirect to
// #tenants. It is the single source of truth for view switching — registered
// once on hashchange (see init), so every tab and back control just sets the hash.
function route() {
  const h = location.hash;
  if (h === "#acl" || h === "#import") { location.hash = "tenants"; return; } // redirect superseded routes
  renderTabBar();
  const memsearch = document.getElementById("memsearch");
  const onMemories = h === "" || h === "#memories";
  if (memsearch) memsearch.hidden = !onMemories;
  if (h === "#admin") {
    (isAdmin ? renderAdmin() : renderMemories()).catch(showError);
  } else if (h === "#tenants") {
    (isAdmin || writableTenants.length ? renderTenants() : renderMemories()).catch(showError);
  } else if (h.startsWith("#tenants/")) {
    const id = decodeURIComponent(h.slice("#tenants/".length));
    (isAdmin || writableTenants.length ? renderTenantPanel(id) : renderMemories()).catch(showError);
  } else if (h === "#connect") {
    renderConnect();
  } else {
    renderMemories().catch(showError);
  }
}

// renderTabBar (re)builds the persistent centered tab bar into #tabbar and marks
// the active tab from the current hash. Visibility reuses the existing role
// signals: Memories always; Tenants when the caller is an admin or manages at
// least one tenant; Admin for system admins only. Connect is a corner link for
// everyone. Every tab just sets the hash — route() does the rendering.
function renderTabBar() {
  const bar = document.getElementById("tabbar");
  if (!bar) return;
  bar.replaceChildren();
  const h = location.hash;
  const tabs = [{ label: "Memories", hash: "memories", active: h === "" || h === "#memories" }];
  if (isAdmin || writableTenants.length) {
    tabs.push({ label: "Tenants", hash: "tenants", active: h === "#tenants" || h.startsWith("#tenants/") });
  }
  if (isAdmin) tabs.push({ label: "Admin", hash: "admin", active: h === "#admin" });
  for (const t of tabs) {
    const b = el("button", { className: "tab-link" + (t.active ? " active" : ""), type: "button", textContent: t.label });
    b.addEventListener("click", () => { location.hash = t.hash; });
    bar.append(b);
  }
  const connect = el("button", { className: "tab-link tab-connect" + (h === "#connect" ? " active" : ""), type: "button", textContent: "Connect" });
  connect.addEventListener("click", () => { location.hash = "connect"; });
  bar.append(connect);
}

// ── Connect an MCP client (Task 5 / D5) ───────────────────────────────────────
// A header entry point mounted for every logged-in user. It renders a static
// panel explaining how to point an MCP client at this server — the OAuth path
// (no token) and the static-key path. It never fetches or persists a secret.

// mcpURL returns this instance's MCP endpoint (<origin>/mcp), preferring the
// server-published `resource` from config.json (equals <base>/mcp) and falling
// back to the current origin when config has not been loaded.
function mcpURL() {
  return (CONFIG && CONFIG.resource) || (window.location.origin + "/mcp");
}

// mcpConfigJSON returns a generic `mcpServers` config block for an MCP client.
// Most coding agents (Claude Code, Cursor, Windsurf, …) share this shape; field
// names vary slightly (type/transport), but the URL — and, for key auth, the
// Authorization header — are the constants. withKey uses a placeholder, never a
// real secret.
function mcpConfigJSON(url, withKey) {
  const server = withKey
    ? { type: "http", url, headers: { Authorization: "Bearer <admin key>" } }
    : { type: "http", url };
  return JSON.stringify({ mcpServers: { memory: server } }, null, 2);
}

// renderConnect shows how to connect an MCP client: the server URL, the OAuth
// path, and the static API-key path. Values are copy-pasteable; nothing here
// reads or stores a secret.
function renderConnect() {
  navStack.length = 0; // root-ish view — clear history
  view.replaceChildren();

  const hdr = el("div", { className: "cat-hdr" });
  const back = el("button", { textContent: "←", className: "back-btn", title: "Back to browse", type: "button" });
  back.addEventListener("click", () => { location.hash = ""; });
  hdr.append(back, el("h2", { textContent: "Connect an MCP client" }));
  view.append(hdr);

  const url = mcpURL();
  const sec = el("section");
  sec.append(el("p", { className: "meta", textContent: "Point your MCP client at this server:" }));
  sec.append(el("code", { className: "modal-key", textContent: url }));

  sec.append(el("h3", { textContent: "With OAuth (recommended)" }));
  sec.append(el("p", {
    className: "meta",
    textContent: "Clients that support OAuth need only the URL — they complete the login flow themselves, with no static token to manage. Field names vary slightly by client (type/transport).",
  }));
  sec.append(el("pre", { className: "code-block", textContent: mcpConfigJSON(url, false) }));

  sec.append(el("h3", { textContent: "With an API key" }));
  const keyPara = isAdmin
    ? el("p", { className: "meta" },
        "For clients (or CI) that don't do OAuth, use a static Bearer key. Issue one from ",
        el("a", { href: "#admin", textContent: "Admin" }),
        " → a tenant → \"Issue key\", then drop it into the ", el("code", { textContent: "Authorization" }), " header:")
    : el("p", { className: "meta" },
        "For clients (or CI) that don't do OAuth, use a static Bearer key (ask an admin to issue one) in the ",
        el("code", { textContent: "Authorization" }), " header:");
  sec.append(keyPara);
  sec.append(el("pre", { className: "code-block", textContent: mcpConfigJSON(url, true) }));

  view.append(sec);
}

// ── Tenants tab ───────────────────────────────────────────────────────────────
// The Tenants tab lists the tenants the caller may manage, split into Shared and
// Personal sub-tabs (GET /api/tenants?type=), with a client-side live filter and
// an admin-only create affordance. Selecting a tenant opens its type-aware panel
// (#tenants/<id>). This replaces the old standalone ACL and Import pages.

let tenantsType = "shared"; // active sub-tab; Shared is the default
const tenantCache = {}; // id → {id,name,type,relation}, populated as lists load

async function renderTenants() {
  navStack.length = 0;
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  let rows;
  try {
    rows = (await apiFetch(`/tenants?type=${encodeURIComponent(tenantsType)}`)) || [];
  } catch (err) { showError(err); return; }
  for (const t of rows) tenantCache[t.id] = t;
  view.replaceChildren();

  view.append(el("div", { className: "cat-hdr" }, el("h2", { textContent: "Tenants" })));

  const subtabs = el("div", { id: "tenant-subtabs" });
  for (const ty of ["shared", "personal"]) {
    const b = el("button", { className: "tab", type: "button", textContent: ty[0].toUpperCase() + ty.slice(1) });
    b.setAttribute("aria-selected", ty === tenantsType ? "true" : "false");
    b.addEventListener("click", () => { if (ty !== tenantsType) { tenantsType = ty; renderTenants().catch(showError); } });
    subtabs.append(b);
  }
  view.append(subtabs);

  if (isAdmin) view.append(newTenantTypeForm());

  const filter = el("input", { className: "admin-input tenant-filter", type: "text", placeholder: "filter by name or UUID…", autocomplete: "off" });
  view.append(el("div", { className: "admin-form-fields" }, filter));

  const list = el("ul", { className: "doc-list" });
  view.append(list);

  // draw filters the already-fetched rows live (name substring, case-insensitive,
  // or UUID substring) — no submit button, no re-fetch per keystroke.
  function draw() {
    const f = filter.value.trim().toLowerCase();
    list.replaceChildren();
    const shown = rows.filter((t) =>
      !f || (t.name || "").toLowerCase().includes(f) || (t.id || "").toLowerCase().includes(f));
    if (!shown.length) {
      list.append(el("li", { className: "state-msg", textContent: tenantsType === "personal" ? "No personal tenants." : "No tenants." }));
      return;
    }
    for (const t of shown) {
      const sw = el("span", { className: "tenant-swatch" });
      sw.style.background = tenantColor(t.id);
      const item = el("li", { className: "doc-item" },
        el("h3", {}, sw, document.createTextNode(t.name || "(unnamed)")),
        el("div", { className: "doc-meta", textContent: [t.type, t.relation, t.id].filter(Boolean).join(" · ") }),
      );
      item.addEventListener("click", () => { location.hash = "tenants/" + t.id; });
      list.append(item);
    }
  }
  filter.addEventListener("input", draw);
  draw();
}

// newTenantTypeForm — admin-only create affordance on the Tenants tab (name +
// type → POST /api/admin/tenants). Distinct from the Admin tab's create form,
// which also collects an owner email.
function newTenantTypeForm() {
  const wrap = el("div", { className: "admin-form" });
  const toggle = el("button", { className: "sec-btn", type: "button", textContent: "+ New tenant" });
  const form = el("form", { className: "admin-form-fields" });
  form.hidden = true;
  const name = el("input", { className: "admin-input", type: "text", placeholder: "name", required: true });
  const type = el("select", { className: "admin-input" },
    el("option", { value: "shared", textContent: "shared" }),
    el("option", { value: "personal", textContent: "personal" }),
  );
  const submit = el("button", { className: "sec-btn sec-btn-primary", type: "submit", textContent: "Create" });
  form.append(name, type, submit);
  toggle.addEventListener("click", () => { form.hidden = !form.hidden; if (!form.hidden) name.focus(); });
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    submit.disabled = true;
    try {
      await apiFetch("/admin/tenants", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name.value.trim(), type: type.value }),
      });
      renderTenants();
    } catch (err) {
      submit.disabled = false;
      alert("Create tenant failed: " + err.message);
    }
  });
  wrap.append(toggle, form);
  return wrap;
}

// lookupTenant resolves a tenant by id — from the cache first, else by fetching
// both type lists (a manager sees only their managed shared tenants; an admin
// sees all of each type) and searching them. Returns null if not manageable.
async function lookupTenant(id) {
  if (tenantCache[id]) return tenantCache[id];
  const lists = await Promise.all(
    ["shared", "personal"].map((ty) => apiFetch(`/tenants?type=${ty}`).catch(() => [])),
  );
  for (const list of lists) for (const t of (list || [])) tenantCache[t.id] = t;
  return tenantCache[id] || null;
}

// renderTenantPanel draws the type-aware per-tenant panel for #tenants/<id>. All
// sections target the route tenant (never a picker). Shared tenants get
// Members/ACL + per-doc sharing + Import; personal tenants get API keys + Import
// (keys are refused for shared tenants; personal tenants have a single owner and
// no members). Both get a "view this tenant's memories" link. No document browser.
async function renderTenantPanel(id) {
  navStack.length = 0;
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  let t;
  try { t = await lookupTenant(id); } catch (err) { showError(err); return; }
  if (!t) {
    view.replaceChildren(el("p", { className: "state-msg state-err", textContent: "Tenant not found or not manageable." }));
    return;
  }
  view.replaceChildren();

  const hdr = el("div", { className: "doc-hdr" });
  const back = el("button", { className: "back-btn", type: "button", textContent: "←", title: "Back to tenants" });
  back.addEventListener("click", () => { location.hash = "tenants"; });
  hdr.append(back, el("h1", { textContent: t.name || "(unnamed)" }), tenantBadge(t.id, t.type));
  view.append(hdr);
  view.append(el("div", { className: "meta", textContent: [t.type, t.relation, t.id].filter(Boolean).join(" · ") }));

  const viewMem = el("button", { className: "sec-btn", type: "button", textContent: "View this tenant's memories" });
  viewMem.addEventListener("click", () => { memFilter = { id: t.id, name: t.name, type: t.type }; location.hash = "memories"; });
  view.append(el("div", { className: "admin-form-fields" }, viewMem));

  if (t.type === "personal") {
    // Personal: API keys + Import. No member management (single-owner tenant).
    let keys;
    try { keys = (await apiFetch(`/admin/tenants/${id}/keys`)) || []; }
    catch (err) { view.append(el("p", { className: "state-msg state-err", textContent: "Failed to load keys: " + err.message })); }
    if (keys) view.append(keysSection(t, keys, () => renderTenantPanel(id)));
    view.append(importSection(id));
  } else {
    // Shared: Members/ACL + per-doc guest sharing + Import. No API keys (refused
    // for shared tenants by the backend).
    view.append(tenantMembersSection(id));
    view.append(aclDocumentSection());
    view.append(importSection(id));
  }
}

// tenantMembersSection — the tenant-membership grants (viewer/member/manager)
// for a fixed tenant id, extracted from the old renderAcl so it renders bound to
// the route tenant with no dropdown. Same /acl/tenants/{id}/grants endpoints.
function tenantMembersSection(tenantID) {
  const sec = el("section", { className: "admin-section" });
  sec.append(el("h2", { textContent: "Members" }));
  const body = el("div");
  sec.append(body);
  async function load() {
    body.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
    let grants;
    try {
      grants = await apiFetch(`/acl/tenants/${tenantID}/grants`);
    } catch (err) {
      body.replaceChildren(el("p", { className: "state-msg state-err", textContent: "Failed to load grants: " + err.message }));
      return;
    }
    body.replaceChildren();
    body.append(tenantGrantsTable(tenantID, grants || [], load));
    body.append(tenantGrantForm(tenantID, load));
  }
  load().catch(showError);
  return sec;
}

// importSection — the upload/poll import flow with the target tenant fixed to a
// passed id (extracted from the old renderImport; no tenant picker). Same
// /import + /import/{id} endpoints.
function importSection(tenantID) {
  const sec = el("section", { className: "admin-section" });
  sec.append(el("h2", { textContent: "Import" }));
  sec.append(el("p", {
    className: "meta",
    textContent: "Upload a .zip archive of memory files. Files must sit at the archive's " +
      "ROOT (category/subcategory/slug.md or category/slug.md) — a wrapping top-level " +
      "directory makes every path parse as misc/<path> instead of its real category.",
  }));

  const form = el("form", { className: "admin-form-fields" });
  const fileInput = el("input", { className: "admin-input", type: "file", accept: ".zip", required: true });
  const submit = el("button", { textContent: "Upload", className: "sec-btn sec-btn-primary", type: "submit" });
  form.append(fileInput, submit);

  const progress = el("div", { className: "import-progress" });
  let activeTimer = null; // guards against overlapping polls if a second file is uploaded before the first finishes

  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    const file = fileInput.files[0];
    if (!file) return;
    if (activeTimer) { clearInterval(activeTimer); activeTimer = null; }
    submit.disabled = true;
    progress.replaceChildren();
    try {
      const body = new FormData();
      body.append("archive", file);
      body.append("tenant_id", tenantID);
      // No Content-Type header: the browser sets the multipart boundary itself.
      const job = await apiFetch("/import", { method: "POST", body });
      fileInput.value = "";
      renderImportProgress(progress, job);
      activeTimer = pollImportJob(progress, job.id, job.tenant_id || tenantID, () => { activeTimer = null; });
    } catch (err) {
      progress.replaceChildren(el("p", { className: "state-msg state-err", textContent: "Upload failed: " + err.message }));
    } finally {
      submit.disabled = false;
    }
  });

  sec.append(form, progress);
  return sec;
}

// renderAdmin — admin root: list tenants + create-tenant form.
async function renderAdmin() {
  navStack.length = 0; // admin root — clear browse/search history
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  let tenants;
  try {
    tenants = await apiFetch("/admin/tenants");
  } catch (err) { showError(err); return; }
  view.replaceChildren();

  const hdr = el("div", { className: "cat-hdr" });
  const browseBtn = el("button", { textContent: "←", className: "back-btn", title: "Back to browse", type: "button" });
  browseBtn.addEventListener("click", () => { location.hash = ""; });
  hdr.append(browseBtn, el("h2", { textContent: "Admin · Tenants" }));
  view.append(hdr);

  view.append(newTenantForm());

  if (!tenants || !tenants.length) {
    view.append(el("p", { className: "state-msg", textContent: "no tenants" }));
    return;
  }
  const list = el("ul", { className: "doc-list" });
  for (const t of tenants) {
    const meta = [t.email, t.staleness_mode ? `staleness: ${t.staleness_mode}` : "", t.id].filter(Boolean).join(" · ");
    const item = el("li", { className: "doc-item" },
      el("h3", { textContent: t.name || "(unnamed)" }),
      el("div", { className: "doc-meta", textContent: meta }),
    );
    item.addEventListener("click", () => { navStack.push(renderAdmin); showTenant(t); });
    list.append(item);
  }
  view.append(list);
}

// ── ACL grant helpers (shared by the per-tenant panel) ───────────────────────

// canGrantManager gates the "manager" option in a tenant grant-relation
// <select> (design.md §6 ceiling: appointing a manager requires tenant#admin
// or system admin). /tenants/writable cannot tell a real tenant#admin apart
// from a plain tenant#manager for a non-system-admin caller — WritableTenants
// labels both relations "manager" — so the UI conservatively offers this
// option only to system admins. A genuine tenant#admin can still grant it
// directly against the API; the backend enforces the real ceiling regardless
// of what this UI offers.
function canGrantManager() {
  return isAdmin;
}

// ── Import job progress (shared by importSection) ─────────────────────────────
// Upload a .zip archive -> POST /import (multipart: "archive" + "tenant_id") ->
// 202 {id, status, tenant_id}; poll GET /import/{id}?tenant_id=<t> until a
// terminal state (succeeded/failed), rendering the running counts.

const IMPORT_POLL_MS = 1500;

// renderImportProgress renders one job's state + counts into `container`,
// replacing whatever was there before — each poll tick calls this fresh.
function renderImportProgress(container, job) {
  container.replaceChildren();
  const badge = el("span", {
    className: `import-status-badge import-status-${job.status}`,
    textContent: job.status,
  });
  container.append(el("p", {}, "Job ", el("code", { textContent: job.id }), " — ", badge));
  container.append(el("div", { className: "import-counts" },
    el("span", { textContent: `total ${job.total ?? 0}` }),
    el("span", { textContent: `imported ${job.imported ?? 0}` }),
    el("span", { textContent: `skipped ${job.skipped ?? 0}` }),
    el("span", { textContent: `failed ${job.failed ?? 0}` }),
  ));
  if (job.status === "failed" && job.error) {
    container.append(el("p", { className: "state-msg state-err", textContent: job.error }));
  }
}

// pollImportJob polls GET /import/{id}?tenant_id=<t> on an interval until
// the job reaches a terminal state (succeeded/failed), then stops polling and
// calls onDone. tenantID scopes the lookup to the tenant the job was targeted at
// (an admin may import into a tenant other than their key's own). Returns the
// interval id so the caller can cancel it early (e.g. a new upload superseding
// this one).
function pollImportJob(container, id, tenantID, onDone) {
  const timer = setInterval(async () => {
    let job;
    try {
      const q = tenantID ? "?tenant_id=" + encodeURIComponent(tenantID) : "";
      job = await apiFetch("/import/" + id + q);
    } catch (err) {
      clearInterval(timer);
      onDone();
      container.append(el("p", { className: "state-msg state-err", textContent: "Status check failed: " + err.message }));
      return;
    }
    renderImportProgress(container, job);
    if (job.status === "succeeded" || job.status === "failed") {
      clearInterval(timer);
      onDone();
    }
  }, IMPORT_POLL_MS);
  return timer;
}

// ── Tenant membership grants (used by tenantMembersSection) ───────────────────

function tenantGrantsTable(tenantID, grants, onRevoked) {
  const table = el("table", { className: "admin-table" });
  const thead = el("thead", {}, el("tr", {},
    el("th", { textContent: "Email" }),
    el("th", { textContent: "Relation" }),
    el("th", { textContent: "" }),
  ));
  const tbody = el("tbody");
  for (const g of grants) tbody.append(tenantGrantRow(tenantID, g, onRevoked));
  table.append(thead, tbody);
  return grants.length ? wrapScroll(table) : el("p", { className: "meta", textContent: "no grants" });
}

function tenantGrantRow(tenantID, g, onRevoked) {
  const tr = el("tr");
  const revoke = el("button", { textContent: "Revoke", className: "sec-btn sec-btn-danger", type: "button" });
  revoke.addEventListener("click", async () => {
    if (!confirm(`Revoke ${g.relation} for ${g.email}?`)) return;
    revoke.disabled = true;
    try {
      await apiFetch(`/acl/tenants/${tenantID}/grants`, {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email: g.email, relation: g.relation }),
      });
      await onRevoked();
    } catch (err) {
      revoke.disabled = false;
      alert("Revoke failed: " + err.message);
    }
  });
  tr.append(
    el("td", { textContent: g.email || "" }),
    el("td", { textContent: g.relation || "" }),
    el("td", { className: "admin-actions" }, revoke),
  );
  return tr;
}

// tenantGrantForm — grant viewer/member/manager on tenantID. The "manager"
// option is only offered to system admins (canGrantManager) — the grant-
// ceiling matrix (design.md §6) forbids a plain manager from appointing
// another manager, and the backend would 403 the attempt anyway.
function tenantGrantForm(tenantID, onGranted) {
  const form = el("form", { className: "admin-form-fields" });
  const email = el("input", { className: "admin-input", type: "email", placeholder: "email", required: true });
  const relation = el("select", { className: "admin-input" },
    el("option", { value: "viewer", textContent: "viewer" }),
    el("option", { value: "member", textContent: "member" }),
  );
  if (canGrantManager()) relation.append(el("option", { value: "manager", textContent: "manager" }));
  const submit = el("button", { textContent: "Grant", className: "sec-btn sec-btn-primary", type: "submit" });
  form.append(el("span", { className: "admin-form-label", textContent: "Grant:" }), email, relation, submit);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    submit.disabled = true;
    try {
      await apiFetch(`/acl/tenants/${tenantID}/grants`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email: email.value.trim(), relation: relation.value }),
      });
      email.value = "";
      await onGranted();
    } catch (err) {
      alert("Grant failed: " + err.message);
    } finally {
      submit.disabled = false;
    }
  });
  return form;
}

// aclDocumentSection renders the per-document guest-sharing section of the
// shared-tenant panel (design.md §9): a document-id input, the doc's current
// guest viewer/editor grants (GET /acl/documents/{id}/grants), and a "share
// with user, read or write" form.
//
// Document-selection UX choice: an id input rather than a list picker. There is
// no cross-tenant document-list endpoint; GET /documents/{id} resolves against
// the caller's readable tenants. A plain id input (paste the UUID from the doc's
// URL or the API) is the simplest picker that doesn't imply a capability the
// backend doesn't have.
function aclDocumentSection() {
  const sec = el("section", { className: "admin-section" });
  sec.append(el("h2", { textContent: "Document sharing" }));
  sec.append(el("p", {
    className: "meta",
    textContent: "Share one document with a specific user, read or write — independent of tenant membership. Enter the document's id (from its URL or the API); lookup is scoped to your own tenant.",
  }));

  const idInput = el("input", { className: "admin-input", type: "text", placeholder: "document id" });
  const loadBtn = el("button", { textContent: "Load", className: "sec-btn", type: "button" });
  sec.append(el("div", { className: "admin-form-fields" },
    el("span", { className: "admin-form-label", textContent: "Document:" }), idInput, loadBtn,
  ));

  const body = el("div");
  sec.append(body);

  async function load() {
    const docID = idInput.value.trim();
    if (!docID) return;
    body.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
    let doc, grants;
    try {
      [doc, grants] = await Promise.all([
        apiFetch(`/documents/${docID}`),
        apiFetch(`/acl/documents/${docID}/grants`),
      ]);
    } catch (err) {
      body.replaceChildren(el("p", { className: "state-msg state-err", textContent: "Failed to load document: " + err.message }));
      return;
    }
    body.replaceChildren();
    const path = [doc.category, doc.subcategory, doc.slug].filter(Boolean).join("/");
    body.append(el("div", { className: "meta", textContent: `${doc.title || doc.slug}  ·  ${path}` }));
    body.append(documentGrantsTable(docID, grants || [], load));
    body.append(documentGrantForm(docID, load));
  }

  loadBtn.addEventListener("click", () => load().catch(showError));
  idInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter") { e.preventDefault(); load().catch(showError); }
  });

  return sec;
}

function documentGrantsTable(docID, grants, onRevoked) {
  const table = el("table", { className: "admin-table" });
  const thead = el("thead", {}, el("tr", {},
    el("th", { textContent: "Email" }),
    el("th", { textContent: "Relation" }),
    el("th", { textContent: "" }),
  ));
  const tbody = el("tbody");
  for (const g of grants) tbody.append(documentGrantRow(docID, g, onRevoked));
  table.append(thead, tbody);
  return grants.length ? wrapScroll(table) : el("p", { className: "meta", textContent: "no guest grants" });
}

function documentGrantRow(docID, g, onRevoked) {
  const tr = el("tr");
  const revoke = el("button", { textContent: "Revoke", className: "sec-btn sec-btn-danger", type: "button" });
  revoke.addEventListener("click", async () => {
    if (!confirm(`Revoke ${g.relation} for ${g.email}?`)) return;
    revoke.disabled = true;
    try {
      await apiFetch(`/acl/documents/${docID}/grants`, {
        method: "DELETE",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email: g.email, relation: g.relation }),
      });
      await onRevoked();
    } catch (err) {
      revoke.disabled = false;
      alert("Revoke failed: " + err.message);
    }
  });
  tr.append(
    el("td", { textContent: g.email || "" }),
    el("td", { textContent: g.relation || "" }),
    el("td", { className: "admin-actions" }, revoke),
  );
  return tr;
}

function documentGrantForm(docID, onGranted) {
  const form = el("form", { className: "admin-form-fields" });
  const email = el("input", { className: "admin-input", type: "email", placeholder: "email", required: true });
  const relation = el("select", { className: "admin-input" },
    el("option", { value: "viewer", textContent: "read (viewer)" }),
    el("option", { value: "editor", textContent: "write (editor)" }),
  );
  const submit = el("button", { textContent: "Share", className: "sec-btn sec-btn-primary", type: "submit" });
  form.append(el("span", { className: "admin-form-label", textContent: "Share with:" }), email, relation, submit);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    submit.disabled = true;
    try {
      await apiFetch(`/acl/documents/${docID}/grants`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email: email.value.trim(), relation: relation.value }),
      });
      email.value = "";
      await onGranted();
    } catch (err) {
      alert("Share failed: " + err.message);
    } finally {
      submit.disabled = false;
    }
  });
  return form;
}

// newTenantForm — collapsed create-tenant form (name + email).
function newTenantForm() {
  const wrap = el("div", { className: "admin-form" });
  const toggle = el("button", { textContent: "+ New tenant", className: "sec-btn", type: "button" });
  const form = el("form", { className: "admin-form-fields" });
  form.hidden = true;
  const name = el("input", { className: "admin-input", type: "text", placeholder: "name", required: true });
  const email = el("input", { className: "admin-input", type: "email", placeholder: "email", required: true });
  const submit = el("button", { textContent: "Create", className: "sec-btn sec-btn-primary", type: "submit" });
  form.append(name, email, submit);
  toggle.addEventListener("click", () => { form.hidden = !form.hidden; if (!form.hidden) name.focus(); });
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    submit.disabled = true;
    try {
      await apiFetch("/admin/tenants", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ name: name.value.trim(), email: email.value.trim() }),
      });
      renderAdmin();
    } catch (err) {
      submit.disabled = false;
      alert("Create tenant failed: " + err.message);
    }
  });
  wrap.append(toggle, form);
  return wrap;
}

// showTenant — tenant detail: users + keys tables with their management forms.
async function showTenant(t) {
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  let users, keys;
  try {
    [users, keys] = await Promise.all([
      apiFetch(`/admin/tenants/${t.id}/users`),
      apiFetch(`/admin/tenants/${t.id}/keys`),
    ]);
  } catch (err) { showError(err); return; }
  view.replaceChildren();

  const hdr = el("div", { className: "doc-hdr" });
  hdr.append(backBtn(), el("h1", { textContent: t.name || "(unnamed)" }));
  view.append(hdr);
  view.append(el("div", { className: "meta", textContent: `${t.email || ""} · staleness: ${t.staleness_mode || "?"} · ${t.id}` }));

  view.append(usersSection(t, users || []));
  view.append(keysSection(t, keys || []));
}

// wrapScroll — horizontal-scroll container so wide tables never scroll the page.
function wrapScroll(node) {
  return el("div", { className: "admin-table-wrap" }, node);
}

function usersSection(t, users) {
  const sec = el("section", { className: "admin-section" });
  sec.append(el("h2", { textContent: "Users" }));

  const table = el("table", { className: "admin-table" });
  const thead = el("thead", {}, el("tr", {},
    el("th", { textContent: "Email" }),
    el("th", { textContent: "Role" }),
    el("th", { textContent: "" }),
  ));
  const tbody = el("tbody");
  for (const u of users) tbody.append(userRow(t, u));
  table.append(thead, tbody);
  sec.append(users.length ? wrapScroll(table) : el("p", { className: "meta", textContent: "no users" }));

  const form = el("form", { className: "admin-form-fields" });
  const email = el("input", { className: "admin-input", type: "email", placeholder: "email", required: true });
  const role = el("select", { className: "admin-input" },
    el("option", { value: "member", textContent: "member" }),
    el("option", { value: "admin", textContent: "admin" }),
  );
  const submit = el("button", { textContent: "Grant", className: "sec-btn sec-btn-primary", type: "submit" });
  form.append(el("span", { className: "admin-form-label", textContent: "Grant user:" }), email, role, submit);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    submit.disabled = true;
    try {
      await apiFetch("/admin/users", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email: email.value.trim(), tenant_id: t.id, role: role.value }),
      });
      showTenant(t);
    } catch (err) {
      submit.disabled = false;
      alert("Grant failed: " + err.message);
    }
  });
  sec.append(form);
  return sec;
}

function userRow(t, u) {
  const tr = el("tr");

  const roleSel = el("select", { className: "admin-input admin-role-select" },
    el("option", { value: "member", textContent: "member" }),
    el("option", { value: "admin", textContent: "admin" }),
  );
  roleSel.value = u.role || "member";
  roleSel.addEventListener("change", async () => {
    const prev = u.role || "member";
    roleSel.disabled = true;
    try {
      await apiFetch("/admin/users", {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email: u.email, role: roleSel.value }),
      });
      u.role = roleSel.value;
    } catch (err) {
      roleSel.value = prev;
      alert("Set role failed: " + err.message);
    } finally {
      roleSel.disabled = false;
    }
  });

  const revoke = el("button", { textContent: "Revoke", className: "sec-btn sec-btn-danger", type: "button" });
  revoke.addEventListener("click", async () => {
    if (!confirm(`Revoke access for ${u.email}?`)) return;
    revoke.disabled = true;
    try {
      await apiFetch(`/admin/users?email=${encodeURIComponent(u.email)}`, { method: "DELETE" });
      tr.remove();
    } catch (err) {
      revoke.disabled = false;
      alert("Revoke failed: " + err.message);
    }
  });

  tr.append(
    el("td", { textContent: u.email || "" }),
    el("td", {}, roleSel),
    el("td", { className: "admin-actions" }, revoke),
  );
  return tr;
}

function keysSection(t, keys, refresh) {
  refresh = refresh || (() => showTenant(t)); // Admin tab re-renders the tenant; the panel re-renders itself
  const sec = el("section", { className: "admin-section" });
  sec.append(el("h2", { textContent: "API keys" }));

  const table = el("table", { className: "admin-table" });
  const thead = el("thead", {}, el("tr", {},
    el("th", { textContent: "Label" }),
    el("th", { textContent: "Prefix" }),
    el("th", { textContent: "Created" }),
    el("th", { textContent: "Last used" }),
    el("th", { textContent: "Expires" }),
    el("th", { textContent: "Status" }),
    el("th", { textContent: "" }),
  ));
  const tbody = el("tbody");
  for (const k of keys) tbody.append(keyRow(t, k, refresh));
  table.append(thead, tbody);
  sec.append(keys.length ? wrapScroll(table) : el("p", { className: "meta", textContent: "no keys" }));

  const form = el("form", { className: "admin-form-fields" });
  const label = el("input", { className: "admin-input", type: "text", placeholder: "label", required: true });
  const ttl = el("input", { className: "admin-input admin-input-num", type: "number", min: "1", placeholder: "TTL days (optional)" });
  const submit = el("button", { textContent: "Issue key", className: "sec-btn sec-btn-primary", type: "submit" });
  form.append(el("span", { className: "admin-form-label", textContent: "Issue key:" }), label, ttl, submit);
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    submit.disabled = true;
    const body = { label: label.value.trim() };
    if (ttl.value) body.expires_in_days = parseInt(ttl.value, 10);
    try {
      const result = await apiFetch(`/admin/tenants/${t.id}/keys`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      showKeyModal(result); // plaintext shown once; modal outlives the view refresh below
      refresh();
    } catch (err) {
      submit.disabled = false;
      alert("Issue key failed: " + err.message);
    }
  });
  sec.append(form);
  return sec;
}

function keyRow(t, k, refresh) {
  refresh = refresh || (() => showTenant(t));
  const tr = el("tr", { className: k.revoked_at ? "admin-key-revoked" : "" });
  const revoked = !!k.revoked_at;
  const status = revoked ? `revoked ${fmtDate(k.revoked_at)}` : "active";

  const rotate = el("button", { textContent: "Rotate", className: "sec-btn", type: "button" });
  rotate.addEventListener("click", async () => {
    const g = prompt("Rotate key. Optional grace period in hours (blank = none):", "");
    if (g === null) return; // cancelled
    const body = {};
    const gh = parseInt(g, 10);
    if (g.trim() !== "" && !Number.isNaN(gh)) body.grace_hours = gh;
    rotate.disabled = true;
    try {
      const result = await apiFetch(`/admin/keys/${k.id}/rotate`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      showKeyModal(result);
      refresh();
    } catch (err) {
      rotate.disabled = false;
      alert("Rotate failed: " + err.message);
    }
  });

  const revoke = el("button", { textContent: "Revoke", className: "sec-btn sec-btn-danger", type: "button" });
  revoke.addEventListener("click", async () => {
    if (!confirm(`Revoke key "${k.label || ""}" (${k.prefix || ""})?`)) return;
    revoke.disabled = true;
    try {
      await apiFetch(`/admin/keys/${k.id}`, { method: "DELETE" });
      refresh();
    } catch (err) {
      revoke.disabled = false;
      alert("Revoke key failed: " + err.message);
    }
  });

  const actions = el("td", { className: "admin-actions" });
  if (!revoked) actions.append(rotate, revoke);

  tr.append(
    el("td", { textContent: k.label || "" }),
    el("td", { textContent: k.prefix || "" }),
    el("td", { textContent: fmtDate(k.created_at) || "—" }),
    el("td", { textContent: fmtDate(k.last_used_at) || "—" }),
    el("td", { textContent: fmtDate(k.expires_at) || "—" }),
    el("td", { textContent: status }),
    actions,
  );
  return tr;
}

// showKeyModal displays a freshly minted plaintext key exactly once. The secret
// lives only in this closure variable and the visible field — it is never written
// to sessionStorage/localStorage or a data-* attribute, and it becomes
// unreachable when the overlay is removed on close. Backdrop clicks do NOT close
// the modal, so the key can't be dismissed accidentally before it's copied.
function showKeyModal(result) {
  const key = result && result.key;
  if (!key) return;

  const overlay = el("div", { className: "modal-overlay" });
  const modal = el("div", { className: "modal" });

  modal.append(el("h2", { className: "modal-title", textContent: "API key created" }));
  modal.append(el("p", { className: "modal-warn", textContent: "Shown only once — copy and store it now. You will not be able to see it again." }));

  const meta = [result.label, result.prefix, result.expires_at ? `expires ${fmtDate(result.expires_at)}` : ""].filter(Boolean).join(" · ");
  if (meta) modal.append(el("div", { className: "meta", textContent: meta }));

  modal.append(el("code", { className: "modal-key", textContent: key }));

  const row = el("div", { className: "modal-actions" });
  const copyBtn = el("button", { textContent: "Copy", className: "sec-btn sec-btn-primary", type: "button" });
  copyBtn.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(key);
      copyBtn.textContent = "Copied!";
      setTimeout(() => { copyBtn.textContent = "Copy"; }, 1500);
    } catch (err) {
      alert("Copy failed — select the key above and copy it manually.");
    }
  });
  const closeBtn = el("button", { textContent: "Done", className: "sec-btn", type: "button" });
  closeBtn.addEventListener("click", () => overlay.remove());
  row.append(copyBtn, closeBtn);
  modal.append(row);

  overlay.append(modal);
  document.body.append(overlay);
  copyBtn.focus();
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
    await checkAdmin();
    await checkWritable();
    renderTabBar();
    // Single hash router for all top-level views; also renders the initial view.
    window.addEventListener("hashchange", route);
    document.addEventListener("keydown", slashFocus);
    route();
  } catch (err) {
    // beginLogin() throws "redirecting to login" — that's expected; ignore it.
    if (err.message !== "redirecting to login") showError(err);
  }
}());
