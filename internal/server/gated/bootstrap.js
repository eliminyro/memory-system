// bootstrap.js — pre-auth /bootstrap provisioning form (design D1/D2). This
// page is served in place of a real front-end while the instance is
// un-bootstrapped (see internal/server/bootstrap.go: bootstrapGate /
// bootstrapPageHandler), so it is deliberately standalone: no sessionStorage
// token, no vendor libs (nothing here is untrusted markdown), just a form that
// fetches whether OAuth is configured and posts to the pre-auth POST
// /bootstrap endpoint.
//
// Everything on the page is derived from one signal — oauth_configured from
// GET /bootstrap/config.json. An always-visible status block reports it, and a
// pair of CSP-safe mode tabs ("MCP tokens" / "OAuth") present the same request
// two ways. The admin key is always issued; admin_email is sent only when the
// operator supplied it AND OAuth is configured. There is exactly ONE
// #bootstrap-admin-email input in the DOM: it lives in the OAuth panel and is
// physically relocated into the tokens panel when the "also enable OAuth"
// toggle is on, so there is never a duplicate id or a second submitted value.

function el(tag, attrs = {}, ...kids) {
  const n = document.createElement(tag);
  Object.assign(n, attrs);
  for (const k of kids) n.append(k);
  return n;
}

const form = document.getElementById("bootstrap-form");
const submitBtn = document.getElementById("bootstrap-submit");
const resultEl = document.getElementById("bootstrap-result");

const statusEl = document.getElementById("bootstrap-status");
const tokensBtn = document.getElementById("tab-tokens");
const oauthBtn = document.getElementById("tab-oauth");
const panelTokens = document.getElementById("panel-tokens");
const panelOauth = document.getElementById("panel-oauth");
const enableOAuthCheckbox = document.getElementById("bootstrap-enable-oauth");
const enableOAuthNote = document.getElementById("bootstrap-enable-oauth-note");
const oauthInfo = document.getElementById("bootstrap-oauth-info");
const emailField = document.getElementById("bootstrap-email-field");
const oauthEmailSlot = document.getElementById("oauth-email-slot");
const tokensEmailSlot = document.getElementById("tokens-email-slot");

let oauthConfigured = false;
let activeTab = "tokens";

// placeEmailField keeps the single admin-email field in the right panel and
// governs its visibility. When OAuth is configured it belongs in whichever
// panel the operator is looking at (the OAuth panel, or the tokens panel when
// the "also enable OAuth" toggle is on); otherwise it stays parked and hidden.
function placeEmailField() {
  if (!oauthConfigured) {
    oauthEmailSlot.appendChild(emailField);
    emailField.hidden = true;
    return;
  }
  if (activeTab === "tokens" && enableOAuthCheckbox.checked) {
    tokensEmailSlot.appendChild(emailField);
    emailField.hidden = false;
  } else {
    oauthEmailSlot.appendChild(emailField);
    emailField.hidden = activeTab !== "oauth";
  }
}

// selectTab toggles the two panels and gives the active tab a primary-button
// look. It is wired via addEventListener (no inline handlers) to stay CSP-safe.
function selectTab(name) {
  activeTab = name;
  const onTokens = name === "tokens";
  panelTokens.hidden = !onTokens;
  panelOauth.hidden = onTokens;
  // Active state is driven purely by aria-selected (styled in style.css .tab);
  // keep the tab class stable so they render as a tab strip, not buttons.
  tokensBtn.className = "tab";
  oauthBtn.className = "tab";
  tokensBtn.setAttribute("aria-selected", String(onTokens));
  oauthBtn.setAttribute("aria-selected", String(!onTokens));
  placeEmailField();
}

// loadConfig fetches whether OAuth login is configured on this instance
// (GET /bootstrap/config.json, mirroring the authenticated UI's
// /ui/config.json) and renders the whole page from that one boolean: the
// status block copy, the tokens-panel toggle (enabled vs disabled-with-note),
// the OAuth panel (admin-email field vs env-setup info), and the default tab.
async function loadConfig() {
  try {
    const cfg = await (await fetch("/bootstrap/config.json")).json();
    oauthConfigured = !!cfg.oauth_configured;
  } catch {
    oauthConfigured = false;
  }

  // OAuth status is a compact green/red badge, not a paragraph. The
  // consequence (OAuth gates /ui + ACL management) is stated in the tokens
  // panel copy, so the status block itself stays minimal. Reuses the shared
  // badge classes already in style.css (import-status-succeeded/-failed).
  statusEl.replaceChildren(
    "OAuth ",
    el("span", {
      className: oauthConfigured
        ? "import-status-badge import-status-succeeded"
        : "import-status-badge import-status-failed",
      textContent: oauthConfigured ? "enabled" : "disabled",
    }),
  );

  // Tokens panel: the "also enable OAuth" toggle only works when OAuth is
  // actually configured; otherwise it is disabled with an explanatory note.
  enableOAuthCheckbox.disabled = !oauthConfigured;
  if (!oauthConfigured) enableOAuthCheckbox.checked = false;
  enableOAuthNote.hidden = oauthConfigured;

  // OAuth panel: the founding admin-email field when configured, otherwise
  // informational copy on which env vars to set and restart.
  if (oauthConfigured) {
    oauthInfo.hidden = true;
  } else {
    oauthInfo.replaceChildren(
      "OAuth is not configured. Set ",
      el("code", { textContent: "MEMORY_MCP_GOOGLE_CLIENT_ID" }),
      " and ",
      el("code", { textContent: "MEMORY_MCP_GOOGLE_CLIENT_SECRET" }),
      " (plus ",
      el("code", { textContent: "AUTHLET_MASTER_KEY" }),
      " and ",
      el("code", { textContent: "PUBLIC_BASE_URL" }),
      ") on the server and restart to enable the /ui console. In-app OAuth setup is coming in a future release.",
    );
    oauthInfo.hidden = false;
  }

  // Default tab: OAuth when configured, else MCP tokens.
  selectTab(oauthConfigured ? "oauth" : "tokens");
}

function showError(msg) {
  resultEl.replaceChildren(el("p", { className: "state-msg state-err", textContent: msg }));
}

// mcpConfigJSON returns a generic `mcpServers` config block for an MCP client.
// Most coding agents (Claude Code, Cursor, Windsurf, …) share this shape; field
// names vary slightly (type/transport), but the URL — and, for key auth, the
// Authorization header — are the constants. withKey uses a placeholder, never
// the real key (which is offered via the Copy button).
function mcpConfigJSON(url, withKey) {
  const server = withKey
    ? { type: "http", url, headers: { Authorization: "Bearer <admin key>" } }
    : { type: "http", url };
  return JSON.stringify({ mcpServers: { memory: server } }, null, 2);
}

// keyCopyControls offers the one-time admin key via a Copy button rather than
// printing it to the screen. On success it confirms "Copied"; if the Clipboard
// API is unavailable or blocked it reveals the key inline as a fallback, so a
// credential that is issued exactly once is never lost.
function keyCopyControls(key) {
  const box = el("div", { className: "keybox" });
  const btn = el("button", { className: "sec-btn sec-btn-primary", type: "button", textContent: "Copy admin key" });
  const status = el("span", { className: "meta" });
  btn.addEventListener("click", async () => {
    try {
      await navigator.clipboard.writeText(key);
      status.textContent = "Copied ✓";
    } catch {
      status.textContent = "Copy unavailable — select and copy manually:";
      box.append(el("code", { className: "modal-key", textContent: key }));
    }
  });
  box.append(btn, status);
  return box;
}

// showSuccess replaces the form with the bootstrap result. The admin key is
// issued exactly once; it is offered via a Copy button (keyCopyControls), not
// printed, and never stored. When OAuth is configured the screen LEADS with the
// /ui login and frames the key as a secondary break-glass credential; without
// OAuth the key is the primary way in.
function showSuccess(resp, adminEmail) {
  form.hidden = true;
  const origin = window.location.origin;
  const wrap = el("div", { className: "setup-result-ok" });
  wrap.append(el("h2", { textContent: "Bootstrap complete" }));

  if (oauthConfigured) {
    // Primary path: sign in via OAuth.
    const who = adminEmail ? ` as ${adminEmail}` : "";
    wrap.append(el("p", {},
      "Sign in to ", el("a", { href: "/ui/", textContent: "the web console" }), who,
      " to browse memory and manage tenants, users, and ACLs."));
    wrap.append(el("p", { className: "meta" },
      "To connect an MCP client, point it at ", el("code", { textContent: origin + "/mcp" }),
      " — clients that support OAuth need only the URL, no token (field names vary by client):"));
    wrap.append(el("pre", { className: "code-block", textContent: mcpConfigJSON(origin + "/mcp", false) }));
    // Secondary path: the break-glass key.
    wrap.append(el("h3", { textContent: "Break-glass admin key" }));
    wrap.append(el("p", { className: "meta" },
      "A root credential for recovery if OAuth login ever fails, and for CI or MCP clients that don't use OAuth. Issued once and never retrievable again — copy it somewhere safe now, or discard it."));
    wrap.append(keyCopyControls(resp.api_key || ""));
    wrap.append(el("p", { className: "meta", textContent: "To use it, add an Authorization header instead of the OAuth flow:" }));
    wrap.append(el("pre", { className: "code-block", textContent: mcpConfigJSON(origin + "/mcp", true) }));
  } else {
    // No OAuth: the key is the only way in.
    wrap.append(el("p", { className: "modal-warn" },
      "Your admin API key — a Bearer token for the MCP and HTTP API. Issued once and never retrievable again; copy it now."));
    wrap.append(keyCopyControls(resp.api_key || ""));
    wrap.append(el("p", { className: "meta", textContent: "Then drop it into your MCP client config (field names vary by client):" }));
    wrap.append(el("pre", { className: "code-block", textContent: mcpConfigJSON(origin + "/mcp", true) }));
  }

  wrap.append(el("p", {
    className: "meta",
    textContent: `tenant ${resp.tenant_id || ""} · key ${resp.key_id || ""}`,
  }));
  resultEl.replaceChildren(wrap);
}

tokensBtn.addEventListener("click", () => selectTab("tokens"));
oauthBtn.addEventListener("click", () => selectTab("oauth"));
enableOAuthCheckbox.addEventListener("change", placeEmailField);

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  submitBtn.disabled = true;
  resultEl.replaceChildren();

  const body = { token: document.getElementById("bootstrap-token").value };
  // Send admin_email only when the single field is present, visible, and
  // non-empty AND OAuth is configured — matching shouldSeedAdminEmail server-side.
  if (oauthConfigured && !emailField.hidden) {
    const email = document.getElementById("bootstrap-admin-email").value.trim();
    if (email) body.admin_email = email;
  }

  try {
    const res = await fetch("/bootstrap", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      if (res.status === 409) {
        showError("This instance is already bootstrapped.");
      } else if (res.status === 403) {
        showError(data.error || "Bootstrap forbidden — check the token.");
      } else {
        showError(data.error || `Bootstrap failed (HTTP ${res.status}).`);
      }
      submitBtn.disabled = false;
      return;
    }
    showSuccess(data, body.admin_email);
  } catch (err) {
    showError("Bootstrap request failed: " + err.message);
    submitBtn.disabled = false;
  }
});

loadConfig();
