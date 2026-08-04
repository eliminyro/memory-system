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

// showSuccess replaces the form with the one-time admin key display. The key
// lives only in this response object and the visible <code> element — it is
// never written to storage, matching the authenticated UI's key modal
// (app.js showKeyModal). The follow-up copy depends on whether OAuth login is
// available: when it is, the key is an MCP/API Bearer credential, /ui is a
// usable console, and MCP clients may connect via key OR OAuth; when it is not,
// the key is the only way in (Bearer token against MCP/API).
function showSuccess(resp) {
  form.hidden = true;
  const wrap = el("div", { className: "setup-result-ok" });
  wrap.append(el("h2", { textContent: "Bootstrap complete" }));
  wrap.append(el("p", {
    className: "modal-warn",
    textContent: "Copy this key now — it is shown only once and cannot be retrieved again.",
  }));
  wrap.append(el("p", {
    className: "meta",
    textContent: oauthConfigured ? "This is your admin MCP/API Bearer key:" : "This is your admin API key:",
  }));
  wrap.append(el("code", { className: "modal-key", textContent: resp.api_key || "" }));
  wrap.append(el("p", {
    className: "meta",
    textContent: `tenant ${resp.tenant_id || ""} · key ${resp.key_id || ""}`,
  }));

  if (oauthConfigured) {
    const origin = window.location.origin;
    wrap.append(el("p", {},
      "Log in to ", el("a", { href: "/ui/", textContent: "the web console" }),
      " — browse memory and manage tenants, users, and ACLs."));
    wrap.append(el("p", { className: "meta" },
      "To connect an MCP client, point it at ", el("code", { textContent: origin + "/mcp" }), " and either:"));
    const ways = el("ul");
    ways.append(el("li", { textContent: "use this key as a Bearer token, or" }));
    ways.append(el("li", { textContent: "complete the OAuth login — no static token to manage." }));
    wrap.append(ways);
  } else {
    wrap.append(el("p", {}, "Store the key somewhere safe, then use it as a Bearer token against the MCP/API endpoints."));
  }

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
    showSuccess(data);
  } catch (err) {
    showError("Bootstrap request failed: " + err.message);
    submitBtn.disabled = false;
  }
});

loadConfig();
