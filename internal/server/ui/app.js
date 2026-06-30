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
  const b = el("button", { textContent: "← back", className: "back-btn" });
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

// Build a single section container. The read view renders frontmatter as a dim
// metadata block and the body as markdown. Clicking the read view (for
// editable sections) swaps it for a textarea; blurring it saves and returns to
// the read view. needs_verification sections show a preview and can only be
// verified, not edited.
function sectionEl(doc, s) {
  const container = el("div", { className: "section-block" + (s.status === "needs_verification" ? " stale" : "") });

  if (s.heading) container.append(el("h2", { textContent: s.heading }));

  const editable = s.status !== "needs_verification";

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
    bodyDiv.innerHTML = marked.parse(body || "");
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
  hdr.append(el("h1", { textContent: doc.title }));
  const delBtn = el("button", { textContent: "Delete document", className: "sec-btn sec-btn-danger" });
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
