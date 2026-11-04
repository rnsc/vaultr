// The background script owns the connection to vaultr (the native
// messaging host). The popup talks to it over a runtime port; it never
// gets a value: on "fill", the background asks vaultr for the value and
// hands it straight to the fill function in the page.
//
// Chrome runs this as a service worker, Firefox as an event page. An open
// native port keeps either alive, which matters for an OIDC login: the
// popup closes when the login page opens, the login goes on here.

"use strict";

if (typeof vaultrField === "undefined" && typeof importScripts === "function") {
  importScripts("fill.js"); // Chrome; Firefox loads it from the manifest
}

const HOST = "be.rnsc.vaultr";
const PROTOCOL = 1;
// Close the connection after this long unused: vaultr exits, and its
// token and index leave memory.
const IDLE_MS = 10 * 60 * 1000;
// What the popup may ask vaultr directly. "get" is not here: values only
// go from vaultr to the page, through fill.
const POPUP_TYPES = ["hello", "status", "namespaces", "search", "refresh", "login"];

const ext = globalThis.browser || globalThis.chrome;

let port = null;
let nextId = 1;
const pending = new Map();
let idleTimer = null;
let login = null; // the running login, shared by popups
const popups = new Set();

function disconnectMessage(m) {
  if (!m) return "vaultr exited.";
  if (/not found|no such native application/i.test(m)) {
    return "vaultr is not set up for this browser.";
  }
  if (/forbidden/i.test(m)) {
    return "vaultr does not allow this extension: run `vaultr extension install` again.";
  }
  return "vaultr stopped: " + m;
}

function connect() {
  const p = ext.runtime.connectNative(HOST);
  p.onMessage.addListener(onHostMessage);
  p.onDisconnect.addListener((dp) => {
    const err = (dp && dp.error) || ext.runtime.lastError;
    const raw = (err && err.message) || "";
    const msg = disconnectMessage(raw);
    const code = /not set up/.test(msg) ? "no_host" : "host";
    if (port === p) port = null;
    // detail: the browser's own words, for when our reading of them is wrong.
    for (const call of pending.values()) call.reject({ code: code, message: msg, detail: raw });
    pending.clear();
  });
  port = p;
}

function touch() {
  clearTimeout(idleTimer);
  idleTimer = setTimeout(() => {
    if (port && pending.size === 0) {
      port.disconnect();
      port = null;
    }
  }, IDLE_MS);
}

// callHost sends one request to vaultr. onEvent gets the updates sent
// while it runs (index progress, the OIDC login address).
function callHost(type, params, onEvent) {
  if (!port) connect();
  touch();
  const id = nextId++;
  return new Promise((resolve, reject) => {
    pending.set(id, { resolve: resolve, reject: reject, onEvent: onEvent });
    try {
      port.postMessage(Object.assign({}, params, { id: id, type: type }));
    } catch (e) {
      pending.delete(id);
      reject({ code: "host", message: disconnectMessage(e.message) });
    }
  });
}

function onHostMessage(m) {
  const call = pending.get(m.id);
  if (!call) return;
  if (m.event) {
    if (call.onEvent) call.onEvent(m.event);
    return;
  }
  pending.delete(m.id);
  touch();
  if (m.error) call.reject(m.error);
  else call.resolve(m.result);
}

function broadcast(event) {
  for (const p of popups) {
    try {
      p.postMessage({ broadcast: true, event: event });
    } catch (e) {
      popups.delete(p);
    }
  }
}

async function inPage(tabId, args) {
  const results = await ext.scripting.executeScript({
    target: { tabId: tabId, frameIds: [0] },
    func: vaultrField,
    args: args,
  });
  const r = results && results[0];
  if (!r) return { ok: false, reason: "no-access" };
  if (r.error) return { ok: false, reason: "no-access", message: String(r.error.message || r.error) };
  return r.result || { ok: false, reason: "no-access" };
}

async function probe(tabId) {
  try {
    return await inPage(tabId, ["probe", null, null]);
  } catch (e) {
    // Browser pages, the extension store, PDF viewers: no scripting.
    return { ok: false, reason: "no-access", message: e.message };
  }
}

// fill fetches one value and puts it in the focused field. The value
// lives only in this function.
async function fill(msg) {
  const req = { path: msg.path, key: msg.key };
  if (typeof msg.namespace === "string") req.namespace = msg.namespace;
  let value = (await callHost("get", req)).value;
  try {
    return await inPage(msg.tabId, ["fill", value, msg.origin]);
  } catch (e) {
    return { ok: false, reason: "no-access", message: e.message };
  } finally {
    value = null;
  }
}

async function fromPopup(msg, reply) {
  switch (msg.op) {
    case "host": {
      if (!POPUP_TYPES.includes(msg.type)) throw { code: "bad_request", message: "not allowed: " + msg.type };
      const params = Object.assign({}, msg.params);
      if (msg.type === "hello") {
        params.protocol = PROTOCOL;
        params.version = ext.runtime.getManifest().version;
      }
      if (msg.type === "login") {
        if (!login) {
          login = callHost("login", params, broadcast).finally(() => {
            login = null;
          });
        }
        return await login;
      }
      return await callHost(msg.type, params, (ev) => reply({ event: ev }));
    }
    case "loginState":
      return { pending: login !== null };
    case "waitLogin":
      return login ? await login : null;
    case "probe":
      return await probe(msg.tabId);
    case "fill":
      return await fill(msg);
  }
  throw { code: "bad_request", message: "unknown op " + msg.op };
}

// Only this extension's own popup may connect: no externally_connectable,
// no onMessageExternal, so web pages and other extensions can't.
function isPopup(sender) {
  return (
    !!sender &&
    sender.id === ext.runtime.id &&
    typeof sender.url === "string" &&
    sender.url.startsWith(ext.runtime.getURL("popup.html"))
  );
}

ext.runtime.onConnect.addListener((p) => {
  if (p.name !== "popup" || !isPopup(p.sender)) {
    p.disconnect();
    return;
  }
  popups.add(p);
  p.onDisconnect.addListener(() => popups.delete(p));
  p.onMessage.addListener((msg) => {
    const reply = (data) => {
      try {
        p.postMessage(Object.assign({ id: msg.id }, data));
      } catch (e) {
        // the popup closed
      }
    };
    fromPopup(msg, reply).then(
      (result) => reply({ result: result === undefined ? null : result }),
      (err) =>
        reply({
          error: {
            code: (err && err.code) || "error",
            message: (err && err.message) || String(err),
            detail: (err && err.detail) || "",
          },
        }),
    );
  });
});
