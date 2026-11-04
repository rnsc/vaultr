// vaultrField runs in the page, injected by background.js with
// scripting.executeScript. The browser serializes the function's source,
// so it must not use anything defined outside it: its helpers are nested.
//
//   vaultrField("probe")                 describes the focused field
//   vaultrField("fill", value, origin)   puts value in it
//
// It returns {ok: true, kind, label, origin, secure} or {ok: false,
// reason}. It never returns or keeps the value.

function vaultrField(action, value, expectedOrigin) {
  const TEXT_TYPES = ["text", "email", "password", "tel", "url", "search", "number"];

  // shadowRootOf finds an element's shadow root, closed ones included
  // where the browser lets extensions see them.
  function shadowRootOf(el) {
    if (el.shadowRoot) return el.shadowRoot;
    try {
      if (typeof chrome !== "undefined" && chrome.dom && chrome.dom.openOrClosedShadowRoot) {
        return chrome.dom.openOrClosedShadowRoot(el); // Chrome
      }
      if (typeof el.openOrClosedShadowRoot === "function") {
        return el.openOrClosedShadowRoot(); // Firefox
      }
    } catch (e) {
      // not a shadow host
    }
    return null;
  }

  // focused follows the focus down through shadow roots and same-origin
  // frames. Frames from another origin can't be read: activeTab only
  // grants access to the page's own origin.
  function focused() {
    let el = document.activeElement;
    for (let depth = 0; el && depth < 32; depth++) {
      const root = shadowRootOf(el);
      if (root && root.activeElement) {
        el = root.activeElement;
        continue;
      }
      if (el.tagName === "IFRAME" || el.tagName === "FRAME") {
        let doc = null;
        try {
          doc = el.contentDocument;
        } catch (e) {
          doc = null;
        }
        if (!doc) return { reason: "cross-origin-frame" };
        if (!doc.activeElement || doc.activeElement === doc.body) return { reason: "none" };
        el = doc.activeElement;
        continue;
      }
      break;
    }
    if (!el || el.tagName === "BODY" || el.tagName === "HTML") return { reason: "none" };
    return { el };
  }

  // kind says what sort of text field el is, or why it isn't one.
  function kind(el) {
    if (el.tagName === "INPUT") {
      if (!TEXT_TYPES.includes(el.type)) return { reason: "not-text" };
      if (el.disabled || el.readOnly) return { reason: "read-only" };
      return { kind: el.type };
    }
    if (el.tagName === "TEXTAREA") {
      if (el.disabled || el.readOnly) return { reason: "read-only" };
      return { kind: "textarea" };
    }
    if (el.isContentEditable) return { kind: "contenteditable" };
    return { reason: "not-text" };
  }

  function label(el) {
    const s =
      el.getAttribute("aria-label") ||
      el.getAttribute("autocomplete") ||
      el.getAttribute("name") ||
      el.id ||
      el.getAttribute("placeholder") ||
      "";
    return s.slice(0, 60);
  }

  const here = location.origin;
  const secure =
    location.protocol === "https:" || ["localhost", "127.0.0.1", "[::1]"].includes(location.hostname);
  if (action === "fill" && here !== expectedOrigin) {
    // The tab moved to another page since the popup looked at it.
    return { ok: false, reason: "origin-changed" };
  }
  const f = focused();
  if (!f.el) return { ok: false, reason: f.reason, origin: here };
  const k = kind(f.el);
  if (!k.kind) return { ok: false, reason: k.reason, origin: here };
  if (action === "probe") {
    return { ok: true, kind: k.kind, label: label(f.el), origin: here, secure: secure };
  }

  const el = f.el;
  const win = el.ownerDocument.defaultView || window;
  el.focus();
  if (k.kind === "contenteditable") {
    el.textContent = value;
  } else {
    // The prototype's setter, not el.value = ...: React replaces the
    // element's own value property to track changes, and would miss
    // ours (and drop it on the next render) if we went through it.
    const proto = el.tagName === "TEXTAREA" ? win.HTMLTextAreaElement.prototype : win.HTMLInputElement.prototype;
    const desc = Object.getOwnPropertyDescriptor(proto, "value");
    if (desc && desc.set) {
      desc.set.call(el, value);
    } else {
      el.value = value;
    }
  }
  // composed: true lets the events cross shadow DOM boundaries, where
  // frameworks often listen.
  const Ev = win.Event || Event;
  el.dispatchEvent(new Ev("input", { bubbles: true, composed: true }));
  el.dispatchEvent(new Ev("change", { bubbles: true, composed: true }));
  value = null;
  return { ok: true, kind: k.kind, origin: here };
}
