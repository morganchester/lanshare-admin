const state = { shares: [], devices: [], users: [], recovery_devices: [], me: null };
let messages = {};
let locale = localStorage.getItem("lanshare.locale") || (navigator.language?.startsWith("en") ? "en-US" : "ru-RU");
let modalReturnFocus = null;
const $ = (selector) => document.querySelector(selector);

document.addEventListener("DOMContentLoaded", async () => {
  await loadLocale(locale);
  bindStaticEvents();
  await loadState();
});

async function loadLocale(next) {
  const response = await fetch(`/api/locales/${encodeURIComponent(next)}`);
  const data = await response.json();
  locale = data.locale;
  messages = data.messages;
  localStorage.setItem("lanshare.locale", locale);
  document.documentElement.lang = locale;
  document.title = t("app.title");
  document.querySelectorAll("[data-i18n]").forEach((node) => { node.textContent = t(node.dataset.i18n); });
  document.querySelectorAll("[data-i18n-aria]").forEach((node) => { node.setAttribute("aria-label", t(node.dataset.i18nAria)); });
  const select = $("#languageSelect");
  select.replaceChildren(option("ru-RU", t("language.ru-RU")), option("en-US", t("language.en-US")));
  select.value = locale;
}

function bindStaticEvents() {
  $("#logoutButton").addEventListener("click", async () => {
    await api("/api/auth/logout", { method: "POST" });
    showLogin();
  });
  $("#languageSelect").addEventListener("change", async (event) => {
    await loadLocale(event.target.value);
    if (!$("#appShell").classList.contains("hidden")) renderRoute(); else showLogin();
  });
  document.querySelectorAll("[data-route]").forEach((link) => link.addEventListener("click", (event) => {
    event.preventDefault(); history.pushState(null, "", link.pathname); renderRoute();
  }));
  window.addEventListener("popstate", renderRoute);
  $("#closeModal").addEventListener("click", closeModal);
  $("#modal").addEventListener("click", (event) => { if (event.target === $("#modal")) closeModal(); });
  document.addEventListener("keydown", (event) => { if (event.key === "Escape" && !$("#modal").classList.contains("hidden")) closeModal(); });
}

async function loadState() {
  const result = await api("/api/state");
  if (result.status === 401) return showLogin();
  if (!result.ok) return toast(result.error || t("common.error"));
  Object.assign(state, result.data);
  showApp(); renderRoute();
}

function showLogin() {
  $("#appShell").classList.add("hidden");
  const view = $("#authView");
  view.classList.remove("hidden"); view.classList.add("flex");
  const panel = el("section", "panel w-full p-7 sm:p-9");
  panel.innerHTML = `<div class="mb-6 flex h-12 w-12 items-center justify-center rounded-2xl bg-moss text-xl text-white">⌁</div><h1 class="text-2xl font-semibold">${t("login.title")}</h1><p class="mt-2 text-sm leading-6 text-black/55">${t("login.subtitle")}</p>`;
  const form = el("form", "mt-6 space-y-4");
  form.innerHTML = `<input name="username" class="field" autocomplete="username" aria-label="${t("login.username")}" placeholder="${t("login.username")}" required><input name="password" class="field" type="password" autocomplete="current-password" aria-label="${t("login.password")}" placeholder="${t("login.password")}" required><label class="flex items-start gap-3 text-sm text-black/60"><input name="trust" type="checkbox" checked class="mt-0.5 h-4 w-4 accent-[#315b48]"><span>${t("login.rememberRecovery")}</span></label><p data-error class="hidden text-sm text-red-700"></p><button class="btn-primary w-full" type="submit">${t("login.submit")}</button>`;
  form.addEventListener("submit", async (event) => {
    event.preventDefault(); const data = new FormData(form);
    const result = await api("/api/auth/login", { method: "POST", body: JSON.stringify({ username: data.get("username"), password: data.get("password"), trust: data.get("trust") === "on", label: navigator.platform || t("recovery.browser") }) });
    if (!result.ok) return formError(form, result.error || t("login.invalid"));
    await loadState();
  });
  const forgot = button(t("login.forgot"), "mt-5 w-full text-center text-sm font-semibold text-moss", showRecovery);
  panel.append(form, forgot); view.replaceChildren(panel);
}

async function showRecovery() {
  const result = await api("/api/auth/recovery", { unauthenticated: true });
  const available = result.ok && result.data.available;
  const panel = el("section", "panel w-full p-7 sm:p-9");
  panel.innerHTML = `<h1 class="text-2xl font-semibold">${t("recovery.title")}</h1><p class="mt-2 text-sm leading-6 text-black/55">${t("recovery.subtitle")}</p><div class="mt-5 rounded-2xl border p-4 text-sm ${available ? "border-emerald-200 bg-emerald-50 text-emerald-900" : "border-amber-200 bg-amber-50 text-amber-950"}">${t(available ? "recovery.available" : "recovery.unavailable")}</div>`;
  if (available) {
    const form = el("form", "mt-5 space-y-4");
    form.innerHTML = `<input name="username" class="field" autocomplete="username" aria-label="${t("login.username")}" placeholder="${t("login.username")}" required><input name="password" type="password" class="field" autocomplete="new-password" minlength="12" aria-label="${t("recovery.newPassword")}" placeholder="${t("recovery.newPassword")}" required><input name="confirm" type="password" class="field" autocomplete="new-password" minlength="12" aria-label="${t("recovery.confirmPassword")}" placeholder="${t("recovery.confirmPassword")}" required><p data-error class="hidden text-sm text-red-700"></p><button class="btn-primary w-full" type="submit">${t("recovery.submit")}</button>`;
    form.addEventListener("submit", async (event) => {
      event.preventDefault(); const data = new FormData(form);
      if (data.get("password") !== data.get("confirm")) return formError(form, t("errors.passwordMismatch"));
      const response = await api("/api/auth/recover", { method: "POST", body: JSON.stringify({ username: data.get("username"), password: data.get("password") }), unauthenticated: true });
      if (!response.ok) return formError(form, response.error);
      toast(t("recovery.success")); showLogin();
    });
    panel.append(form);
  }
  panel.append(button(t("recovery.back"), "mt-5 w-full text-center text-sm font-semibold text-moss", showLogin));
  $("#authView").replaceChildren(panel);
}

function showApp() {
  $("#authView").classList.add("hidden"); $("#authView").classList.remove("flex"); $("#appShell").classList.remove("hidden");
}

function renderRoute() {
  document.querySelectorAll("[data-route]").forEach((link) => link.classList.toggle("active", link.pathname === location.pathname));
  if (location.pathname === "/users") return renderUsers();
  if (location.pathname === "/shares") return renderShares();
  renderDashboard();
}

function pageHeader(title, subtitle, action) {
  const header = el("header", "flex flex-col justify-between gap-4 sm:flex-row sm:items-end");
  const text = el("div"); text.append(el("h1", "text-3xl font-semibold tracking-tight", title), el("p", "mt-2 text-sm text-black/50", subtitle));
  header.append(text); if (action) header.append(action); return header;
}

function renderDashboard() {
  const page = $("#page");
  const stats = el("div", "grid gap-4 sm:grid-cols-2 xl:grid-cols-4");
  [["dashboard.devices", state.devices.length], ["dashboard.shares", state.shares.length], ["dashboard.admins", state.users.length], ["dashboard.recoveryDevices", state.recovery_devices.length]].forEach(([key, value]) => {
    const card = el("article", "panel p-5"); card.append(el("p", "text-xs font-bold uppercase tracking-[.14em] text-black/40", t(key)), el("strong", "mt-3 block text-4xl font-semibold", String(value))); stats.append(card);
  });
  page.replaceChildren(pageHeader(t("dashboard.title"), t("dashboard.subtitle"), button(t("dashboard.passwordRecovery"), "btn-primary", showSecuritySettings)), stats);
}

function showSecuritySettings() {
  const body = el("div", "space-y-5");
  const password = el("div", "rounded-2xl border border-black/10 bg-white p-4");
  password.append(el("h3", "font-semibold", t("dashboard.changePassword")), el("p", "mt-1 text-sm text-black/45", t("dashboard.securityText")), button(t("dashboard.changePassword"), "btn-secondary mt-4", showChangePassword));
  const trusted = el("div", "mt-5 space-y-2");
  trusted.append(el("h3", "text-sm font-semibold", t("recovery.trustedTitle")), el("p", "text-xs text-black/40", t("recovery.trustedHint")));
  if (!state.recovery_devices.length) trusted.append(el("p", "rounded-xl bg-black/4 p-3 text-sm text-black/45", t("recovery.empty")));
  state.recovery_devices.forEach((item) => {
    const row = el("div", "flex items-center justify-between gap-3 rounded-xl border border-black/8 bg-white/60 p-3");
    row.append(el("span", "text-sm", item.label), button(t("recovery.remove"), "text-xs font-semibold text-red-700", async () => { const result = await api(`/api/recovery-devices/${item.id}`, { method: "DELETE" }); if (!result.ok) return toast(result.error); await refresh(); showSecuritySettings(); })); trusted.append(row);
  });
  body.append(password, trusted);
  modal(t("dashboard.passwordRecovery"), t("recovery.managementHint"), body);
}

function renderUsers() {
  const page = $("#page");
  const devicesPanel = el("section", "panel overflow-hidden");
  devicesPanel.append(sectionHeader(t("users.devicesTitle"), button(`+ ${t("users.addDevice")}`, "btn-primary", showAddDevice)));
  const deviceList = el("div", "divide-y divide-black/8");
  if (!state.devices.length) deviceList.append(emptyState(t("users.emptyDevices")));
  state.devices.forEach((device) => deviceList.append(deviceRow(device)));
  devicesPanel.append(deviceList);
  const adminsPanel = el("section", "panel overflow-hidden");
  adminsPanel.append(sectionHeader(t("users.adminsTitle"), button(`+ ${t("users.addAdmin")}`, "btn-secondary", showAddAdmin)));
  const adminList = el("div", "divide-y divide-black/8");
  state.users.forEach((user) => adminList.append(adminRow(user)));
  adminsPanel.append(adminList);
  page.replaceChildren(pageHeader(t("users.title"), t("users.subtitle")), devicesPanel, adminsPanel);
}

function renderShares() {
  const page = $("#page");
  const grid = el("div", "grid gap-4 sm:grid-cols-2");
  state.shares.forEach((share) => grid.append(shareCard(share)));
  if (!state.shares.length) grid.append(emptyState(t("shares.empty")));
  page.replaceChildren(pageHeader(t("shares.title"), t("shares.subtitle"), button(`+ ${t("shares.add")}`, "btn-primary", showAddShare)), grid);
}

function sectionHeader(title, action) { const node = el("div", "flex items-center justify-between gap-4 border-b border-black/8 px-6 py-5"); node.append(el("h2", "text-lg font-semibold", title), action); return node; }
function emptyState(text) { return el("div", "p-10 text-center text-sm text-black/40", text); }

function deviceRow(device) {
  const row = el("article", "flex flex-col gap-4 px-6 py-5 sm:flex-row sm:items-center sm:justify-between");
  const info = el("div", "min-w-0"); const title = el("div", "flex flex-wrap items-center gap-2");
  title.append(el("h3", "font-semibold", device.name), el("span", "rounded-full bg-black/5 px-2 py-0.5 text-[11px] font-semibold text-black/50", t(`platform.${device.platform || "other"}`)));
  info.append(title, el("p", "mt-1 font-mono text-xs text-black/40", device.username));
  const chips = el("div", "mt-3 flex flex-wrap gap-2"); device.shares.forEach((id) => chips.append(el("span", "chip", state.shares.find((item) => item.id === id)?.name || id))); info.append(chips);
  const actions = el("div", "flex flex-wrap gap-2");
  actions.append(button(t("users.editShares"), "btn-secondary", () => showShareEditor(device)), button(t("users.rotate"), "btn-secondary", () => rotateDevice(device)), button(t("users.revoke"), "rounded-xl px-3 py-2.5 text-sm font-semibold text-red-700 hover:bg-red-50", () => revokeDevice(device)));
  row.append(info, actions); return row;
}

function adminRow(user) {
  const row = el("article", "flex flex-col gap-3 px-6 py-5 sm:flex-row sm:items-center sm:justify-between");
  const info = el("div"); const title = el("div", "flex items-center gap-2"); title.append(el("h3", "font-semibold", user.username));
  if (state.me.id === user.id) title.append(el("span", "chip", t("admin.current")));
  info.append(title, el("p", "mt-1 text-xs text-black/40", `${t("users.lastLogin")}: ${user.last_login ? formatDate(user.last_login) : t("users.noLastLogin")}`));
  const actions = el("div", "flex gap-2");
  if (state.me.id !== user.id) actions.append(button(t("users.resetPassword"), "btn-secondary", () => resetAdmin(user)), button(t("common.delete"), "rounded-xl px-3 py-2.5 text-sm font-semibold text-red-700 hover:bg-red-50", () => deleteAdmin(user)));
  row.append(info, actions); return row;
}

function shareCard(share) {
  const card = el("article", "panel p-5"); const top = el("div", "flex items-start justify-between gap-3"); const info = el("div", "min-w-0");
  info.append(el("h3", "font-semibold", share.name), el("p", "mt-1 truncate font-mono text-xs text-black/40", share.unc));
  top.append(info, el("span", share.read_only ? "chip" : "rounded-full bg-clay/10 px-2.5 py-1 text-xs font-medium text-clay", t(share.read_only ? "common.readOnly" : "common.readWrite"))); card.append(top);
  if (share.path) card.append(el("p", "mt-3 truncate text-xs text-black/45", share.path));
  const footer = el("div", "mt-4 flex items-center justify-between border-t border-black/8 pt-3"); footer.append(el("span", "text-xs text-black/35", t(share.managed ? "shares.managed" : "shares.external")));
  if (share.managed) footer.append(button(t("common.delete"), "text-xs font-semibold text-red-700", () => removeShare(share))); card.append(footer); return card;
}

function showChangePassword() {
  modal(t("dashboard.changePassword"), "", passwordForm(true, async (form, data) => {
    const result = await api("/api/auth/password", { method: "POST", body: JSON.stringify({ current: data.get("current"), password: data.get("password") }) });
    if (!result.ok) return formError(form, result.error); closeModal(); toast(t("password.changed")); showLogin();
  }));
}

function passwordForm(withCurrent, submit) {
  const form = el("form", "space-y-4");
  form.innerHTML = `${withCurrent ? `<input name="current" type="password" class="field" autocomplete="current-password" aria-label="${t("password.current")}" placeholder="${t("password.current")}" required>` : ""}<input name="password" type="password" class="field" autocomplete="new-password" minlength="12" aria-label="${t("password.new")}" placeholder="${t("password.new")}" required><input name="confirm" type="password" class="field" autocomplete="new-password" minlength="12" aria-label="${t("password.confirm")}" placeholder="${t("password.confirm")}" required><p data-error class="hidden text-sm text-red-700"></p><button class="btn-primary w-full" type="submit">${t("common.save")}</button>`;
  form.addEventListener("submit", async (event) => { event.preventDefault(); const data = new FormData(form); if (data.get("password") !== data.get("confirm")) return formError(form, t("errors.passwordMismatch")); await submit(form, data); }); return form;
}

function showAddAdmin() {
  const form = el("form", "space-y-4"); form.innerHTML = `<input name="username" class="field" pattern="[a-z0-9._-]{3,32}" placeholder="${t("login.username")}" required><p class="text-xs text-black/40">${t("admin.usernameHint")}</p><input name="password" type="password" class="field" minlength="12" placeholder="${t("admin.passwordOptional")}"><p class="text-xs text-black/40">${t("admin.passwordHint")}</p><p data-error class="hidden text-sm text-red-700"></p><button class="btn-primary w-full" type="submit">${t("admin.create")}</button>`;
  form.addEventListener("submit", async (event) => { event.preventDefault(); const data = new FormData(form); const result = await api("/api/users", { method: "POST", body: JSON.stringify({ username: data.get("username"), password: data.get("password") }) }); if (!result.ok) return formError(form, result.error); if (result.data.password) showGeneratedPassword(result.data.user.username, result.data.password); else { closeModal(); await loadState(); } });
  modal(t("admin.newTitle"), t("admin.passwordHint"), form);
}

async function resetAdmin(user) { if (!confirm(fmt("admin.resetConfirm", user.username))) return; const result = await api(`/api/users/${user.id}/reset`, { method: "POST" }); if (!result.ok) return toast(result.error); showGeneratedPassword(user.username, result.data.password); await refresh(); }
async function deleteAdmin(user) { if (!confirm(fmt("admin.deleteConfirm", user.username))) return; const result = await api(`/api/users/${user.id}`, { method: "DELETE" }); if (!result.ok) return toast(result.error); await loadState(); }
function showGeneratedPassword(username, password) { const body = el("div", "space-y-4"); body.append(el("p", "text-sm text-black/55", t("users.generatedHint")), secretLine(t("login.username"), username), secretLine(t("users.generatedPassword"), password), button(t("common.done"), "btn-primary w-full", closeModal)); modal(t("users.generatedPassword"), "", body); }

function showAddDevice() {
  const form = el("form", "space-y-5"); form.innerHTML = `<label class="block"><span class="mb-2 block text-sm font-semibold">${t("device.name")}</span><input name="name" class="field" pattern="[a-z][a-z0-9-]{0,22}" placeholder="${t("device.nameExample")}" required><span class="mt-1.5 block text-xs text-black/40">${t("device.nameHint")}</span></label><label class="block"><span class="mb-2 block text-sm font-semibold">${t("users.platform")}</span><select name="platform" class="field"><option value="windows">${t("platform.windows")}</option><option value="macos">${t("platform.macos")}</option><option value="linux">${t("platform.linux")}</option><option value="other">${t("platform.other")}</option></select></label><fieldset><legend class="mb-2 text-sm font-semibold">${t("device.selectShares")}</legend><div data-share-options class="space-y-2"></div></fieldset><p data-error class="hidden text-sm text-red-700"></p><button class="btn-primary w-full" type="submit">${t("device.create")}</button>`;
  fillShareOptions(form.querySelector("[data-share-options]"), state.shares.map((item) => item.id));
  form.addEventListener("submit", async (event) => { event.preventDefault(); const data = new FormData(form); const result = await api("/api/devices", { method: "POST", body: JSON.stringify({ name: data.get("name"), platform: data.get("platform"), shares: data.getAll("shares") }) }); if (!result.ok) return formError(form, result.error); showCredential(result.data); await refresh(); });
  modal(t("device.newTitle"), t("device.newSubtitle"), form);
}

function showShareEditor(device) {
  const form = el("form", "space-y-5"); const options = el("div", "space-y-2"); fillShareOptions(options, device.shares); const error = el("p", "hidden text-sm text-red-700"); form.append(options, error, button(t("common.save"), "btn-primary w-full", null, "submit"));
  form.addEventListener("submit", async (event) => { event.preventDefault(); const result = await api(`/api/devices/${encodeURIComponent(device.name)}/shares`, { method: "PUT", body: JSON.stringify({ shares: new FormData(form).getAll("shares") }) }); if (!result.ok) return formError(form, result.error); closeModal(); toast(t("device.sharesUpdated")); await loadState(); });
  modal(`${t("users.allowedShares")} · ${device.name}`, t("device.newSubtitle"), form);
}

async function rotateDevice(device) { if (!confirm(fmt("device.rotateConfirm", device.name))) return; const result = await api(`/api/devices/${encodeURIComponent(device.name)}/rotate`, { method: "POST" }); if (!result.ok) return toast(result.error); showCredential(result.data); await refresh(); }
async function revokeDevice(device) { if (!confirm(fmt("device.revokeConfirm", device.name))) return; const result = await api(`/api/devices/${encodeURIComponent(device.name)}`, { method: "DELETE" }); if (!result.ok) return toast(result.error); toast(t("device.revoked")); await loadState(); }

function showCredential(result) {
  const body = el("div", "space-y-5"); body.append(el("div", "rounded-2xl border border-amber-300/60 bg-amber-50 p-4 text-sm text-amber-950", t("device.credentialOnce")));
  const credentials = el("div", "space-y-3 rounded-2xl border border-black/10 bg-white p-4"); credentials.append(secretLine(t("device.username"), result.device.username), secretLine(t("device.key"), result.password)); body.append(credentials);
  state.shares.filter((share) => result.device.shares.includes(share.id)).forEach((share) => { const command = setupCommand(result.device.platform, share.unc, result.device.username, result.password); const block = el("div", "rounded-2xl bg-ink p-4 text-white"); const header = el("div", "mb-2 flex items-center justify-between"); header.append(el("span", "text-xs font-bold uppercase tracking-wider text-white/50", share.name), button(t("common.copy"), "text-xs font-semibold text-[#d7dfb9]", () => copyText(command))); block.append(header, el("pre", "overflow-x-auto whitespace-pre-wrap break-all font-mono text-xs leading-5 text-white/85", command)); body.append(block); });
  body.append(button(t("common.done"), "btn-primary w-full", closeModal)); modal(t("device.credentialTitle"), t("device.credentialOnce"), body);
}

async function showAddShare() {
  const form = el("form", "space-y-5"); form.innerHTML = `<div class="grid gap-4 sm:grid-cols-2"><label><span class="mb-2 block text-sm font-semibold">${t("shares.networkName")}</span><input name="name" class="field" maxlength="64" required></label><label><span class="mb-2 block text-sm font-semibold">${t("shares.systemID")}</span><input name="id" class="field" pattern="[a-z][a-z0-9-]{0,22}" required><span class="mt-1.5 block text-xs text-black/40">${t("shares.systemIDHint")}</span></label></div><div><span class="mb-2 block text-sm font-semibold">${t("shares.serverDirectory")}</span><input data-path class="field font-mono" readonly><div data-browser class="mt-2 max-h-56 overflow-y-auto rounded-2xl border border-black/10 bg-white"></div><button data-use-path class="btn-secondary mt-2 w-full" type="button">${t("shares.useDirectory")}</button></div><label class="flex items-center gap-3 rounded-2xl border border-black/10 bg-white p-4"><input name="read_only" type="checkbox" class="h-4 w-4 accent-[#315b48]"><span><strong class="block text-sm">${t("common.readOnly")}</strong><span class="text-xs text-black/40">${t("shares.readOnlyHint")}</span></span></label><p data-error class="hidden text-sm text-red-700"></p><button data-submit class="btn-primary w-full" type="submit" disabled>${t("shares.create")}</button>`;
  let currentPath = ""; const pathInput = form.querySelector("[data-path]"); const browser = form.querySelector("[data-browser]"); const submit = form.querySelector("[data-submit]");
  async function browse(path = "") { browser.replaceChildren(el("p", "p-4 text-sm text-black/40", t("common.loading"))); const result = await api(`/api/directories?path=${encodeURIComponent(path)}`); if (!result.ok) return formError(form, result.error); currentPath = result.data.path; pathInput.value = currentPath; browser.replaceChildren(...result.data.directories.map((dir) => button(dir.name === ".." ? `↑ ${t("shares.up")}` : `▱ ${dir.name}`, "block w-full border-b border-black/8 px-4 py-3 text-left text-sm last:border-0 hover:bg-black/5", () => browse(dir.path)))); }
  form.querySelector("[data-use-path]").addEventListener("click", () => { submit.disabled = false; toast(t("shares.directorySelected")); });
  form.addEventListener("submit", async (event) => { event.preventDefault(); const data = new FormData(form); const result = await api("/api/shares", { method: "POST", body: JSON.stringify({ id: data.get("id"), name: data.get("name"), path: currentPath, read_only: data.get("read_only") === "on" }) }); if (!result.ok) { submit.disabled = false; return formError(form, result.error); } closeModal(); toast(t("shares.created")); await loadState(); });
  modal(t("shares.add"), t("shares.subtitle"), form); await browse();
}

async function removeShare(share) { if (!confirm(fmt("shares.removeConfirm", share.name))) return; const result = await api(`/api/shares/${encodeURIComponent(share.id)}`, { method: "DELETE" }); if (!result.ok) return toast(result.error); toast(t("shares.removed")); await loadState(); }

function fillShareOptions(container, selected) { state.shares.forEach((share) => { const label = el("label", "flex cursor-pointer items-center gap-3 rounded-2xl border border-black/10 bg-white p-4 hover:border-moss/40"); const checkbox = document.createElement("input"); checkbox.type = "checkbox"; checkbox.name = "shares"; checkbox.value = share.id; checkbox.checked = selected.includes(share.id); checkbox.className = "h-4 w-4 accent-[#315b48]"; const text = el("span", "min-w-0"); text.append(el("strong", "block text-sm", share.name), el("span", "block truncate font-mono text-xs text-black/40", share.unc)); label.append(checkbox, text); container.append(label); }); }
function setupCommand(platform, unc, username, password) { const parts = unc.split("\\").filter(Boolean); const server = parts[0]; if (platform === "windows") return `cmdkey /add:${server} /user:${username} /pass:${password}\nstart "" "${unc}"`; const smbURL = `smb://${username}:${password}@${server}/${parts[1]}`; return platform === "macos" ? `open '${smbURL}'` : smbURL; }
function secretLine(label, value) { const row = el("div", "flex items-center justify-between gap-3"); const text = el("div", "min-w-0"); text.append(el("span", "block text-xs text-black/40", label), el("code", "block truncate text-sm font-semibold", value)); row.append(text, button(t("common.copy"), "text-xs font-semibold text-moss", () => copyText(value))); return row; }

async function api(path, options = {}) { try { const response = await fetch(path, { credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Locale": locale }, ...options }); let data = null; if (response.status !== 204) data = await response.json().catch(() => null); return { ok: response.ok, status: response.status, data, error: data?.error }; } catch (_) { return { ok: false, status: 0, error: t("errors.serverUnavailable") }; } }
async function refresh() { const result = await api("/api/state"); if (result.ok) Object.assign(state, result.data); }
function modal(title, subtitle, content) { if ($("#modal").classList.contains("hidden")) modalReturnFocus = document.activeElement; $("#modalTitle").textContent = title; $("#modalSubtitle").textContent = subtitle; $("#modalBody").replaceChildren(content); $("#modal").classList.remove("hidden"); $("#modal").classList.add("flex"); requestAnimationFrame(() => $("#modalBody").querySelector("input, select, button")?.focus()); }
function closeModal() { $("#modal").classList.add("hidden"); $("#modal").classList.remove("flex"); modalReturnFocus?.focus(); modalReturnFocus = null; }
function formError(form, message) { const node = form.querySelector("[data-error]"); if (node) { node.textContent = message || t("common.error"); node.classList.remove("hidden"); } }
function button(text, className, handler, type = "button") { const node = el("button", className, text); node.type = type; if (handler) node.addEventListener("click", handler); return node; }
function el(tag, className = "", text = "") { const node = document.createElement(tag); node.className = className; if (text !== "") node.textContent = text; return node; }
function option(value, text) { const node = document.createElement("option"); node.value = value; node.textContent = text; return node; }
function t(key) { return messages[key] || key; }
function fmt(key, value) { return t(key).replace("%s", value); }
function formatDate(value) { return new Intl.DateTimeFormat(locale, { dateStyle: "medium", timeStyle: "short" }).format(new Date(value)); }
async function copyText(value) { await navigator.clipboard.writeText(value); toast(t("common.copied")); }
function toast(message) { const node = $("#toast"); node.textContent = message; node.classList.remove("hidden"); clearTimeout(toast.timer); toast.timer = setTimeout(() => node.classList.add("hidden"), 2400); }
