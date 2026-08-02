// setup.js — pre-auth bootstrap form (Task 8.1). This page is served in place
// of the normal SPA shell while the instance is un-bootstrapped (see
// internal/server/bootstrap.go: uiGateHandler / serveSetupView), so it is
// deliberately standalone: no sessionStorage token, no vendor libs
// (marked/purify — nothing here is untrusted markdown), just a form posting
// to the pre-auth POST /api/bootstrap endpoint.

function el(tag, attrs = {}, ...kids) {
  const n = document.createElement(tag);
  Object.assign(n, attrs);
  for (const k of kids) n.append(k);
  return n;
}

const form = document.getElementById("setup-form");
const submitBtn = document.getElementById("setup-submit");
const resultEl = document.getElementById("setup-result");

function showError(msg) {
  resultEl.replaceChildren(el("p", { className: "state-msg state-err", textContent: msg }));
}

// showSuccess replaces the form with the one-time admin key display. The key
// lives only in this response object and the visible <code> element — it is
// never written to storage, matching the authenticated UI's key modal
// (app.js showKeyModal).
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
  wrap.append(el("p", {},
    "Store the key somewhere safe, then ",
    el("a", { href: "/ui/", textContent: "log in" }),
    " to continue.",
  ));
  resultEl.replaceChildren(wrap);
}

form.addEventListener("submit", async (e) => {
  e.preventDefault();
  submitBtn.disabled = true;
  resultEl.replaceChildren();

  const body = { token: document.getElementById("setup-token").value };
  const tenantName = document.getElementById("setup-tenant-name").value.trim();
  const tenantEmail = document.getElementById("setup-tenant-email").value.trim();
  const keyLabel = document.getElementById("setup-key-label").value.trim();
  if (tenantName) body.tenant_name = tenantName;
  if (tenantEmail) body.tenant_email = tenantEmail;
  if (keyLabel) body.key_label = keyLabel;

  try {
    const res = await fetch("/api/bootstrap", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(body),
    });
    const data = await res.json().catch(() => ({}));
    if (!res.ok) {
      if (res.status === 409) {
        showError("This instance is already bootstrapped. Log in instead.");
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
