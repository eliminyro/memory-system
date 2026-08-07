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

// relAge renders a compact relative age ("4h", "9d", "2mo", "1y") from an ISO
// timestamp — used on cards, section spines and the doc meta line.
function relAge(iso) {
  if (!iso) return "";
  const ms = Date.now() - new Date(iso).getTime();
  if (Number.isNaN(ms)) return "";
  const h = ms / 36e5;
  if (h < 1) return Math.max(1, Math.round(ms / 6e4)) + "m";
  if (h < 24) return Math.round(h) + "h";
  const d = Math.round(h / 24);
  if (d < 30) return d + "d";
  const mo = Math.round(d / 30);
  if (mo < 12) return mo + "mo";
  return Math.round(mo / 12) + "y";
}

// icon parses a trusted static SVG string (literals in THIS file only, never
// user data) into a detached node so it can be appended via el(). The SVG markup
// carries no inline styles or event handlers, so it satisfies the CSP.
function icon(svg) {
  const t = document.createElement("template");
  t.innerHTML = svg.trim();
  return t.content.firstChild;
}

const ICON_COPY = '<svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a2 2 0 0 1 2-2h10"/></svg>';
const ICON_SHIELD = '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3l7 3v5c0 4.4-3 7.6-7 9-4-1.4-7-4.6-7-9V6z"/></svg>';
const ICON_LOCK = '<svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"><rect x="4" y="10.5" width="16" height="9.5" rx="2"/><path d="M8 10.5V7a4 4 0 0 1 8 0v3.5"/></svg>';
const ICON_WARN = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M10.3 3.9 2.4 18a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/><path d="M12 9v4M12 17h.01"/></svg>';
const ICON_INFO = '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="9"/><path d="M12 8h.01M11 12h1v4h1"/></svg>';
const ICON_UPLOAD = '<svg width="22" height="22" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3v12"/><path d="m7 10 5 5 5-5"/><path d="M5 21h14"/></svg>';

// ── Console chrome: theme + accent (client-only, localStorage-backed) ─────────
const _root = document.documentElement;
const SUN = '<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>';
const MOON = '<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>';

function applyTheme(t) {
  _root.setAttribute("data-theme", t);
  const ic = document.getElementById("theme-icon");
  if (ic) ic.innerHTML = t === "dark" ? MOON : SUN;
}

const ACCENT_HEX = { amber: "#f5b13d", green: "#4fd18b", cyan: "#49d4d4", violet: "#a78bfa", rose: "#f6799a" };
function applyAccent(a) {
  _root.setAttribute("data-accent", a);
  document.querySelectorAll(".sw").forEach((s) => s.setAttribute("aria-pressed", String(s.dataset.accent === a)));
}

// Apply the saved (or default) theme immediately, before the first dynamic paint.
applyTheme(localStorage.getItem("mem-theme") || "dark");

// wireChrome wires the static topbar controls once after auth: the swatch --c
// custom props (CSSOM — CSP forbids inline style attrs), the accent popover,
// the theme toggle, and the search-bar layout tweaks that the #q id-selector
// would otherwise override.
function wireChrome() {
  document.querySelectorAll(".sw").forEach((s) => {
    const hex = ACCENT_HEX[s.dataset.accent];
    if (hex) s.style.setProperty("--c", hex);
  });
  applyAccent(localStorage.getItem("mem-accent") || "amber");

  const themeBtn = document.getElementById("theme");
  if (themeBtn) themeBtn.addEventListener("click", () => {
    const next = _root.getAttribute("data-theme") === "dark" ? "light" : "dark";
    localStorage.setItem("mem-theme", next);
    applyTheme(next);
  });

  const accentBtn = document.getElementById("accent-btn");
  const accentPop = document.getElementById("accent-pop");
  if (accentBtn && accentPop) {
    const setPop = (open) => { accentPop.hidden = !open; accentBtn.setAttribute("aria-expanded", String(open)); };
    accentBtn.addEventListener("click", (e) => { e.stopPropagation(); setPop(accentPop.hidden); });
    document.querySelectorAll(".sw").forEach((s) => s.addEventListener("click", (e) => {
      e.stopPropagation();
      localStorage.setItem("mem-accent", s.dataset.accent);
      applyAccent(s.dataset.accent); // popover stays open — dismiss via outside-click / Escape
    }));
    document.addEventListener("click", (e) => { if (!accentPop.hidden && !accentPop.contains(e.target) && e.target !== accentBtn) setPop(false); });
    document.addEventListener("keydown", (e) => { if (e.key === "Escape") setPop(false); });
  }

  // #q's id-selector overrides the .searchbar input padding; restore room for the
  // mag icon and give the bar a sensible width — both via CSSOM (allowed).
  const sb = document.querySelector("#memsearch .searchbar");
  if (sb) { sb.style.flex = "1 1 auto"; sb.style.maxWidth = "640px"; }
  const q = document.getElementById("q");
  if (q) q.style.paddingLeft = "2.3rem";
}

// ── Segmented "rocker" controls ───────────────────────────────────────────────
// A .seg-thumb slides behind the active button. placeThumb positions it; snap
// (no animation) when first revealed, animate on click. Views render
// dynamically, so callers run initRockers + placeThumbsIn right after insert.
function placeThumb(seg, animate) {
  const thumb = seg.querySelector(".seg-thumb");
  const active = seg.querySelector("button.active");
  if (!thumb || !active || !seg.offsetParent) return; // hidden -> offsetParent null
  if (!animate) thumb.style.transition = "none";
  thumb.style.left = active.offsetLeft + "px";
  thumb.style.width = active.offsetWidth + "px";
  if (!animate) { void thumb.offsetWidth; thumb.style.transition = ""; }
}
// segmentedsIn returns rocker elements at OR under `elm`. Call sites pass either a
// standalone .segmented (tenant-scope, connect-method, new-tenant-type) or a
// container holding several (a rendered view / addform / fragment); querying only
// descendants misses a .segmented passed as `elm` itself. DocumentFragment has no
// classList, so guard it.
function segmentedsIn(elm) {
  const self = elm.classList && elm.classList.contains("segmented") ? [elm] : [];
  return self.concat(elm.querySelectorAll ? [...elm.querySelectorAll(".segmented")] : []);
}
function placeThumbsIn(elm, animate) {
  if (!elm) return;
  requestAnimationFrame(() => segmentedsIn(elm).forEach((s) => placeThumb(s, animate)));
}
// initRockers wires every not-yet-wired .segmented at or under `elm`: inserts the
// thumb and per-button activation. onChange(button, seg) fires after a click so
// callers can swap panels. Returns nothing; call placeThumbsIn to position.
function initRockers(elm, onChange) {
  if (!elm) return;
  segmentedsIn(elm).forEach((seg) => {
    if (seg._rockerReady) return;
    seg._rockerReady = true;
    seg.insertBefore(el("span", { className: "seg-thumb" }), seg.firstChild);
    seg.querySelectorAll("button").forEach((btn) => btn.addEventListener("click", () => {
      seg.querySelectorAll("button").forEach((x) => x.classList.toggle("active", x === btn));
      placeThumb(seg, true);
      if (onChange) onChange(btn, seg);
    }));
    placeThumb(seg, false);
  });
}
window.addEventListener("resize", () => document.querySelectorAll(".segmented").forEach((s) => placeThumb(s, false)));

// segActive returns the trimmed lowercased label of a rocker's active button.
function segActive(seg) {
  const b = seg && seg.querySelector("button.active");
  return b ? b.textContent.trim().toLowerCase() : "";
}

// ── Inline add/create forms ───────────────────────────────────────────────────
// wireAddToggle links an .add-toggle to its .addform: toggles [hidden] + .open,
// focuses the first field and positions rockers on show, resets a key-reveal on
// hide, and collapses on .af-cancel. opts.onOpen runs just before focus.
function wireAddToggle(toggle, form, opts = {}) {
  const resetKeyReveal = () => {
    const cfg = form.querySelector(".key-config"), rev = form.querySelector(".key-reveal");
    if (cfg && rev) { cfg.hidden = false; rev.hidden = true; }
  };
  toggle.addEventListener("click", () => {
    const show = form.hidden;
    form.hidden = !show;
    toggle.classList.toggle("open", show);
    if (show) {
      if (opts.onOpen) opts.onOpen();
      const i = form.querySelector("input,textarea,select"); if (i) i.focus();
      placeThumbsIn(form, false);
    } else resetKeyReveal();
  });
  form.querySelectorAll(".af-cancel").forEach((c) => c.addEventListener("click", (e) => {
    e.preventDefault(); form.hidden = true; toggle.classList.remove("open"); resetKeyReveal();
  }));
  return form;
}

// autoGrow makes a .sec-edit-ta (resize:none; overflow:hidden) grow to fit its
// content on focus and input.
function autoGrow(ta) {
  const fit = () => { ta.style.height = "auto"; ta.style.height = ta.scrollHeight + "px"; };
  ta.addEventListener("focus", fit);
  ta.addEventListener("input", fit);
  fit();
}

// ── Copy affordances ──────────────────────────────────────────────────────────

// flashCopy wires a copy-to-clipboard click on `btn`. getText() supplies the
// text (return null to abort silently — e.g. a missing source node or no
// secret). The label — a child .idtext if present, else the button itself —
// flashes `done` and toggles `addClass`, reverting to its original text after
// `ms`. With `onError`, a clipboard failure calls it and skips the flash;
// without one, failure is swallowed and the flash still runs (the copy-id /
// copy-code affordances' existing behavior). (R14)
function flashCopy(btn, getText, opts = {}) {
  const labelEl = btn.querySelector(".idtext") || btn;
  const { done = "copied ✓", ms = 1200, addClass = "copied", onError } = opts;
  const revert = opts.revert != null ? opts.revert : labelEl.textContent;
  btn.addEventListener("click", async () => {
    const text = getText();
    if (text == null) return;
    try {
      await navigator.clipboard.writeText(text);
    } catch (err) {
      if (onError) { onError(err); return; }
    }
    labelEl.textContent = done;
    if (addClass) btn.classList.add(addClass);
    setTimeout(() => { labelEl.textContent = revert; if (addClass) btn.classList.remove(addClass); }, ms);
  });
}

function shortId(s) {
  s = String(s || "");
  return s.length > 16 ? s.slice(0, 8) + "…" + s.slice(-4) : s;
}
// copyIdBtn copies `full` (a tenant id, endpoint URL — never a secret) and
// flashes "copied ✓". The full value lives in data-full; the label is elided.
function copyIdBtn(full, label, title) {
  const txt = el("span", { className: "idtext", textContent: label != null ? label : shortId(full) });
  const btn = el("button", { className: "copy-id", type: "button", title: title || "Copy" });
  btn.dataset.full = full;
  btn.append(txt, icon(ICON_COPY));
  flashCopy(btn, () => btn.dataset.full);
  return btn;
}
// copyCodeBtn copies the text of the sibling <pre> in its .codewrap parent.
function copyCodeBtn() {
  const btn = el("button", { className: "copy-code", type: "button", textContent: "copy" });
  flashCopy(btn, () => { const pre = btn.parentElement.querySelector("pre"); return pre ? pre.innerText : null; });
  return btn;
}

// wireDestructiveAction wires a confirm → DELETE → reload flow on `btn`: confirm
// `confirm`, disable the button, run `request` ({path, opts} for apiFetch), then
// await onDone(); on failure re-enable the button and alert `errorPrefix`. (R15)
function wireDestructiveAction(btn, { confirm: msg, request, onDone, errorPrefix = "Action failed" }) {
  btn.addEventListener("click", async () => {
    if (!confirm(msg)) return;
    btn.disabled = true;
    try {
      await apiFetch(request.path, request.opts);
      await onDone();
    } catch (err) {
      btn.disabled = false;
      alert(errorPrefix + ": " + err.message);
    }
  });
}

// gDot is the small status glow-dot inside a status .pill; its color is a token
// reference set via CSSOM (no CSS rule paints .pill .g).
function gDot(token) {
  const g = el("span", { className: "g" });
  g.style.background = `var(${token})`;
  return g;
}

// ── Navigation history ────────────────────────────────────────────────────────
// A stack of thunks that re-render the previous view. Drilling into a view
// pushes the way back; the back button pops and re-renders it. The two roots
// (browse, search results) reset the stack so back never escapes them.
const navStack = [];
function goBack() {
  (navStack.pop() || renderBrowse)();
}
// backButton returns a ← back control with the given label and click handler.
// Used by every drill-in view's header (R16).
function backButton(label, onClick) {
  const b = el("button", { className: "back", type: "button", textContent: label });
  b.addEventListener("click", onClick);
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
  const container = el("div", { className: "sec" });
  const editable = s.status !== "needs_verification";

  // Heading — editable (verified) or plain (withheld). Empty editable headings
  // get a faint "add heading" placeholder.
  const head = el("div", { className: "sec-head" });
  let headingEl;
  if (editable) {
    headingEl = el("h2", { className: "sec-h editable" + (s.heading ? "" : " placeholder"), textContent: s.heading || "Add heading…", title: "Click to edit" });
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
  } else {
    headingEl = el("h2", { className: "sec-h", textContent: s.heading || "" });
  }
  head.append(headingEl);
  const ageIso = s.verified_at || s.updated_at;
  const age = relAge(ageIso);
  if (age) {
    head.append(el("div", { className: "sec-meta" },
      el("span", { className: "age", title: (s.verified_at ? "Verified " : "Edited ") + (fmtDate(ageIso) || ""), textContent: age + " ago" })));
  }
  container.append(head);

  // needs_verification — guarded: content withheld; only "Mark verified"
  // (POST /sections/{id}/verify) unlocks editing. The .md-withheld child drives
  // the amber spine via .sec:has(.md-withheld)::before.
  if (!editable) {
    const withheld = el("div", { className: "md-withheld" });
    withheld.append(el("p", { textContent: s.preview || "Content withheld — this section was edited since it was last confirmed and stays locked until verified against source." }));
    const hints = s.verify_hints || [];
    if (hints.length) {
      const hintsRow = el("div", { className: "hints" }, el("span", { className: "eyebrow", textContent: "cited" }));
      for (const hp of hints) hintsRow.append(el("code", { textContent: hp }));
      withheld.append(hintsRow);
    }
    const verifyBtn = el("button", { className: "btn btn--primary", type: "button", textContent: "Mark verified" });
    verifyBtn.addEventListener("click", async () => {
      verifyBtn.disabled = true;
      try {
        await apiFetch("/sections/" + s.id + "/verify", { method: "POST" });
        showDocument(doc.id); // re-render: the section is now editable
      } catch (err) {
        verifyBtn.disabled = false;
        alert("Verify failed: " + err.message);
      }
    });
    withheld.append(el("div", { className: "withheld-actions" }, verifyBtn));
    container.append(withheld);
    return container;
  }

  // Verified prose — click-to-edit; blur commits PATCH content. The .prose.editable
  // child drives the green spine via .sec:has(.prose.editable)::before.
  const prose = el("div", { className: "prose editable", title: "Click to edit" });
  function renderRead() {
    prose.replaceChildren();
    const { frontmatter, body } = splitFrontmatter(s.content);
    if (frontmatter) prose.append(el("div", { className: "frontmatter", textContent: frontmatter }));
    const bodyDiv = el("div", { className: "md-body" });
    bodyDiv.innerHTML = renderMarkdown(body);
    prose.append(bodyDiv);
  }
  renderRead();

  function enterEdit() {
    if (prose.dataset.editing) return;
    prose.dataset.editing = "1";
    const ta = el("textarea", { className: "sec-edit-ta", value: s.content || "" });
    let settled = false;
    async function commit() {
      if (settled) return;
      settled = true;
      if (ta.value === (s.content || "")) { delete prose.dataset.editing; ta.replaceWith(prose); return; }
      try {
        const result = await apiFetch("/sections/" + s.id, {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ content: ta.value }),
        });
        s.content = result.content;
        delete prose.dataset.editing;
        renderRead();
        ta.replaceWith(prose);
      } catch (err) {
        settled = false; // keep editing so the user can retry
        alert("Save failed: " + err.message);
        ta.focus();
      }
    }
    function cancel() { if (settled) return; settled = true; delete prose.dataset.editing; ta.replaceWith(prose); }
    ta.addEventListener("blur", commit);
    ta.addEventListener("keydown", (e) => { if (e.key === "Escape") { e.preventDefault(); cancel(); } });
    prose.replaceWith(ta);
    autoGrow(ta);
    ta.focus();
  }
  prose.addEventListener("click", (e) => {
    if (e.target.closest("a")) return; // let links work normally
    enterEdit();
  });
  container.append(prose);
  return container;
}

async function showDocument(id) {
  const seq = _renderSeq;
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  const doc = await apiFetch(`/documents/${id}`);
  if (seq !== _renderSeq) return; // discard: navigated away during the fetch (B7)
  view.replaceChildren();

  view.append(backButton("← Memories", goBack));

  // path line (category/subcategory · slug)
  const path = el("div", { className: "mem-path" });
  const prefix = [doc.category, doc.subcategory].filter(Boolean).join("/");
  if (prefix) path.append(document.createTextNode(prefix + " · "));
  path.append(el("b", { textContent: doc.slug || "" }));
  view.append(path);

  // title — inline click-to-edit (PATCH /documents/{id})
  const titleEl = el("h1", { className: "doc-title editable", title: "Click to edit", textContent: doc.title || doc.slug || "" });
  inlineEdit(titleEl, doc.title || "", async (title) => {
    const updated = await apiFetch("/documents/" + doc.id, {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title }),
    });
    doc.title = updated.title;
    return updated.title;
  });
  view.append(titleEl);

  // meta row: tenant tag + doc_type + edited age + delete (DELETE /documents/{id})
  const meta = el("div", { className: "doc-meta" });
  if (doc.tenant_id) {
    const tag = el("span", { className: "tenant-tag" }, el("span", { className: "dot" }), document.createTextNode(doc.tenant_name || doc.tenant_id));
    tag.style.setProperty("--tc", tenantColor(doc.tenant_id));
    meta.append(tag);
  }
  if (doc.doc_type) meta.append(el("span", { className: "pill", textContent: doc.doc_type }));
  if (doc.updated_at) meta.append(el("span", { className: "muted mono", textContent: "edited " + relAge(doc.updated_at) + " ago" }));
  meta.append(el("span", { className: "spacer" }));
  const delBtn = el("button", { textContent: "Delete", className: "btn btn--danger", type: "button", title: "Delete document" });
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
  meta.append(delBtn);
  view.append(meta);

  const sections = doc.sections || [];
  const needing = sections.filter((s) => s.status === "needs_verification").length;
  const body = el("div", { className: "doc-body" });

  if (needing) {
    const notice = el("div", { className: "doc-notice" }, icon(ICON_WARN));
    notice.append(el("span", {},
      el("b", { textContent: `${needing} of ${sections.length} section${sections.length === 1 ? "" : "s"}` }),
      document.createTextNode(" need" + (needing === 1 ? "s" : "") + " verification.")));
    const review = el("button", { className: "linklike", type: "button", textContent: "Review ↓" });
    review.addEventListener("click", () => { const w = body.querySelector(".md-withheld"); if (w) w.scrollIntoView({ behavior: "smooth", block: "center" }); });
    notice.append(review);
    view.append(notice);
  }

  body.append(el("div", { className: "sec-head-row" }, el("span", { className: "eyebrow", textContent: "sections · " + sections.length })));
  for (const s of sections) body.append(sectionEl(doc, s));
  view.append(body);
}

// ── Search results view ───────────────────────────────────────────────────────

function renderSearchResults(results) {
  view.replaceChildren();
  view.append(...memHead());
  if (!results.length) {
    view.append(el("p", { className: "state-msg", textContent: "no results" }));
    return;
  }
  const legend = buildLegend(results);
  if (legend) view.append(legend);
  const list = el("div", { className: "mem-list" });
  for (const r of results) {
    list.append(memCard({
      pathPrefix: [r.category, r.subcategory].filter(Boolean).join("/"),
      pathBold: r.slug,
      title: r.doc_title,
      tenantId: r.tenant_id,
      tenantName: r.tenant_name,
      status: r.status,
      verifiedAt: r.verified_at,
      onClick: () => { navStack.push(() => renderSearchResults(results)); showDocument(r.document_id); },
    }));
  }
  view.append(list);
}

// ── Browse (index) view ───────────────────────────────────────────────────────

async function renderBrowse() {
  const seq = _renderSeq;
  navStack.length = 0; // root view — clear history
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  // A freshly-bootstrapped tenant with zero documents can yield JSON null here
  // (and older servers still do); coerce to [] so we never iterate a non-array.
  const tf = memFilter ? `&tenant_id=${encodeURIComponent(memFilter.id)}` : "";
  const entries = (await apiFetch(`/index?depth=summary${tf}`)) || [];
  if (seq !== _renderSeq) return; // discard stale render (B7)
  view.replaceChildren();
  view.append(...memHead());

  if (!entries.length) {
    view.append(el("p", {
      className: "state-msg",
      textContent: "No memories yet — import an archive from a tenant's page (Tenants tab), or add memories via your MCP client / the API.",
    }));
    return;
  }

  // Each index summary row (a category/subcategory bucket) is one card; clicking
  // it drills into that bucket's documents.
  const list = el("div", { className: "mem-list" });
  for (const e of entries) {
    const label = e.subcategory || e.category;
    const countText = e.doc_count != null ? ` (${e.doc_count})` : "";
    list.append(memCard({
      pathPrefix: e.category,
      pathBold: e.subcategory || null,
      title: label + countText,
      snip: e.topics || "",
      onClick: () => { navStack.push(renderBrowse); renderCategoryDocs(e.category, e.subcategory || null); },
    }));
  }
  view.append(list);
}

async function renderCategoryDocs(category, subcategory) {
  const seq = _renderSeq;
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  const params = new URLSearchParams({ category });
  if (subcategory) params.set("subcategory", subcategory);
  if (memFilter) params.set("tenant_id", memFilter.id);
  const docs = await apiFetch(`/documents?${params}`);
  if (seq !== _renderSeq) return; // discard stale render (B7)
  view.replaceChildren();

  view.append(backButton("← Memories", goBack));
  view.append(el("div", { className: "view-head" }, el("h1", { textContent: subcategory ? `${category} / ${subcategory}` : category })));

  if (!docs.length) {
    view.append(el("p", { className: "state-msg", textContent: "no documents" }));
    return;
  }
  const legend = buildLegend(docs);
  if (legend) view.append(legend);
  const list = el("div", { className: "mem-list" });
  for (const doc of docs) {
    list.append(memCard({
      pathPrefix: [doc.category || category, doc.subcategory || subcategory].filter(Boolean).join("/"),
      pathBold: doc.slug,
      title: doc.title || doc.slug,
      tenantId: doc.tenant_id,
      tenantName: doc.tenant_name,
      status: doc.status,
      verifiedAt: doc.verified_at,
      metaPill: doc.doc_type || null,
      onClick: () => { navStack.push(() => renderCategoryDocs(category, subcategory)); showDocument(doc.id); },
    }));
  }
  view.append(list);
}

// ── Search wiring ─────────────────────────────────────────────────────────────

let _searchTimer = null;
// _renderSeq is a monotonic render token bumped by route() on every navigation.
// Async renderers capture it before their first await and bail before any
// post-await write to the shared #view, so a fetch that resolves after the user
// has navigated away can't clobber the current view (B7).
let _renderSeq = 0;

async function runSearch(q) {
  const seq = _renderSeq;
  navStack.length = 0; // root view — clear history
  view.replaceChildren(el("p", { className: "state-msg", textContent: "searching…" }));
  try {
    const tf = memFilter ? `&tenant_id=${encodeURIComponent(memFilter.id)}` : "";
    const results = await apiFetch(`/search?q=${encodeURIComponent(q)}&limit=20${tf}`);
    if (seq !== _renderSeq) return; // discard stale render (B7)
    renderSearchResults(results);
  } catch (err) {
    if (seq !== _renderSeq) return; // discard stale render (B7)
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

// memCard builds a .mem-card. The owning tenant tints the left spine via the
// --tc custom prop (CSSOM); its status renders as an ok/warn pill.
function memCard(opts) {
  const card = el("a", { className: "mem-card" });
  if (opts.tenantId) card.style.setProperty("--tc", tenantColor(opts.tenantId));
  const path = el("span", { className: "mem-path" });
  if (opts.pathPrefix) path.append(document.createTextNode(opts.pathPrefix));
  if (opts.pathBold) {
    if (opts.pathPrefix) path.append(document.createTextNode(" · "));
    path.append(el("b", { textContent: opts.pathBold }));
  }
  card.append(el("div", { className: "mem-top" }, path));
  card.append(el("div", { className: "mem-title", textContent: opts.title || "" }));
  if (opts.snip) card.append(el("p", { className: "mem-snip", textContent: opts.snip }));

  const meta = el("div", { className: "mem-meta" });
  if (opts.tenantName || opts.tenantId) {
    const tag = el("span", { className: "tenant-tag" }, el("span", { className: "dot" }), document.createTextNode(opts.tenantName || opts.tenantId));
    if (opts.tenantType === "shared") tag.append(el("span", { className: "pill pill--shared", textContent: "shared" }));
    meta.append(tag);
  }
  meta.append(el("span", { className: "spacer" }));
  if (opts.status === "needs_verification") {
    meta.append(el("span", { className: "pill pill--warn" }, gDot("--warn"), document.createTextNode("needs verification")));
  } else if (opts.verifiedAt) {
    meta.append(el("span", { className: "pill pill--ok" }, gDot("--ok"), document.createTextNode("verified · " + relAge(opts.verifiedAt))));
  } else if (opts.metaPill) {
    meta.append(el("span", { className: "pill", textContent: opts.metaPill }));
  }
  card.append(meta);
  if (opts.onClick) card.addEventListener("click", (e) => { e.preventDefault(); opts.onClick(); });
  return card;
}

// buildLegend renders the per-tenant .legend (dot + name + count) + a total
// pill from a result set. Each chip narrows the view to its tenant. Returns
// null when no row carries a tenant id (e.g. the aggregate index summary).
function buildLegend(rows) {
  const seen = new Map(); // id -> {name, count}
  for (const r of rows) {
    if (!r.tenant_id) continue;
    const cur = seen.get(r.tenant_id) || { name: r.tenant_name || r.tenant_id, count: 0 };
    cur.count++;
    seen.set(r.tenant_id, cur);
  }
  if (!seen.size) return null;
  const legend = el("div", { className: "legend" }, el("span", { className: "eyebrow", textContent: "tenants" }));
  for (const [id, info] of seen) {
    const chip = el("button", { className: "lchip", type: "button" });
    const dot = el("span", { className: "dot" });
    dot.style.background = tenantColor(id);
    chip.append(dot, document.createTextNode((info.name || id) + " "), el("span", { className: "muted", textContent: "· " + info.count }));
    chip.addEventListener("click", () => { memFilter = { id, name: info.name }; renderMemFilterChip(); renderMemories().catch(showError); });
    legend.append(chip);
  }
  legend.append(el("span", { className: "total-pill" }, document.createTextNode("Total "), el("span", { className: "tnum", textContent: "· " + rows.length })));
  return legend;
}

// memHead builds the Memories view head (title + "+ new memory") and its inline
// create .addform. Commit POSTs /documents (tenant + path + title + first
// section) and opens the new doc. Fields collapse via the add-form helper.
function memHead() {
  const head = el("div", { className: "view-head" }, el("h1", { textContent: "Memories" }));
  const toggle = el("button", { className: "add-toggle", type: "button", textContent: "+ new memory" });
  head.append(toggle);

  const form = el("div", { className: "addform" });
  form.hidden = true;

  const tenantSel = el("select", { className: "text-input" });
  for (const t of writableTenants) tenantSel.append(el("option", { value: t.id, textContent: t.name || t.id }));
  if (!writableTenants.length) tenantSel.append(el("option", { value: "", textContent: "(no writable tenant)" }));
  const tRow = el("div", { className: "af-row" }, el("label", { textContent: "Tenant" }), tenantSel);
  form.append(el("div", { className: "af-inline" }, tRow));

  const catI = el("input", { className: "text-input", placeholder: "category" });
  const subI = el("input", { className: "text-input", placeholder: "subcategory — optional" });
  const slugI = el("input", { className: "text-input", placeholder: "slug" });
  const pathWrap = el("div", { className: "af-path" }, catI, el("span", { className: "sep", textContent: "/" }), subI, el("span", { className: "sep", textContent: "/" }), slugI);
  form.append(el("div", { className: "af-row" },
    el("label", { textContent: "Path" }), pathWrap,
    el("span", { className: "hint", textContent: "subcategory is optional — omit for e.g. tools/claude-code" })));

  const titleI = el("input", { className: "text-input", placeholder: "Short descriptive title" });
  form.append(el("div", { className: "af-row" }, el("label", { textContent: "Title" }), titleI));

  const bodyTa = el("textarea", { className: "text-input sec-edit-ta", placeholder: "## Heading\nBody text…" });
  form.append(el("div", { className: "af-row" }, el("label", { textContent: "First section (markdown)" }), bodyTa));
  autoGrow(bodyTa);

  const errLine = el("div", { className: "state-err" });
  errLine.hidden = true;
  const cancel = el("button", { className: "btn af-cancel", type: "button", textContent: "Cancel" });
  const commit = el("button", { className: "btn btn--primary af-commit", type: "button", textContent: "Create memory" });
  form.append(errLine, el("div", { className: "af-actions" }, cancel, commit));

  const showErr = (m) => { errLine.textContent = m; errLine.hidden = false; };
  wireAddToggle(toggle, form);
  commit.addEventListener("click", async () => {
    errLine.hidden = true;
    const category = catI.value.trim(), slug = slugI.value.trim(), title = titleI.value.trim();
    // The API derives the document title from the content's first H1 (else the
    // slug), so fold the Title field in as a leading heading; the first-section
    // markdown follows. There is no separate title field on the endpoint.
    const bodyText = bodyTa.value.trim();
    const content = title ? `# ${title}\n\n${bodyText}` : bodyText;
    if (!category || !slug) { showErr("Category and slug are required."); return; }
    if (!content) { showErr("Add a title or some content."); return; }
    if (!tenantSel.value) { showErr("No tenant you can write to is selected."); return; }
    commit.disabled = true;
    try {
      const created = await apiFetch("/documents", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          tenant_id: tenantSel.value,
          category,
          subcategory: subI.value.trim(),
          slug,
          content,
        }),
      });
      form.hidden = true; toggle.classList.remove("open");
      if (created && created.id) showDocument(created.id);
      else renderMemories().catch(showError);
    } catch (err) {
      commit.disabled = false;
      showErr(err.status === 409
        ? "A similar memory already exists in that tenant."
        : "Create failed: " + err.message);
    }
  });

  return [head, form];
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

// ── Admin gate ────────────────────────────────────────────────────────────────
// isAdmin is probed once on init and gates admin-only affordances in the Tenants
// tab (system admins see every tenant plus the create-tenant control). A 403 on
// the probe means "not an admin" and is swallowed (distinct from 401, which
// apiFetch already turns into a re-login).

let isAdmin = false;

// checkAdmin probes /admin/whoami and sets the module-level isAdmin. Returns true
// when the caller is an admin; returns false silently on 403 or any other
// failure, so non-admins never receive admin affordances.
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
// length to gate the Tenants tab / #tenants routes for non-admins (admins get
// the tab via isAdmin).
let writableTenants = [];

// checkWritable probes GET /tenants/writable — not adminOnly, so every
// logged-in caller can reach it. Its result (empty for a plain user, non-empty
// for a system admin or a delegated manager) gates the Tenants tab and the
// #tenants routes for non-admins (admins get them via isAdmin).
async function checkWritable() {
  try {
    writableTenants = (await apiFetch("/tenants/writable")) || [];
  } catch (err) {
    writableTenants = [];
  }
}

// route renders the top-level view for the current hash and refreshes the tab
// bar's active state. Routes: #memories (default), #tenants, #tenants/<id>,
// #connect. The superseded #admin/#acl/#import routes silently redirect to
// #tenants (the Tenants tab now owns all tenant management). It is the single
// source of truth for view switching — registered once on hashchange (see init),
// so every tab and back control just sets the hash.
function route() {
  _renderSeq++;               // invalidate any in-flight renderer from the prior view (B7)
  clearTimeout(_searchTimer); // a queued debounced search must not fire against the new view (B7)
  const h = location.hash;
  if (h === "#admin" || h === "#acl" || h === "#import") { location.hash = "tenants"; return; } // redirect superseded routes
  renderTabBar();
  const memsearch = document.getElementById("memsearch");
  const onMemories = h === "" || h === "#memories";
  if (memsearch) memsearch.hidden = !onMemories;
  if (h === "#tenants") {
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
// least one tenant. Connect is a corner link for everyone. Every tab just sets
// the hash — route() does the rendering.
function renderTabBar() {
  const bar = document.getElementById("tabbar");
  if (!bar) return;
  bar.replaceChildren();
  const h = location.hash;
  const tabs = [{ label: "Memories", view: "memories", hash: "memories", active: h === "" || h === "#memories" }];
  if (isAdmin || writableTenants.length) {
    tabs.push({ label: "Tenants", view: "tenants", hash: "tenants", active: h === "#tenants" || h.startsWith("#tenants/") });
  }
  for (const t of tabs) {
    const b = el("button", { type: "button", textContent: t.label, className: t.active ? "active" : "" });
    b.dataset.view = t.view;
    b.addEventListener("click", () => { location.hash = t.hash; });
    bar.append(b);
  }

  // Inject the admin role badge + the Connect ghost-btn into .top-right, left of
  // the static theme button. Rebuilt each route; clear prior injections first.
  const tr = document.querySelector(".top-right");
  if (!tr) return;
  tr.querySelectorAll(".role, .ghost-btn").forEach((n) => n.remove());
  const themeBtn = document.getElementById("theme");
  if (isAdmin) {
    const role = el("span", { className: "role", title: "You have system-admin privileges" });
    role.append(icon(ICON_SHIELD), document.createTextNode("system admin"));
    tr.insertBefore(role, themeBtn);
  }
  const connect = el("button", { className: "ghost-btn", type: "button", textContent: "Connect", title: "Connection instructions" });
  connect.addEventListener("click", () => { location.hash = "connect"; });
  tr.insertBefore(connect, themeBtn);
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

  view.append(backButton("← Memories", () => { location.hash = ""; }));
  view.append(el("div", { className: "view-head" }, el("h1", { textContent: "Connect a client" })));
  view.append(el("p", { className: "lede", textContent: "Point any MCP client at this server. Use OAuth for interactive work — it opens a browser once and needs no secret. Use an API key for headless agents and CI." }));

  const url = mcpURL();
  view.append(el("div", { className: "endpoint" },
    el("span", { className: "eyebrow", textContent: "mcp endpoint" }),
    el("code", { textContent: url }),
    el("span", { className: "spacer" }),
    copyIdBtn(url, "copy", "Copy endpoint")));

  const seg = el("div", { className: "segmented", id: "connect-method" },
    el("button", { type: "button", className: "active", textContent: "OAuth" }),
    el("button", { type: "button", textContent: "API key" }));
  view.append(seg);

  // OAuth
  const oauthBody = el("div", { className: "method-body" });
  oauthBody.dataset.method = "oauth";
  oauthBody.append(el("p", {}, "Add this to your client config (e.g. ", el("code", { textContent: "~/.claude.json" }), "). On first call the client opens a browser to authorize this instance."));
  const oauthWrap = el("div", { className: "codewrap" });
  oauthWrap.append(copyCodeBtn(), el("pre", {}, el("code", { textContent: mcpConfigJSON(url, false) })));
  oauthBody.append(oauthWrap);
  const oauthCallout = el("div", { className: "callout" }, icon(ICON_INFO));
  oauthCallout.append(el("span", {}, el("b", { textContent: "First time here? " }), document.createTextNode("Authorizing provisions your personal tenant automatically — no setup needed. You land on it as owner.")));
  oauthBody.append(oauthCallout);
  view.append(oauthBody);

  // API key
  const keyBody = el("div", { className: "method-body" });
  keyBody.dataset.method = "key";
  keyBody.hidden = true;
  const keyLink = el("a", { textContent: "Tenants → API keys" });
  keyLink.addEventListener("click", () => { location.hash = "tenants"; });
  keyBody.append(el("p", {}, "Create a key under ", keyLink, ", then send it as a bearer token. The key is scoped to the tenant it was minted on."));
  const keyWrap = el("div", { className: "codewrap" });
  keyWrap.append(copyCodeBtn(), el("pre", {}, el("code", { textContent: mcpConfigJSON(url, true) })));
  keyBody.append(keyWrap);
  const keyCallout = el("div", { className: "callout" }, icon(ICON_SHIELD));
  keyCallout.append(el("span", { textContent: "Treat the key like a password — it grants full read/write on its tenant. Rotate it from the same panel if it leaks." }));
  keyBody.append(keyCallout);
  view.append(keyBody);

  initRockers(seg, (btn) => {
    const m = btn.textContent.trim().toLowerCase().includes("oauth") ? "oauth" : "key";
    view.querySelectorAll(".method-body").forEach((mb) => { mb.hidden = mb.dataset.method !== m; });
  });
  placeThumbsIn(view, false);
}

// ── Tenants tab ───────────────────────────────────────────────────────────────
// The Tenants tab lists the tenants the caller may manage, split into Shared and
// Personal sub-tabs (GET /api/tenants?type=), with a client-side live filter and
// an admin-only create affordance. Selecting a tenant opens its type-aware panel
// (#tenants/<id>). This replaces the old standalone ACL and Import pages.

let tenantsType = "shared"; // active sub-tab; Shared is the default
const tenantCache = {}; // id → {id,name,type,relation}, populated as lists load

async function renderTenants() {
  const seq = _renderSeq;
  navStack.length = 0;
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  let rows;
  try {
    rows = (await apiFetch(`/tenants?type=${encodeURIComponent(tenantsType)}`)) || [];
  } catch (err) { showError(err); return; }
  if (seq !== _renderSeq) return; // discard stale render (B7)
  for (const t of rows) tenantCache[t.id] = t;
  view.replaceChildren();

  const head = el("div", { className: "view-head" }, el("h1", { textContent: "Tenants" }));
  view.append(head);
  if (isAdmin) {
    const toggle = el("button", { className: "add-toggle", type: "button", textContent: "+ new tenant" });
    head.append(toggle);
    view.append(newTenantTypeForm(toggle));
  }

  // Personal ⇄ Shared scope rocker (replaces the old #tenant-subtabs)
  const scope = el("div", { className: "segmented", id: "tenant-scope" },
    el("button", { type: "button", className: tenantsType === "personal" ? "active" : "", textContent: "Personal" }),
    el("button", { type: "button", className: tenantsType === "shared" ? "active" : "", textContent: "Shared" }));
  view.append(scope);

  // client-side live filter (name substring / UUID substring), preserved
  const filter = el("input", { className: "text-input tenant-filter", type: "text", placeholder: "filter by name or UUID…", autocomplete: "off" });
  view.append(el("div", { className: "admin-form-fields" }, filter));

  const grid = el("div", { className: "tenant-grid" });
  view.append(grid);

  function draw() {
    const f = filter.value.trim().toLowerCase();
    grid.replaceChildren();
    const shown = rows.filter((t) =>
      !f || (t.name || "").toLowerCase().includes(f) || (t.id || "").toLowerCase().includes(f));
    if (!shown.length) {
      grid.append(el("p", { className: "state-msg", textContent: tenantsType === "personal" ? "No personal tenants." : "No tenants." }));
      return;
    }
    for (const t of shown) {
      const card = el("div", { className: "tenant-card" });
      card.style.setProperty("--tc", tenantColor(t.id));
      card.append(
        el("span", { className: "dot" }),
        el("div", {},
          el("div", { className: "nm", textContent: t.name || "(unnamed)" }),
          el("div", { className: "sub", textContent: [t.relation, t.type].filter(Boolean).join(" · ") })),
        el("span", { className: "spacer" }),
        el("span", { className: t.type === "shared" ? "pill pill--shared" : "pill pill--personal", textContent: t.type || "" }));
      card.addEventListener("click", () => { location.hash = "tenants/" + t.id; });
      grid.append(card);
    }
  }
  filter.addEventListener("input", draw);
  draw();

  initRockers(scope, (btn) => {
    const ty = btn.textContent.trim().toLowerCase() === "shared" ? "shared" : "personal";
    if (ty !== tenantsType) { tenantsType = ty; renderTenants().catch(showError); }
  });
  placeThumbsIn(view, false);
}

// newTenantTypeForm — admin-only inline create .addform on the Tenants tab: a
// type rocker (personal/shared, pre-picked from the active scope), a name, and
// an owner-email row shown only for personal tenants. POST /api/admin/tenants
// (body { name, type }, plus owner_email when a personal owner is provided).
function newTenantTypeForm(toggle) {
  const form = el("div", { className: "addform" });
  form.hidden = true;

  const typeSeg = el("div", { className: "segmented seg-inline", id: "new-tenant-type" },
    el("button", { type: "button", className: "active", textContent: "personal" }),
    el("button", { type: "button", textContent: "shared" }));
  form.append(el("div", { className: "af-row" }, el("label", { textContent: "Type" }), typeSeg));

  const nameI = el("input", { className: "text-input", placeholder: "e.g. jdoe, platform" });
  form.append(el("div", { className: "af-row" }, el("label", { textContent: "Name" }), nameI));

  const emailRow = el("div", { className: "af-row" });
  emailRow.dataset.type = "personal";
  const emailI = el("input", { className: "text-input", type: "email", placeholder: "person@example.com" });
  emailRow.append(
    el("label", {}, document.createTextNode("Owner email "), el("span", { className: "desc", textContent: "— whom this personal tenant is for; they become its owner (one per tenant)" })),
    emailI);
  form.append(emailRow);

  const cancel = el("button", { className: "btn af-cancel", type: "button", textContent: "Cancel" });
  const commit = el("button", { className: "btn btn--primary af-commit", type: "button", textContent: "Create tenant" });
  form.append(el("div", { className: "af-actions" }, cancel, commit));

  const currentType = () => (segActive(typeSeg) === "shared" ? "shared" : "personal");
  const applyType = (t) => { emailRow.hidden = t !== "personal"; };

  wireAddToggle(toggle, form, { onOpen: () => {
    const scope = document.querySelector("#tenant-scope button.active");
    const t = scope && scope.textContent.trim().toLowerCase() === "shared" ? "shared" : "personal";
    typeSeg.querySelectorAll("button").forEach((b) => b.classList.toggle("active", b.textContent.trim().toLowerCase() === t));
    applyType(t);
    placeThumb(typeSeg, false);
  } });
  initRockers(typeSeg, (btn) => applyType(btn.textContent.trim().toLowerCase()));
  applyType(currentType());

  commit.addEventListener("click", async () => {
    const name = nameI.value.trim();
    const type = currentType();
    if (!name) { alert("Name is required."); return; }
    commit.disabled = true;
    const body = { name, type };
    if (type === "personal" && emailI.value.trim()) body.owner_email = emailI.value.trim();
    try {
      await apiFetch("/admin/tenants", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      form.hidden = true; toggle.classList.remove("open");
      renderTenants();
    } catch (err) {
      commit.disabled = false;
      alert("Create tenant failed: " + err.message);
    }
  });

  return form;
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
  const seq = _renderSeq;
  navStack.length = 0;
  view.replaceChildren(el("p", { className: "state-msg", textContent: "loading…" }));
  let t;
  try { t = await lookupTenant(id); } catch (err) { showError(err); return; }
  if (seq !== _renderSeq) return; // discard stale render (B7)
  if (!t) {
    view.replaceChildren(el("p", { className: "state-msg state-err", textContent: "Tenant not found or not manageable." }));
    return;
  }
  view.replaceChildren();

  view.append(backButton("← Tenants", () => { location.hash = "tenants"; }));

  const panel = el("div", { className: "panel" });

  const phead = el("div", { className: "panel-head" });
  phead.style.setProperty("--tc", tenantColor(t.id));
  const viewMem = el("button", { className: "btn", type: "button", textContent: "View memories" });
  viewMem.addEventListener("click", () => { memFilter = { id: t.id, name: t.name, type: t.type }; location.hash = "memories"; });
  phead.append(
    el("span", { className: "dot" }),
    el("h2", { textContent: t.name || "(unnamed)" }),
    el("span", { className: "spacer" }),
    viewMem,
    copyIdBtn(t.id, shortId(t.id), "Copy full tenant ID"));
  panel.append(phead);

  // Settings rockers: self-service lock + enforcement toggles. tenantSettingsSection
  // loads current values and persists changes (reveals only to a manager/admin).
  panel.append(tenantSettingsSection(t));

  if (t.type === "personal") {
    // Personal: API keys + Import. No member management (single-owner tenant).
    let keys;
    try { keys = (await apiFetch(`/admin/tenants/${id}/keys`)) || []; }
    catch (err) { keys = null; panel.append(el("div", { className: "section" }, el("p", { className: "state-msg state-err", textContent: "Failed to load keys: " + err.message }))); }
    if (keys) panel.append(keysSection(t, keys, () => renderTenantPanel(id)));
    panel.append(importSection(id));
  } else {
    // Shared: Members/ACL + per-doc guest sharing + Import. No API keys (refused
    // for shared tenants by the backend).
    panel.append(tenantMembersSection(id));
    panel.append(aclDocumentSection());
    panel.append(importSection(id));
  }

  if (seq !== _renderSeq) return; // discard: navigated away during the keys fetch (B7)
  view.append(panel);
  initRockers(view);          // wire settings + addform rockers in this panel
  placeThumbsIn(view, false); // position the visible ones
}

// tenantMembersSection — the tenant-membership grants (viewer/member/manager)
// for a fixed tenant id, extracted from the old renderAcl so it renders bound to
// the route tenant with no dropdown. Same /acl/tenants/{id}/grants endpoints.
function tenantMembersSection(tenantID) {
  const sec = el("div", { className: "section" });
  const eyebrow = el("span", { className: "eyebrow", textContent: "members" });
  const toggle = el("button", { className: "add-toggle", type: "button", textContent: "+ invite" });
  sec.append(el("div", { className: "sec-head-row" }, eyebrow, toggle));

  // invite addform (email + role rocker) → POST grant
  const form = el("div", { className: "addform" });
  form.hidden = true;
  const emailI = el("input", { className: "text-input", type: "email", placeholder: "name@example.com" });
  const roleSeg = el("div", { className: "segmented seg-inline" },
    el("button", { type: "button", className: "active", textContent: "viewer" }),
    el("button", { type: "button", textContent: "member" }));
  if (canGrantManager()) roleSeg.append(el("button", { type: "button", textContent: "manager" }));
  form.append(el("div", { className: "af-inline" },
    el("div", { className: "af-row" }, el("label", { textContent: "Email" }), emailI),
    el("div", { className: "af-row" }, el("label", { textContent: "Role" }), roleSeg)));
  const cancel = el("button", { className: "btn af-cancel", type: "button", textContent: "Cancel" });
  const commit = el("button", { className: "btn btn--primary af-commit", type: "button", textContent: "Send invite" });
  form.append(el("div", { className: "af-actions" }, cancel, commit));
  sec.append(form);
  wireAddToggle(toggle, form);

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
    const list = grants || [];
    eyebrow.textContent = "members · " + list.length;
    if (!list.length) { body.append(el("p", { className: "meta", textContent: "no members" })); return; }
    for (const g of list) body.append(memberRow(tenantID, g, load));
  }

  commit.addEventListener("click", async () => {
    const email = emailI.value.trim();
    if (!email) { alert("Email is required."); return; }
    commit.disabled = true;
    const relation = segActive(roleSeg) || "member";
    try {
      await apiFetch(`/acl/tenants/${tenantID}/grants`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, relation }),
      });
      emailI.value = ""; form.hidden = true; toggle.classList.remove("open");
      await load();
    } catch (err) {
      alert("Invite failed: " + err.message);
    } finally {
      commit.disabled = false;
    }
  });

  load().catch(showError);
  return sec;
}

// memberRow renders one tenant grant as a .member-row: avatar (initials, tinted
// via --av), name/email, role tag (mgr accent for managers), and a hover Remove
// (DELETE /acl/tenants/{id}/grants).
function memberRow(tenantID, g, onRevoked) {
  const email = g.email || "";
  const local = email.replace(/@.*/, "");
  const avatar = el("span", { className: "avatar", textContent: (local.slice(0, 2) || "?").toUpperCase() });
  avatar.style.setProperty("--av", tenantColor(email || g.relation));
  const who = el("div", { className: "who" },
    el("div", { className: "nm", textContent: local || email }),
    el("small", { textContent: email }));
  const roleTag = el("span", { className: "role-tag" + (g.relation === "manager" ? " mgr" : ""), textContent: g.relation || "" });
  const revoke = el("button", { className: "btn btn--danger", type: "button", textContent: "Remove" });
  wireDestructiveAction(revoke, {
    confirm: `Revoke ${g.relation} for ${email}?`,
    request: {
      path: `/acl/tenants/${tenantID}/grants`,
      opts: { method: "DELETE", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ email: g.email, relation: g.relation }) },
    },
    onDone: onRevoked,
    errorPrefix: "Revoke failed",
  });
  return el("div", { className: "member-row" }, avatar, who, roleTag, el("div", { className: "row-actions" }, revoke));
}

// importSection — the upload/poll import flow (dropzone) with the target tenant
// fixed to a passed id. Same /import + /import/{id} endpoints (multipart
// "archive" + "tenant_id").
function importSection(tenantID) {
  const sec = el("div", { className: "section" });
  sec.append(el("span", { className: "eyebrow", textContent: "import archive" }));

  const fileInput = el("input", { type: "file", accept: ".zip" });
  fileInput.hidden = true;
  const dz = el("label", { className: "dropzone" },
    icon(ICON_UPLOAD),
    el("span", { className: "dz-hint" }, el("b", { textContent: "Choose a file" }), document.createTextNode(" or drop it here")),
    el("span", { className: "dz-sub", textContent: "markdown archive · .zip" }),
    fileInput);
  sec.append(dz);

  sec.append(el("div", { className: "import-note" }, icon(ICON_INFO),
    el("span", {},
      document.createTextNode("Entries are markdown files keyed by path: "),
      el("code", { textContent: "category / subcategory / slug.md" }),
      document.createTextNode(" (subcategory optional). Files must sit at the archive ROOT — a wrapping top-level directory makes every path parse as misc/<path>. A matching document is "),
      el("b", { textContent: "overwritten" }),
      document.createTextNode("; new paths are created; documents not in the archive are left as-is."))));

  const importBtn = el("button", { className: "btn btn--primary", type: "button", textContent: "Import" });
  importBtn.disabled = true;
  sec.append(importBtn);

  const progress = el("div", { className: "import-progress" });
  sec.append(progress);

  let activeTimer = null; // guards against overlapping polls
  let chosen = null;
  const dzHint = () => dz.querySelector(".dz-hint");
  fileInput.addEventListener("change", () => {
    chosen = fileInput.files[0] || null;
    importBtn.disabled = !chosen;
    dzHint().replaceChildren(chosen
      ? el("b", { textContent: chosen.name })
      : el("b", { textContent: "Choose a file" }));
  });

  importBtn.addEventListener("click", async () => {
    if (!chosen) return;
    if (activeTimer) { clearInterval(activeTimer); activeTimer = null; }
    importBtn.disabled = true;
    progress.replaceChildren();
    try {
      const body = new FormData();
      body.append("archive", chosen);
      body.append("tenant_id", tenantID);
      // No Content-Type header: the browser sets the multipart boundary itself.
      const job = await apiFetch("/import", { method: "POST", body });
      fileInput.value = ""; chosen = null;
      dzHint().replaceChildren(el("b", { textContent: "Choose a file" }), document.createTextNode(" or drop it here"));
      renderImportProgress(progress, job);
      activeTimer = pollImportJob(progress, job.id, job.tenant_id || tenantID, () => { activeTimer = null; });
    } catch (err) {
      progress.replaceChildren(el("p", { className: "state-msg state-err", textContent: "Upload failed: " + err.message }));
    } finally {
      importBtn.disabled = !chosen;
    }
  });

  return sec;
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
    // Self-cancel once our target node is detached (route() replaced the panel):
    // stops an orphaned interval writing to a dead node, and a duplicate interval
    // spawning on return. isConnected is false once replaceChildren detached it. (R12)
    if (!container.isConnected) { clearInterval(timer); onDone(); return; }
    let job;
    try {
      const q = tenantID ? "?tenant_id=" + encodeURIComponent(tenantID) : "";
      job = await apiFetch("/import/" + id + q);
    } catch (err) {
      clearInterval(timer);
      onDone();
      if (container.isConnected) container.append(el("p", { className: "state-msg state-err", textContent: "Status check failed: " + err.message }));
      return;
    }
    if (!container.isConnected) { clearInterval(timer); onDone(); return; } // detached during the await
    renderImportProgress(container, job);
    if (job.status === "succeeded" || job.status === "failed") {
      clearInterval(timer);
      onDone();
    }
  }, IMPORT_POLL_MS);
  return timer;
}

// ── Tenant membership grants (used by tenantMembersSection) ───────────────────

// tenantSettingsSection renders the enforcement toggles as segmented rockers:
// the self-service lock (its own lock-glyph row) plus staleness / duplicate-
// guard / cleanup. These persist on toggle: the three enforcement toggles
// (staleness_mode / duplicate_guard / cleanup_scan_enabled) via
// PATCH /tenants/{id}/settings (manager-level, honoring the self-service lock),
// and the self-service lock via PATCH /admin/tenants/{id} (admin-only). Returns a
// fragment of .section blocks; the caller's initRockers wires the thumbs.
function tenantSettingsSection(t) {
  const shared = t.type === "shared";
  const frag = document.createDocumentFragment();

  // Both sections stay hidden until GET settings confirms the caller may manage
  // them (a plain member never sees settings).
  const lockSec = el("div", { className: "section" });
  lockSec.hidden = true;
  const lockSeg = el("div", { className: "segmented seg-inline" },
    el("button", { type: "button", textContent: "open" }),
    el("button", { type: "button", textContent: "admin-only" }));
  lockSec.append(el("div", { className: "toggle-row lock-row" },
    el("span", { className: "lk" }, icon(ICON_LOCK)),
    el("div", { className: "lbl" },
      el("b", { textContent: "Self-service lock" }),
      el("small", { textContent: shared ? "admin-only: only managers & system admins change settings or make keys" : "who may change settings & create keys on this tenant" })),
    lockSeg));
  frag.append(lockSec);

  const enf = el("div", { className: "section" });
  enf.hidden = true;
  enf.append(el("span", { className: "eyebrow", textContent: "enforcement" }));
  const staleSeg = el("div", { className: "segmented seg-inline" },
    el("button", { type: "button", textContent: "off" }),
    el("button", { type: "button", textContent: "advisory" }),
    el("button", { type: "button", textContent: "hard" }));
  enf.append(el("div", { className: "toggle-row" },
    el("div", { className: "lbl" }, document.createTextNode("Staleness mode "), el("small", { textContent: "off · advisory flags stale reads · hard withholds guarded content" })),
    staleSeg));
  const dupSeg = el("div", { className: "segmented seg-inline" },
    el("button", { type: "button", textContent: "off" }),
    el("button", { type: "button", textContent: "on" }));
  enf.append(el("div", { className: "toggle-row" },
    el("div", { className: "lbl" }, document.createTextNode("Duplicate guard "), el("small", { textContent: "refuse near-duplicate memories on write" })),
    dupSeg));
  const cleanSeg = el("div", { className: "segmented seg-inline" },
    el("button", { type: "button", textContent: "off" }),
    el("button", { type: "button", textContent: "on" }));
  enf.append(el("div", { className: "toggle-row" },
    el("div", { className: "lbl" }, document.createTextNode("Cleanup scan "), el("small", { textContent: "nightly near-duplicate sweep" })),
    cleanSeg));
  const errLine = el("div", { className: "state-err" });
  errLine.hidden = true;
  enf.append(errLine);
  frag.append(enf);

  const setActive = (seg, label) =>
    seg.querySelectorAll("button").forEach((b) => b.classList.toggle("active", b.textContent.trim().toLowerCase() === label));
  const showErr = (m) => { errLine.textContent = m; errLine.hidden = false; setTimeout(() => { errLine.hidden = true; }, 4000); };

  let current = null; // last saved settings, for optimistic revert-on-error

  function applyState(s) {
    if (!s) return;
    setActive(lockSeg, s.effective_self_service_policy === "admin_only" ? "admin-only" : "open");
    setActive(staleSeg, s.staleness_mode || "off");
    setActive(dupSeg, s.duplicate_guard ? "on" : "off");
    setActive(cleanSeg, s.cleanup_scan_enabled ? "on" : "off");
    placeThumbsIn(lockSec, false);
    placeThumbsIn(enf, false);
  }
  async function save(path, patch) {
    try {
      current = await apiFetch(path, { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify(patch) });
    } catch (err) {
      showErr(err.status === 403 ? "Not allowed to change that here." : "Couldn't save: " + err.message);
      applyState(current); // revert the rocker to the last saved value
    }
  }

  // Persist on toggle. The three enforcement toggles hit the self-service settings
  // surface (manager-level, honoring the self-service lock); the lock itself is
  // admin-only and goes through the admin tenant patch. initRockers here marks the
  // rockers ready, so the panel's later global initRockers(view) skips them.
  initRockers(frag, (btn, seg) => {
    const val = btn.textContent.trim().toLowerCase();
    if (seg === staleSeg) save(`/tenants/${t.id}/settings`, { staleness_mode: val });
    else if (seg === dupSeg) save(`/tenants/${t.id}/settings`, { duplicate_guard: val === "on" });
    else if (seg === cleanSeg) save(`/tenants/${t.id}/settings`, { cleanup_scan_enabled: val === "on" });
    else if (seg === lockSeg) save(`/admin/tenants/${t.id}`, { self_service_policy: val === "admin-only" ? "admin_only" : "open" });
  });

  // Load current settings; reveal only to a caller who may manage them. The lock
  // is admin-only to CHANGE, so non-admins see it read-only (still informative).
  apiFetch(`/tenants/${t.id}/settings`).then((s) => {
    current = s;
    lockSec.hidden = false; enf.hidden = false;
    applyState(s);
    if (!isAdmin) { lockSeg.style.pointerEvents = "none"; lockSeg.style.opacity = "0.55"; }
  }).catch(() => { /* not manageable by this caller: leave settings hidden */ });

  return frag;
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
// aclDocumentSection — per-document guest sharing for a shared tenant. There is
// no tenant-wide grant-list endpoint (lookup is by document id), so a "+ grant"
// addform (email + document id + access rocker → POST /acl/documents/{id}/grants)
// sits above a document-id loader that renders that doc's grants as .grant-rows.
function aclDocumentSection() {
  const sec = el("div", { className: "section" });
  const toggle = el("button", { className: "add-toggle", type: "button", textContent: "+ grant" });
  sec.append(el("div", { className: "sec-head-row" },
    el("span", { className: "eyebrow", textContent: "access beyond members · single documents" }), toggle));

  const form = el("div", { className: "addform" });
  form.hidden = true;
  const gEmail = el("input", { className: "text-input", type: "email", placeholder: "name@example.com" });
  const gDocId = el("input", { className: "text-input", placeholder: "document id (from its URL or the API)" });
  const accessSeg = el("div", { className: "segmented seg-inline" },
    el("button", { type: "button", className: "active", textContent: "read" }),
    el("button", { type: "button", textContent: "read + write" }));
  form.append(
    el("div", { className: "af-row" }, el("label", { textContent: "User email" }), gEmail),
    el("div", { className: "af-row" }, el("label", { textContent: "Document" }), gDocId, el("span", { className: "hint", textContent: "a single document in this tenant, by id" })),
    el("div", { className: "af-row" }, el("label", { textContent: "Access" }), accessSeg));
  const gCancel = el("button", { className: "btn af-cancel", type: "button", textContent: "Cancel" });
  const gCommit = el("button", { className: "btn btn--primary af-commit", type: "button", textContent: "Grant" });
  form.append(el("div", { className: "af-actions" }, gCancel, gCommit));
  sec.append(form);
  wireAddToggle(toggle, form);

  const idInput = el("input", { className: "text-input", type: "text", placeholder: "document id" });
  const loadBtn = el("button", { className: "btn", type: "button", textContent: "Load grants" });
  sec.append(el("div", { className: "admin-form-fields" },
    el("span", { className: "admin-form-label", textContent: "Document:" }), idInput, loadBtn));

  const body = el("div");
  sec.append(body);

  async function load(docID) {
    docID = (docID || idInput.value).trim();
    if (!docID) return;
    idInput.value = docID;
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
    const list = grants || [];
    if (!list.length) { body.append(el("p", { className: "meta", textContent: "no guest grants" })); return; }
    for (const g of list) body.append(grantRow(docID, doc, g, () => load(docID)));
  }

  loadBtn.addEventListener("click", () => load().catch(showError));
  idInput.addEventListener("keydown", (e) => { if (e.key === "Enter") { e.preventDefault(); load().catch(showError); } });

  gCommit.addEventListener("click", async () => {
    const email = gEmail.value.trim(), docID = gDocId.value.trim();
    if (!email || !docID) { alert("Email and document id are required."); return; }
    gCommit.disabled = true;
    const relation = segActive(accessSeg).includes("write") ? "editor" : "viewer";
    try {
      await apiFetch(`/acl/documents/${docID}/grants`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ email, relation }),
      });
      gEmail.value = ""; form.hidden = true; toggle.classList.remove("open");
      await load(docID);
    } catch (err) {
      alert("Grant failed: " + err.message);
    } finally {
      gCommit.disabled = false;
    }
  });

  return sec;
}

// grantRow renders one per-document guest grant as a .grant-row: the subject on
// the left, the document→access chain on the right, and a hover-overlaid Revoke
// (DELETE /acl/documents/{id}/grants).
function grantRow(docID, doc, g, onRevoked) {
  const prefix = [doc.category, doc.subcategory].filter(Boolean).join("/");
  const chain = el("span", { className: "gchain" });
  if (prefix) chain.append(document.createTextNode(prefix + " · "));
  chain.append(el("b", { textContent: doc.slug || "" }), document.createTextNode(" · "), el("span", { className: "gacc", textContent: g.relation === "editor" ? "rw" : "r" }));
  const revoke = el("button", { className: "btn btn--danger", type: "button", textContent: "Revoke" });
  wireDestructiveAction(revoke, {
    confirm: `Revoke ${g.relation} for ${g.email}?`,
    request: {
      path: `/acl/documents/${docID}/grants`,
      opts: { method: "DELETE", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ email: g.email, relation: g.relation }) },
    },
    onDone: onRevoked,
    errorPrefix: "Revoke failed",
  });
  return el("div", { className: "grant-row" }, el("span", { className: "subj", textContent: g.email || "" }), chain, el("div", { className: "row-actions" }, revoke));
}

function keysSection(t, keys, refresh) {
  refresh = refresh || (() => renderTenantPanel(t.id)); // default re-renders the tenant panel
  const sec = el("div", { className: "section" });
  const toggle = el("button", { className: "add-toggle", type: "button", textContent: "+ create key" });
  sec.append(el("div", { className: "sec-head-row" }, el("span", { className: "eyebrow", textContent: "api keys" }), toggle));

  // create addform: .key-config collects label/TTL; .key-create swaps in the
  // .key-reveal that shows the plaintext exactly once.
  const form = el("div", { className: "addform" });
  form.hidden = true;
  const labelI = el("input", { className: "text-input", placeholder: "e.g. laptop, ci" });
  const ttlI = el("input", { className: "text-input", type: "number", min: "1", placeholder: "TTL days (optional)" });
  const kcCancel = el("button", { className: "btn af-cancel", type: "button", textContent: "Cancel" });
  const kcCreate = el("button", { className: "btn btn--primary key-create", type: "button", textContent: "Create key" });
  const cfg = el("div", { className: "key-config" },
    el("div", { className: "af-row" }, el("label", { textContent: "Label" }), labelI, el("span", { className: "hint", textContent: "names the key so you can tell them apart later" })),
    el("div", { className: "af-row" }, el("label", { textContent: "TTL (days)" }), ttlI),
    el("div", { className: "af-actions" }, kcCancel, kcCreate));
  form.append(cfg);

  // The secret shows once: it lives only in this closure and the visible code
  // node — never in a data-* attribute, sessionStorage, or elsewhere.
  let secret = null;
  const secretCode = el("code", { textContent: "" });
  const copyBtn = el("button", { className: "copy-id", type: "button", title: "Copy key" },
    el("span", { className: "idtext", textContent: "copy" }), icon(ICON_COPY));
  const doneBtn = el("button", { className: "btn btn--primary af-commit", type: "button", textContent: "Done" });
  const reveal = el("div", { className: "key-reveal" },
    el("div", { className: "af-title", textContent: "copy it now — shown once" }),
    el("div", { className: "kv" }, secretCode, copyBtn),
    el("div", { className: "warn-line" }, icon(ICON_WARN), document.createTextNode("Store it in your secret manager — you won't be able to see it again. Rotate to replace.")),
    el("div", { className: "af-actions" }, doneBtn));
  reveal.hidden = true;
  form.append(reveal);
  sec.append(form);
  wireAddToggle(toggle, form);

  // key list
  const list = el("div");
  sec.append(list);
  if (!keys.length) list.append(el("p", { className: "meta", textContent: "no keys" }));
  else for (const k of keys) list.append(keyRow(t, k, refresh));

  flashCopy(copyBtn, () => secret || null);
  kcCreate.addEventListener("click", async () => {
    const label = labelI.value.trim();
    if (!label) { alert("Label is required."); return; }
    kcCreate.disabled = true;
    const body = { label };
    if (ttlI.value) body.expires_in_days = parseInt(ttlI.value, 10);
    try {
      const result = await apiFetch(`/admin/tenants/${t.id}/keys`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      secret = (result && result.key) || "";
      secretCode.textContent = secret;
      cfg.hidden = true; reveal.hidden = false;
    } catch (err) {
      kcCreate.disabled = false;
      alert("Issue key failed: " + err.message);
    }
  });
  doneBtn.addEventListener("click", () => {
    secret = null; secretCode.textContent = ""; // drop the plaintext before rebuilding
    form.hidden = true; toggle.classList.remove("open");
    refresh();
  });

  return sec;
}

function keyRow(t, k, refresh) {
  refresh = refresh || (() => renderTenantPanel(t.id)); // safe default
  const revoked = !!k.revoked_at;
  const row = el("div", { className: "key-row" + (revoked ? " revoked" : "") });

  const label = el("span", { className: "k" }, el("b", { textContent: k.prefix || "key" }));
  if (k.label) label.append(document.createTextNode(" · " + k.label));
  row.append(label);

  if (revoked) {
    row.append(el("span", { className: "pill pill--danger", textContent: "revoked " + (fmtDate(k.revoked_at) || "") }));
    row.append(el("div", { className: "row-actions" }));
    return row;
  }

  const rotate = el("button", { className: "btn", type: "button", textContent: "Rotate" });
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
  const revoke = el("button", { className: "btn btn--danger", type: "button", textContent: "Revoke" });
  wireDestructiveAction(revoke, {
    confirm: `Revoke key "${k.label || ""}" (${k.prefix || ""})?`,
    request: { path: `/admin/keys/${k.id}`, opts: { method: "DELETE" } },
    onDone: refresh,
    errorPrefix: "Revoke key failed",
  });
  row.append(el("div", { className: "row-actions" }, rotate, revoke));
  return row;
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
  flashCopy(copyBtn, () => key, {
    done: "Copied!",
    revert: "Copy",
    ms: 1500,
    addClass: null,
    onError: () => alert("Copy failed — select the key above and copy it manually."),
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
    wireChrome();
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
