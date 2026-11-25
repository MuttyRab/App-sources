// Shared UI primitives: popover menus, modal dialogs, confirmations, toasts
// and tooltips. All are keyboard accessible.
import { h, $$ } from "./util.js";
import { icon } from "./icons.js";

let openMenuState = null;

/** Closes the open popover menu, if any. */
export function closeMenu() {
  if (!openMenuState) return;
  const { el, anchor, cleanup } = openMenuState;
  openMenuState = null;
  cleanup();
  el.remove();
  anchor?.setAttribute("aria-expanded", "false");
}

/**
 * Opens a popover menu next to anchor. items: {label, icon, onClick, danger,
 * checked, disabled, hint, kbd} | {divider:true} | {heading} | {custom: Element}.
 */
export function openMenu(anchor, items, opts = {}) {
  if (openMenuState?.anchor === anchor) {
    closeMenu();
    return null;
  }
  closeMenu();
  const menu = h("div", { class: "menu " + (opts.className || ""), role: "menu", tabindex: "-1" });
  if (opts.width) menu.style.width = opts.width + "px";
  for (const it of items) {
    if (!it) continue;
    if (it.divider) {
      menu.append(h("div", { class: "menu-divider", role: "separator" }));
      continue;
    }
    if (it.heading) {
      menu.append(h("div", { class: "menu-heading", text: it.heading }));
      continue;
    }
    if (it.custom) {
      menu.append(it.custom);
      continue;
    }
    const btn = h(
      "button",
      {
        type: "button",
        class: "menu-item" + (it.danger ? " danger" : "") + (it.checked ? " checked" : ""),
        role: it.checked !== undefined ? "menuitemcheckbox" : "menuitem",
        "aria-checked": it.checked !== undefined ? String(!!it.checked) : null,
        disabled: !!it.disabled,
        onclick: (e) => {
          e.stopPropagation();
          if (!it.keepOpen) closeMenu();
          it.onClick?.(e);
        },
      },
      it.icon ? h("span", { class: "menu-icon", html: icon(it.icon, 18) }) : null,
      h("span", { class: "menu-label" }, h("span", { text: it.label }), it.hint ? h("span", { class: "menu-hint", text: it.hint }) : null),
      it.kbd ? h("kbd", { class: "menu-kbd", text: it.kbd }) : null,
      it.checked ? h("span", { class: "menu-check", html: icon("check", 18) }) : null,
    );
    menu.append(btn);
  }
  document.body.append(menu);
  position(menu, anchor, opts);
  anchor?.setAttribute("aria-expanded", "true");

  const onDown = (e) => {
    if (!menu.contains(e.target) && !anchor?.contains(e.target)) closeMenu();
  };
  const onKey = (e) => {
    const btns = $$(".menu-item:not(:disabled)", menu);
    const i = btns.indexOf(document.activeElement);
    if (e.key === "Escape") {
      e.preventDefault();
      closeMenu();
      anchor?.focus();
    } else if (e.key === "ArrowDown") {
      e.preventDefault();
      btns[(i + 1) % btns.length]?.focus();
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      btns[(i - 1 + btns.length) % btns.length]?.focus();
    } else if (e.key === "Tab") {
      closeMenu();
    }
  };
  const onResize = () => closeMenu();
  setTimeout(() => document.addEventListener("pointerdown", onDown, true));
  document.addEventListener("keydown", onKey, true);
  window.addEventListener("resize", onResize);
  openMenuState = {
    el: menu,
    anchor,
    cleanup() {
      document.removeEventListener("pointerdown", onDown, true);
      document.removeEventListener("keydown", onKey, true);
      window.removeEventListener("resize", onResize);
      opts.onClose?.();
    },
  };
  if (opts.focusFirst !== false) requestAnimationFrame(() => (menu.querySelector(".menu-item:not(:disabled)") || menu).focus({ preventScroll: true }));
  return menu;
}

function position(el, anchor, { side = "bottom", align = "start", offset = 6 } = {}) {
  const r = anchor.getBoundingClientRect();
  const m = el.getBoundingClientRect();
  const vw = innerWidth, vh = innerHeight;
  let top, left;
  if (side === "right") {
    left = r.right + offset;
    top = align === "end" ? r.bottom - m.height : r.top;
  } else if (side === "top") {
    top = r.top - m.height - offset;
    left = align === "end" ? r.right - m.width : r.left;
  } else {
    top = r.bottom + offset;
    left = align === "end" ? r.right - m.width : align === "center" ? r.left + r.width / 2 - m.width / 2 : r.left;
  }
  if (top + m.height > vh - 8) top = Math.max(8, side === "bottom" ? r.top - m.height - offset : vh - m.height - 8);
  if (top < 8) top = 8;
  left = Math.min(Math.max(8, left), vw - m.width - 8);
  el.style.top = top + "px";
  el.style.left = left + "px";
}

const modalStack = [];

/** Opens a modal dialog and returns {el, body, close}. */
export function openModal({ title, content, className = "", onClose, label }) {
  const prevFocus = document.activeElement;
  const body = h("div", { class: "modal-body" });
  const titleId = "m" + Math.random().toString(36).slice(2);
  const dialog = h(
    "div",
    { class: "modal " + className, role: "dialog", "aria-modal": "true", "aria-labelledby": title ? titleId : null, "aria-label": title ? null : label },
    title
      ? h(
          "div",
          { class: "modal-head" },
          h("h2", { id: titleId, text: title }),
          h("button", { type: "button", class: "icon-btn", "aria-label": "Close", html: icon("x", 20), onclick: () => close() }),
        )
      : null,
    body,
  );
  const overlay = h("div", { class: "overlay", onmousedown: (e) => e.target === overlay && close() }, dialog);
  document.body.append(overlay);
  if (typeof content === "function") content(body, close);
  else if (content) body.append(content);
  const onKey = (e) => {
    if (modalStack[modalStack.length - 1] !== overlay) return;
    if (e.key === "Escape") {
      e.preventDefault();
      close();
    } else if (e.key === "Tab") {
      const f = $$('button:not(:disabled), [href], input:not(:disabled), select, textarea, [tabindex]:not([tabindex="-1"])', dialog).filter((x) => x.offsetParent);
      if (!f.length) return;
      if (e.shiftKey && document.activeElement === f[0]) {
        e.preventDefault();
        f[f.length - 1].focus();
      } else if (!e.shiftKey && document.activeElement === f[f.length - 1]) {
        e.preventDefault();
        f[0].focus();
      }
    }
  };
  document.addEventListener("keydown", onKey, true);
  modalStack.push(overlay);
  let closed = false;
  function close() {
    if (closed) return;
    closed = true;
    document.removeEventListener("keydown", onKey, true);
    modalStack.splice(modalStack.indexOf(overlay), 1);
    overlay.remove();
    onClose?.();
    prevFocus?.focus?.({ preventScroll: true });
  }
  requestAnimationFrame(() => {
    const f = dialog.querySelector("[autofocus], input, textarea, select, .modal-body button") || dialog.querySelector("button");
    f?.focus({ preventScroll: true });
  });
  return { el: dialog, body, close };
}

/** Shows a confirmation dialog; resolves true when confirmed. */
export function confirmDialog({ title, message, confirmLabel = "Confirm", danger = false }) {
  return new Promise((resolve) => {
    let result = false;
    const m = openModal({
      title,
      className: "modal-small",
      onClose: () => resolve(result),
      content: (body, close) => {
        body.append(
          h("p", { class: "modal-text", text: message }),
          h(
            "div",
            { class: "modal-actions" },
            h("button", { type: "button", class: "btn btn-secondary", text: "Cancel", onclick: () => close() }),
            h("button", {
              type: "button",
              class: "btn " + (danger ? "btn-danger" : "btn-primary"),
              text: confirmLabel,
              autofocus: true,
              onclick: () => {
                result = true;
                close();
              },
            }),
          ),
        );
      },
    });
    return m;
  });
}

/** Asks for a line of text; resolves with the string or null. */
export function promptDialog({ title, label, value = "", confirmLabel = "Save" }) {
  return new Promise((resolve) => {
    let result = null;
    openModal({
      title,
      className: "modal-small",
      onClose: () => resolve(result),
      content: (body, close) => {
        const input = h("input", { class: "input", value, "aria-label": label || title, autofocus: true });
        const submit = () => {
          result = input.value.trim() || null;
          close();
        };
        input.addEventListener("keydown", (e) => e.key === "Enter" && submit());
        body.append(
          label ? h("label", { class: "field-label", text: label }) : null,
          input,
          h(
            "div",
            { class: "modal-actions" },
            h("button", { type: "button", class: "btn btn-secondary", text: "Cancel", onclick: () => close() }),
            h("button", { type: "button", class: "btn btn-primary", text: confirmLabel, onclick: submit }),
          ),
        );
        requestAnimationFrame(() => input.select());
      },
    });
  });
}

let toastHost;
/** Shows a short message at the bottom of the window. */
export function toast(message, { action, timeout = 3500, kind = "" } = {}) {
  if (!toastHost) {
    toastHost = h("div", { class: "toasts", role: "status", "aria-live": "polite" });
    document.body.append(toastHost);
  }
  const t = h(
    "div",
    { class: "toast " + kind },
    h("span", { text: message }),
    action ? h("button", { type: "button", class: "toast-action", text: action.label, onclick: () => (action.fn(), dismiss()) }) : null,
  );
  toastHost.append(t);
  const timer = setTimeout(dismiss, timeout);
  function dismiss() {
    clearTimeout(timer);
    t.classList.add("out");
    setTimeout(() => t.remove(), 180);
  }
  return dismiss;
}

// Tooltips for any element with data-tip (and optional data-tip-kbd).
let tipEl, tipTimer, tipTarget;
function showTip(target) {
  const text = target.dataset.tip;
  if (!text) return;
  tipEl ||= h("div", { class: "tooltip", role: "tooltip" });
  tipEl.replaceChildren(h("span", { text }), target.dataset.tipKbd ? h("kbd", { text: target.dataset.tipKbd }) : null);
  document.body.append(tipEl);
  const r = target.getBoundingClientRect();
  const m = tipEl.getBoundingClientRect();
  const side = target.dataset.tipSide || "bottom";
  let top = side === "top" ? r.top - m.height - 6 : side === "right" ? r.top + r.height / 2 - m.height / 2 : r.bottom + 6;
  let left = side === "right" ? r.right + 8 : r.left + r.width / 2 - m.width / 2;
  left = Math.min(Math.max(6, left), innerWidth - m.width - 6);
  if (top + m.height > innerHeight - 6) top = r.top - m.height - 6;
  tipEl.style.top = top + "px";
  tipEl.style.left = left + "px";
}
function hideTip() {
  clearTimeout(tipTimer);
  tipTarget = null;
  tipEl?.remove();
}
document.addEventListener("mouseover", (e) => {
  const t = e.target.closest?.("[data-tip]");
  if (t === tipTarget) return;
  hideTip();
  if (!t) return;
  tipTarget = t;
  tipTimer = setTimeout(() => tipTarget === t && document.contains(t) && showTip(t), 450);
});
document.addEventListener("pointerdown", hideTip, true);
document.addEventListener("keydown", hideTip, true);
window.addEventListener("blur", hideTip);
