// The popup: search the index through vaultr, pick a key, and have the
// background fill it into the field that had focus on the page. Values
// never come here.

"use strict";

const ext = globalThis.browser || globalThis.chrome;
const $ = (id) => document.getElementById(id);

const state = {
  tabId: null,
  origin: null, // the page's origin when the popup opened
  items: [],
  selected: 0,
  seq: 0, // latest search, to drop answers to older ones
  scope: { all: false, ns: null }, // ns null: the configured namespace
  busy: false,
};

// --- talking to the background ---

const bg = ext.runtime.connect({ name: "popup" });
const waits = new Map();
let rid = 0;

bg.onMessage.addListener((m) => {
  if (m.broadcast) {
    onEvent(m.event);
    return;
  }
  const w = waits.get(m.id);
  if (!w) return;
  if (m.event) {
    onEvent(m.event);
    return;
  }
  waits.delete(m.id);
  if (m.error) w.reject(m.error);
  else w.resolve(m.result);
});

function rpc(op, params) {
  const id = ++rid;
  return new Promise((resolve, reject) => {
    waits.set(id, { resolve: resolve, reject: reject });
    bg.postMessage(Object.assign({}, params, { id: id, op: op }));
  });
}

function host(type, params) {
  return rpc("host", { type: type, params: params || {} });
}

function onEvent(ev) {
  if (ev.type === "progress") {
    note($("status"), "Indexing: " + ev.secrets + " secrets, " + ev.lists + " folders");
  } else if (ev.type === "login_url") {
    const p = $("login-url");
    p.hidden = false;
    p.textContent = "If no login page opened: ";
    const a = document.createElement("a");
    a.href = ev.url;
    a.target = "_blank";
    a.rel = "noreferrer";
    a.textContent = "open it";
    p.append(a);
  }
}

// --- display helpers ---

function note(el, text, cls) {
  el.textContent = text;
  el.className = cls || (el.id === "target" ? "note" : "");
}

function showMessage(text, isError) {
  const m = $("message");
  m.hidden = !text;
  m.className = isError ? "error" : "";
  m.textContent = text || "";
}

// browserName guesses the --browser value for the install hint.
function browserName() {
  if (/Firefox\//.test(navigator.userAgent)) return "firefox";
  if (navigator.brave) return "brave";
  const brands = (navigator.userAgentData && navigator.userAgentData.brands) || [];
  const names = brands.map((b) => b.brand);
  if (names.includes("Microsoft Edge")) return "edge";
  if (names.includes("Google Chrome")) return "chrome";
  if (names.includes("Chromium")) return "chromium";
  return "chrome";
}

function errorText(err) {
  switch (err.code) {
    case "no_host":
      return (
        "vaultr is not set up for this browser. Run `vaultr extension install --browser " +
        browserName() +
        "` in a terminal, then open vaultr again." +
        (err.detail ? " (The browser said: " + err.detail + ")" : "")
      );
    case "not_logged_in":
      return "Not logged in to Vault.";
    default:
      return err.message || String(err);
  }
}

function nsLabel(ns) {
  return ns === "" ? "/" : ns;
}

// --- the page's focused field ---

const REASONS = {
  none: "Click into a text field on the page first, then open vaultr again.",
  "not-text": "The focused element is not a text field.",
  "read-only": "The focused field is read-only.",
  "cross-origin-frame":
    "The focused field is in a frame from another site, which the extension can't reach. Open that frame's page in its own tab.",
  "no-access": "vaultr can't fill fields on this page.",
  "origin-changed": "The page changed since vaultr opened. Open it again.",
};

async function probe() {
  const t = $("target");
  if (state.tabId == null) {
    note(t, REASONS["no-access"], "note warn");
    return;
  }
  const r = await rpc("probe", { tabId: state.tabId });
  state.origin = r.origin || null;
  if (!r.ok) {
    note(t, REASONS[r.reason] || r.reason, "note warn");
    return;
  }
  let text = "Fills the " + r.kind + " field";
  if (r.label) text += " “" + r.label + "”";
  text += " on " + r.origin;
  if (!r.secure) {
    note(t, text + ". Warning: this page is not HTTPS.", "note warn");
    return;
  }
  note(t, text);
}

// --- search ---

function scopeParams() {
  if (state.scope.all) return { allNamespaces: true };
  if (state.scope.ns !== null) return { namespace: state.scope.ns };
  return {};
}

let timer = null;
function scheduleSearch() {
  clearTimeout(timer);
  timer = setTimeout(search, 60);
}

async function search() {
  const seq = ++state.seq;
  let r;
  try {
    r = await host("search", Object.assign({ query: $("query").value, limit: 50 }, scopeParams()));
  } catch (err) {
    if (seq !== state.seq) return;
    if (err.code === "not_logged_in") {
      showLogin({ authMethod: state.authMethod, reason: "expired" });
      return;
    }
    render([]);
    showMessage(errorText(err), true);
    return;
  }
  if (seq !== state.seq) return;
  showMessage(r.warning ? "Warning: " + r.warning : "", false);
  state.items = r.items;
  state.recent = r.recent;
  state.selected = 0;
  render(r.items);
  let s = r.total + (r.total === 1 ? " match" : " matches");
  if (r.truncated) s = r.items.length + " of " + s;
  note($("status"), s);
}

function render(items) {
  const ul = $("results");
  ul.textContent = "";
  items.forEach((it, i) => {
    const li = document.createElement("li");
    li.setAttribute("role", "option");
    li.setAttribute("aria-selected", String(i === state.selected));
    if (state.scope.all) {
      const ns = document.createElement("span");
      ns.className = "ns";
      ns.textContent = nsLabel(it.namespace);
      li.append(ns);
    }
    const p = document.createElement("span");
    p.className = "path";
    const bdi = document.createElement("bdi");
    bdi.textContent = it.path;
    p.append(bdi);
    p.title = it.path;
    li.append(p);
    const k = document.createElement("span");
    k.className = "key";
    k.textContent = it.key || "";
    li.append(k);
    if (i < (state.recent || 0)) {
      const rc = document.createElement("span");
      rc.className = "recent";
      rc.textContent = "recent";
      li.append(rc);
    }
    li.addEventListener("click", () => {
      state.selected = i;
      pick();
    });
    ul.append(li);
  });
}

function move(delta) {
  if (state.items.length === 0) return;
  state.selected = (state.selected + delta + state.items.length) % state.items.length;
  const lis = $("results").children;
  for (let i = 0; i < lis.length; i++) {
    lis[i].setAttribute("aria-selected", String(i === state.selected));
  }
  lis[state.selected].scrollIntoView({ block: "nearest" });
}

async function pick() {
  const it = state.items[state.selected];
  if (!it || state.busy) return;
  if (!it.key) {
    showMessage("This secret has no keys to fill.", true);
    return;
  }
  if (state.tabId == null || !state.origin) {
    showMessage(REASONS["no-access"], true);
    return;
  }
  state.busy = true;
  showMessage("Filling…", false);
  try {
    const r = await rpc("fill", {
      tabId: state.tabId,
      origin: state.origin,
      namespace: it.namespace,
      path: it.path,
      key: it.key,
    });
    if (r.ok) {
      window.close();
      return;
    }
    showMessage(REASONS[r.reason] || r.message || r.reason, true);
  } catch (err) {
    showMessage(errorText(err), true);
  } finally {
    state.busy = false;
  }
}

$("query").addEventListener("input", scheduleSearch);
$("query").addEventListener("keydown", (e) => {
  switch (e.key) {
    case "ArrowDown":
      move(1);
      break;
    case "ArrowUp":
      move(-1);
      break;
    case "PageDown":
      move(10);
      break;
    case "PageUp":
      move(-10);
      break;
    case "Enter":
      pick();
      break;
    case "Escape":
      if ($("query").value) {
        $("query").value = "";
        scheduleSearch();
      } else {
        window.close();
      }
      break;
    default:
      return;
  }
  e.preventDefault();
});

// --- namespaces ---

const NS_KEY = "vaultr.scope";

function savedScope() {
  try {
    const s = JSON.parse(localStorage.getItem(NS_KEY) || "null");
    if (s && typeof s.all === "boolean" && (s.ns === null || typeof s.ns === "string")) return s;
  } catch (e) {
    // no storage
  }
  return { all: false, ns: null };
}

async function loadNamespaces(configured) {
  const sel = $("namespace");
  let list;
  try {
    list = (await host("namespaces")).namespaces;
  } catch (e) {
    list = []; // e.g. Vault CE: no namespaces
  }
  if (!list.includes(configured)) list.unshift(configured);
  if (list.length <= 1) {
    sel.hidden = true;
    return;
  }
  sel.textContent = "";
  for (const ns of list) {
    const o = document.createElement("option");
    o.value = "ns:" + ns;
    o.textContent = nsLabel(ns) + (ns === configured ? " (configured)" : "");
    sel.append(o);
  }
  const all = document.createElement("option");
  all.value = "all";
  all.textContent = "All namespaces";
  sel.append(all);
  const s = savedScope();
  if (s.all) {
    state.scope = s;
    sel.value = "all";
  } else if (s.ns !== null && list.includes(s.ns)) {
    state.scope = s;
    sel.value = "ns:" + s.ns;
  } else {
    sel.value = "ns:" + configured;
  }
  sel.hidden = false;
  sel.addEventListener("change", () => {
    state.scope = sel.value === "all" ? { all: true, ns: null } : { all: false, ns: sel.value.slice(3) };
    try {
      localStorage.setItem(NS_KEY, JSON.stringify(state.scope));
    } catch (e) {
      // no storage: the choice lasts until the popup closes
    }
    $("query").focus();
    search();
  });
}

// --- login ---

function showLogin(st) {
  $("results").textContent = "";
  $("login").hidden = false;
  const method = st.authMethod || "";
  const why = st.reason === "expired" ? "Your Vault token expired." : "Not logged in to Vault.";
  if (method === "ldap" || method === "userpass") {
    // Directory passwords never go through the browser.
    $("login-why").textContent =
      why + " Your login method (" + method + ") asks for a password: run `vaultr login` in a terminal, then open vaultr again.";
    $("login-oidc").hidden = true;
  } else {
    $("login-why").textContent = why;
    $("login-oidc").hidden = method === "token";
  }
}

async function runLogin(params) {
  showMessage("Waiting for the login…", false);
  $("login").querySelectorAll("button, input").forEach((el) => (el.disabled = true));
  try {
    const r = await host("login", params);
    $("login").hidden = true;
    showMessage(r.warning ? "Warning: " + r.warning : "", false);
    await ready();
  } catch (err) {
    // vaultr's own message: "not logged in" says nothing about a bad token.
    showMessage(err.message || errorText(err), true);
  } finally {
    $("login").querySelectorAll("button, input").forEach((el) => (el.disabled = false));
  }
}

$("login-oidc").addEventListener("click", () => {
  showMessage("Finish logging in on the page that opened.", false);
  runLogin({ method: "oidc" });
});

$("login-token-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const input = $("login-token");
  const token = input.value.trim();
  input.value = "";
  if (token) runLogin({ method: "token", token: token });
});

// --- start ---

async function shortcut() {
  try {
    const cmds = await ext.commands.getAll();
    const c = cmds.find((x) => x.name === "_execute_action");
    if (c && c.shortcut) {
      note($("shortcut"), "Shortcut: " + c.shortcut);
      return;
    }
  } catch (e) {
    // commands unavailable
  }
  const where =
    browserName() === "firefox"
      ? "about:addons, gear menu, Manage Extension Shortcuts"
      : browserName() + "://extensions/shortcuts";
  note($("shortcut"), "No shortcut set (" + where + ")");
}

// ready runs once vaultr answers and the user is logged in.
async function ready() {
  const configured = state.hello.namespace;
  await loadNamespaces(configured);
  $("query").focus();
  await search();
}

async function start() {
  shortcut();
  try {
    const tabs = await ext.tabs.query({ active: true, currentWindow: true });
    state.tabId = tabs.length ? tabs[0].id : null;
  } catch (e) {
    state.tabId = null;
  }
  const probed = probe().catch(() => note($("target"), REASONS["no-access"], "note warn"));

  const ls = await rpc("loginState");
  if (ls.pending) {
    showMessage("Finish logging in on the page that opened.", false);
    try {
      await rpc("waitLogin");
      showMessage("", false);
    } catch (err) {
      showMessage(errorText(err), true);
    }
  }
  let hello;
  try {
    hello = await host("hello");
  } catch (err) {
    showMessage(errorText(err), true);
    return;
  }
  state.hello = hello;
  if (hello.problem) {
    showMessage(hello.problem.message, true);
    return;
  }
  if (hello.warnings && hello.warnings.length) showMessage("Warning: " + hello.warnings.join("; "), false);
  let st;
  try {
    st = await host("status");
  } catch (err) {
    showMessage(errorText(err), true);
    return;
  }
  state.authMethod = st.authMethod;
  await probed;
  if (!st.loggedIn) {
    showLogin(st);
    return;
  }
  await ready();
}

// Focus the search box as soon as the popup opens, not once vaultr has
// answered: what is typed meanwhile is searched when it can be. Firefox
// may ignore focus() while its popup is still opening, so try again when
// the popup's window gets focus.
function focusQuery() {
  if ($("login").hidden && document.activeElement !== $("query")) $("query").focus();
}
focusQuery();
window.addEventListener("focus", focusQuery, { once: true });
setTimeout(focusQuery, 100);

start();
