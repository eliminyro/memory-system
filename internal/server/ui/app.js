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
  const list = el("ul", { className: "doc-list" });
  for (const doc of docs) {
    const meta = [doc.subcategory, doc.slug, doc.doc_type].filter(Boolean).join(" · ");
    const item = el("li", { className: "doc-item" },
      el("h3", { textContent: doc.title || doc.slug }),
      el("div", { className: "doc-meta", textContent: meta }),
    );
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
  mountAdminEntry();
  return true;
}

// mountAdminEntry adds an "Admin" button to the header and wires hash routing.
// The element is created only for admins, so non-admins never receive it.
function mountAdminEntry() {
  const header = document.querySelector("header");
  if (!header || document.getElementById("admin-link")) return;
  const link = el("button", { id: "admin-link", className: "admin-link", textContent: "Admin", type: "button" });
  link.addEventListener("click", () => { location.hash = "admin"; });
  header.append(link);

  window.addEventListener("hashchange", () => {
    if (!isAdmin) return;
    if (location.hash === "#admin") renderAdmin().catch(showError);
    else renderBrowse().catch(showError);
  });
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

  view.append(importSection());
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

// ── Import (Task 8.2) ─────────────────────────────────────────────────────────
// Upload a .zip archive -> POST /admin/import (multipart, field "archive") ->
// 202 {id, status}; poll GET /admin/import/{id} on an interval until a
// terminal state (succeeded/failed), rendering the running counts. Import
// always targets the caller's own tenant (resolved server-side from the
// bearer token via auth.TenantIDFromContext) — there is no tenant picker —
// so this panel lives on the admin root page rather than under a specific
// tenant's detail view.

const IMPORT_POLL_MS = 1500;

// importSection builds the upload form + progress area shown on the admin root.
function importSection() {
  const sec = el("section", { className: "admin-section" });
  sec.append(el("h2", { textContent: "Import documents" }));
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
  let activeTimer = null; // guards against overlapping polls if a second file is uploaded before the first job finishes

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
      // No Content-Type header here on purpose: the browser sets the
      // multipart boundary itself. apiFetch only adds Authorization.
      const job = await apiFetch("/admin/import", { method: "POST", body });
      fileInput.value = "";
      renderImportProgress(progress, job);
      activeTimer = pollImportJob(progress, job.id, () => { activeTimer = null; });
    } catch (err) {
      progress.replaceChildren(el("p", { className: "state-msg state-err", textContent: "Upload failed: " + err.message }));
    } finally {
      submit.disabled = false;
    }
  });

  sec.append(form, progress);
  return sec;
}

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

// pollImportJob polls GET /admin/import/{id} on an interval until the job
// reaches a terminal state (succeeded/failed), then stops polling and calls
// onDone. Returns the interval id so the caller can cancel it early (e.g. a
// new upload superseding this one).
function pollImportJob(container, id, onDone) {
  const timer = setInterval(async () => {
    let job;
    try {
      job = await apiFetch("/admin/import/" + id);
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

function keysSection(t, keys) {
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
  for (const k of keys) tbody.append(keyRow(t, k));
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
      showTenant(t);
    } catch (err) {
      submit.disabled = false;
      alert("Issue key failed: " + err.message);
    }
  });
  sec.append(form);
  return sec;
}

function keyRow(t, k) {
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
      showTenant(t);
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
      showTenant(t);
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
    const admin = await checkAdmin();
    if (admin && location.hash === "#admin") {
      await renderAdmin();
    } else {
      await renderBrowse();
    }
  } catch (err) {
    // beginLogin() throws "redirecting to login" — that's expected; ignore it.
    if (err.message !== "redirecting to login") showError(err);
  }
}());
