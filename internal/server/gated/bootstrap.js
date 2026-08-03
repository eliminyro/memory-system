// bootstrap.js — pre-auth /bootstrap provisioning form (design D3/D4). This
// page is served in place of a real front-end while the instance is
// un-bootstrapped (see internal/server/bootstrap.go: bootstrapGate /
// bootstrapPageHandler), so it is deliberately standalone: no sessionStorage
// token, no vendor libs (nothing here is untrusted markdown), just a form that
// fetches whether OAuth is configured (to show/hide the admin-email field)
// and posts to the pre-auth POST /bootstrap endpoint.

function el(tag, attrs = {}, ...kids) {
  const n = document.createElement(tag);
  Object.assign(n, attrs);
  for (const k of kids) n.append(k);
  return n;
}

const form = document.getElementById("bootstrap-form");
const submitBtn = document.getElementById("bootstrap-submit");
const resultEl = document.getElementById("bootstrap-result");
const emailField = document.getElementById("bootstrap-email-field");

let oauthConfigured = false;

// loadConfig fetches whether OAuth login is configured on this instance
// (GET /bootstrap/config.json, mirroring the authenticated UI's
// /ui/config.json) and shows/hides the admin-email field accordingly — an
// email is moot if there is no OAuth login to use it with.
async function loadConfig() {
  try {
    const cfg = await (await fetch("/bootstrap/config.json")).json();
    oauthConfigured = !!cfg.oauth_configured;
  } catch {
    oauthConfigured = false;
  }
  emailField.hidden = !oauthConfigured;
}

function showError(msg) {
  resultEl.replaceChildren(el("p", { className: "state-msg state-err", textContent: msg }));
}

// showSuccess replaces the form with the one-time admin key display. The key
// lives only in this response object and the visible <code> element — it is
// never written to storage, matching the authenticated UI's key modal
// (app.js showKeyModal). The follow-up pointer depends on whether OAuth login
// is available: log in via /ui, or use the key directly against MCP/API.
function showSuccess(resp) {
  form.hidden = true;
  const wrap = el("div", { className: "setup-result-ok" });
  wrap.append(el("h2", { textContent: "Bootstrap complete" }));
  wrap.append(el("p", {
    className: "modal-warn",
    textContent: "Copy this admin API key now — it is shown only once and cannot be retrieved again.",
  }));
  wrap.append(el("code", { className: "modal-key", textContent: resp.api_key || "" }));
  wrap.append(el("p", {
    className: "meta",
    textContent: `tenant ${resp.tenant_id || ""} · key ${resp.key_id || ""}`,
  }));
  const next = oauthConfigured
    ? el("p", {}, "Store the key somewhere safe, then ", el("a", { href: "/ui/", textContent: "log in" }), " to continue.")
    : el("p", {}, "Store the key somewhere safe, then use it as a Bearer token against the MCP/API endpoints.");
  wrap.append(next);
  resultEl.replaceChildren(wrap);
}

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  submitBtn.disabled = true;
  resultEl.replaceChildren();

  const body = { token: document.getElementById("bootstrap-token").value };
  if (oauthConfigured) {
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
