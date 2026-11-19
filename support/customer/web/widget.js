(() => {
  "use strict";
  if (window.DawnSupport) return;
  const script = document.currentScript;
  let host, frame, panel, trigger, menu, config, storeKey, pageTimer, observer, outsideClick;
  let identityTask = Promise.resolve(),
    identityVersion = 0;
  const read = (key) => {
    try {
      return localStorage.getItem(key) || "";
    } catch {
      return "";
    }
  };
  const save = (key, value) => {
    try {
      value ? localStorage.setItem(key, value) : localStorage.removeItem(key);
    } catch {
      /* Browser storage can be disabled. */
    }
  };
  function post(type, data = {}) {
    if (frame?.contentWindow)
      frame.contentWindow.postMessage(
        { source: "dawn-support-host", type, ...data },
        config.server,
      );
  }
  function context() {
    return {
      page_url: location.href.split("#")[0],
      page_title: document.title,
      referrer: document.referrer,
      page_origin: location.origin,
      ...Object.fromEntries(
        [...new URLSearchParams(location.search)].filter(([key]) =>
          key.startsWith("utm_"),
        ),
      ),
    };
  }
  async function request(
    resource,
    method = "POST",
    body,
    token = read(storeKey),
  ) {
    const response = await fetch(
      `${config.server}/api/support/${encodeURIComponent(config.app)}/${resource}`,
      {
        method,
        signal: AbortSignal.timeout(15000),
        credentials: "omit",
        headers: {
          "Content-Type": "application/json",
          "Accept-Language": config.locale,
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      },
    );
    const result = response.status === 204 ? {} : await response.json();
    if (!response.ok)
      throw Object.assign(new Error(result.message), {
        status: response.status,
      });
    return result.data;
  }
  const api = {
    init(options) {
      if (host) return api;
      config = {
        theme: "system",
        locale: navigator.language.startsWith("zh") ? "zh" : "en",
        entry: "default",
        attributes: {},
        ...options,
      };
      config.server = new URL(
        config.server || script.src,
        location.href,
      ).origin;
      if (!config.app) return api;
      storeKey = `dawn.support:${config.server}:${config.app}`;
      host = document.createElement("div");
      host.dataset.dawnSupport = "";
      host.style.cssText =
        "position:fixed;right:0;top:65%;width:52px;height:52px;z-index:2147483000;pointer-events:none;";
      host.style.colorScheme = config.theme === "system" ? "light dark" : config.theme;
      const shadow = host.attachShadow({ mode: "open" });
      const style = document.createElement("style");
      style.textContent =
        ":host{font:14px system-ui;color-scheme:light dark}*{box-sizing:border-box}button{border:0;cursor:pointer}#trigger{pointer-events:auto;position:absolute;inset:0;width:52px;height:52px;border-radius:50%;background:#07a85a;color:#fff;box-shadow:0 3px 18px #0003;transform:translateX(35%);transition:transform .18s;touch-action:none;display:grid;place-items:center}#trigger:hover,#trigger:focus-visible{transform:translateX(0)!important;outline:2px solid #85cfa6;outline-offset:2px}#trigger img{width:32px;height:32px;object-fit:cover;border-radius:50%}#trigger svg{width:25px;height:25px}#panel{pointer-events:auto;position:fixed;width:min(400px,calc(100vw - 24px));height:min(660px,calc(100dvh - 32px));bottom:max(16px,env(safe-area-inset-bottom));right:12px;background:#fff;border:1px solid #2b353b22;border-radius:16px;overflow:hidden;box-shadow:0 15px 70px #0003}iframe{width:100%;height:100%;border:0;display:block}#badge{position:absolute;right:3px;top:0;border-radius:8px;background:#fa5151;min-width:8px;height:8px;display:none}[hidden]{display:none!important}@media(max-width:600px){#panel{inset:0;width:100%;height:100dvh;border:0;border-radius:0}}";
      trigger = document.createElement("button");
      trigger.id = "trigger";
      trigger.type = "button";
      trigger.ariaLabel =
        config.label ||
        (config.locale.startsWith("zh") ? "在线客服" : "Customer support");
      trigger.setAttribute("aria-expanded", "false");
      if (config.avatar) {
        const image = document.createElement("img");
        image.src = config.avatar;
        image.alt = "";
        trigger.append(image);
      } else
        trigger.innerHTML =
          '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" aria-hidden="true"><path d="M4 13v-2a8 8 0 0 1 16 0v2M4 12H3v6h4v-6H4Zm16 0h1v6h-4v-6h3Zm0 6c0 3-3 3-6 3"/><path d="M10 21h4"/></svg>';
      const badge = document.createElement("span");
      badge.id = "badge";
      trigger.append(badge);
      panel = document.createElement("div");
      panel.id = "panel";
      panel.hidden = true;
      panel.setAttribute("role", "dialog");
      panel.setAttribute("aria-label", trigger.ariaLabel);
      menu = document.createElement("div");
      menu.hidden = true;
      menu.style.cssText = "pointer-events:auto;position:fixed;padding:6px;background:light-dark(#fff,#202722);color:light-dark(#24362c,#e4e8e6);border:1px solid light-dark(#e4e8e6,#364139);border-radius:12px;box-shadow:0 8px 28px #0002;display:flex;gap:2px";
      menu.style.flexDirection = config.menuLayout === "horizontal" ? "row" : "column";
      for (const item of config.menu || []) {
        const button = document.createElement("button");
        button.type = "button";
        button.style.cssText = "display:flex;align-items:center;gap:8px;padding:9px 16px;white-space:nowrap;background:transparent;color:inherit;border-radius:8px;font:inherit";
        if (item.icon) {
          const icon = document.createElement("img");
          icon.src = item.icon;
          icon.alt = "";
          icon.style.cssText = "width:20px;height:20px;object-fit:contain";
          button.append(icon);
        }
        const label = document.createElement("span");
        label.textContent = typeof item.label === "function" ? item.label(config.locale) : item.label;
        button.append(label);
        button.onclick = () => { menu.hidden = true; trigger.setAttribute("aria-expanded", "false"); item.onClick(api); };
        menu.append(button);
      }
      shadow.append(style, trigger, panel, menu);
      document.body.append(host);
      const saved = read(`${storeKey}:position`);
      if (saved) {
        try {
          const position = JSON.parse(saved);
          host.style.top = `${Math.min(90, Math.max(5, position.top))}%`;
          host.style.right = position.side === "left" ? "auto" : "0";
          host.style.left = position.side === "left" ? "0" : "auto";
          trigger.style.transform = `translateX(${position.side === "left" ? "-35" : "35"}%)`;
        } catch {
          /* Ignore an invalid saved position. */
        }
      }
      let drag;
      trigger.onpointerdown = (event) => {
        if (event.button !== 0) return;
        trigger.setPointerCapture(event.pointerId);
        drag = {
          x: event.clientX,
          y: event.clientY,
          top: host.getBoundingClientRect().top,
          moved: false,
        };
        trigger.style.transition = "none";
      };
      trigger.onpointermove = (event) => {
        if (!drag) return;
        if (
          Math.abs(event.clientY - drag.y) + Math.abs(event.clientX - drag.x) >
          7
        )
          drag.moved = true;
        if (!drag.moved) return;
        const top = Math.max(
          8,
          Math.min(innerHeight - 60, drag.top + event.clientY - drag.y),
        );
        host.style.top = `${top}px`;
        const left = event.clientX < innerWidth / 2;
        host.style.left = left ? "0" : "auto";
        host.style.right = left ? "auto" : "0";
        trigger.style.transform = "translateX(0)";
      };
      function toggle() {
        if (!panel.hidden) return api.close();
        if (!config.menu?.length) return void api.open();
        menu.hidden = !menu.hidden;
        trigger.setAttribute("aria-expanded", String(!menu.hidden));
        if (!menu.hidden) {
          const rect = host.getBoundingClientRect();
          menu.style.left = host.style.left === "0px" ? "16px" : "auto";
          menu.style.right = host.style.left === "0px" ? "auto" : "16px";
          menu.style.top = `${Math.max(8, rect.top - menu.getBoundingClientRect().height - 8)}px`;
        }
      }
      function finish(event) {
        if (!drag) return;
        const moved = drag.moved;
        drag = undefined;
        trigger.style.transition = "";
        const side = host.style.left === "0px" ? "left" : "right";
        const top = Math.max(
          5,
          Math.min(90, (host.getBoundingClientRect().top / innerHeight) * 100),
        );
        host.style.top = `${top}%`;
        trigger.style.transform = `translateX(${side === "left" ? "-35" : "35"}%)`;
        save(`${storeKey}:position`, JSON.stringify({ top, side }));
        if (!moved && event.type !== "pointercancel") {
          toggle();
        }
      }
      trigger.onpointerup = finish;
      trigger.onpointercancel = finish;
      trigger.onkeydown = (event) => {
        if (event.key === "Enter" || event.key === " ") {
          event.preventDefault();
          toggle();
        }
      };
      pageTimer = setInterval(
        () => post("context", { observed: context() }),
        10000,
      );
      observer = () => {
        panel.style.left = "";
        panel.style.right = "";
        if (innerWidth > 600) {
          panel.style.right = host.style.left === "0px" ? "auto" : "12px";
          panel.style.left = host.style.left === "0px" ? "12px" : "auto";
        }
      };
      addEventListener("resize", observer);
      outsideClick = (event) => {
        if (!host.contains(event.target)) { menu.hidden = true; trigger.setAttribute("aria-expanded", String(!panel.hidden)); }
        if (innerWidth > 600 && !panel.hidden && !host.contains(event.target))
          api.close();
      };
      document.addEventListener("click", outsideClick);
      void api.syncIdentity().catch((error) => config.onError?.(error));
      return api;
    },
    async open() {
      if (!host) return;
      if (!frame) {
        try {
          await identityTask;
        } catch {
          try {
            await api.syncIdentity();
          } catch (error) {
            config.onError?.(error);
            if (!frame) return;
          }
        }
        if (!frame) {
          frame = document.createElement("iframe");
          const url = new URL(
            `/chat/${encodeURIComponent(config.app)}`,
            config.server,
          );
          url.search = new URLSearchParams({
            embed: "1",
            parent_origin: location.origin,
            entry: config.entry,
            theme: config.theme,
            locale: config.locale,
          });
          frame.src = url.href;
          frame.title = trigger.ariaLabel;
          frame.referrerPolicy = "no-referrer";
          panel.append(frame);
        }
      }
      menu.hidden = true;
      panel.hidden = false;
      trigger.hidden = true;
      trigger.setAttribute("aria-expanded", "true");
      observer();
      post("visible", { visible: true });
      frame.focus();
    },
    close() {
      if (!panel || panel.hidden) return;
      panel.hidden = true;
      trigger.hidden = false;
      trigger.setAttribute("aria-expanded", "false");
      post("visible", { visible: false });
      trigger.focus();
    },
    // Share the authenticated identity without exposing stored credentials.
    async createRealtimeTicket(resume = "") {
      await identityTask;
      return request("realtime-tickets", "POST", { resume });
    },
    syncIdentity(proof) {
      const version = ++identityVersion;
      const synchronize = async () => {
        if (version !== identityVersion) return;
        const entry = await request(
          `configuration?entry=${encodeURIComponent(config.entry)}`,
          "GET",
        );
        if (
          entry.origins?.length &&
          !entry.origins.includes("*") &&
          !entry.origins.includes(location.origin)
        )
          throw new Error(
            config.locale.startsWith("zh")
              ? "网站来源不被允许"
              : "Website origin is not allowed",
          );
        let session;
        if (read(storeKey)) {
          try {
            session = await request("identities", "POST", {});
          } catch (error) {
            if (error.status !== 401) throw error;
            save(storeKey, "");
          }
        }
        if (version !== identityVersion) return;
        let input = proof;
        if (!input && config.resolveIdentity) {
          const visitor =
            session?.user.type === "guest"
              ? await request("visitor-tickets", "POST", {})
              : null;
          input = await config.resolveIdentity({
            visitor_ticket: visitor?.ticket,
          });
        }
        input ||= { ticket: config.ticket, identity: config.identity };
        if (version !== identityVersion) return;
        if (!session || input.ticket || input.identity)
          session = await request("identities", "POST", {
            ...input,
            attributes: config.attributes,
            observed: context(),
            entry_key: config.entry,
          });
        if (version !== identityVersion) return;
        save(storeKey, session.token || read(storeKey));
        delete config.identity;
        delete config.ticket;
        config.onSessionChange?.({
          user: session.user,
          verified: session.verified,
        });
        post("identify", {
          token: read(storeKey),
          observed: context(),
          attributes: config.attributes,
          identity_version: version,
        });
        return session;
      };
      identityTask = identityTask
        .catch(() => {})
        .then(() =>
          navigator.locks
            ? navigator.locks.request(storeKey, synchronize)
            : synchronize(),
        );
      return identityTask;
    },
    identify(input) {
      if (input.attributes) Object.assign(config.attributes, input.attributes);
      return api.syncIdentity(input);
    },
    setTheme(value) {
      config.theme = value;
      host.style.colorScheme = value === "system" ? "light dark" : value;
      post("theme", { theme: value });
    },
    setLocale(value) {
      if (!config) return;
      config.locale = value.startsWith("zh") ? "zh" : "en";
      trigger.ariaLabel = config.label || (config.locale === "zh" ? "在线客服" : "Customer support");
      panel.setAttribute("aria-label", trigger.ariaLabel);
      (config.menu || []).forEach((item, index) => {
        menu.children[index].lastChild.textContent = typeof item.label === "function" ? item.label(config.locale) : item.label;
      });
      if (frame) frame.title = trigger.ariaLabel;
      post("locale", { locale: config.locale });
    },
    async reset() {
      if (!config) return;
      ++identityVersion;
      await identityTask.catch(() => {});
      const visible = !panel.hidden;
      frame?.remove();
      frame = undefined;
      if (read(storeKey)) {
        try {
          await request("session", "DELETE");
        } catch (error) {
          if (error.status !== 401) throw error;
        }
      }
      delete config.identity;
      delete config.ticket;
      config.attributes = {};
      save(storeKey, "");
      await api.syncIdentity();
      if (visible) await api.open();
    },
    destroy() {
      ++identityVersion;
      clearInterval(pageTimer);
      removeEventListener("resize", observer);
      document.removeEventListener("click", outsideClick);
      post("visible", { visible: false });
      frame?.remove();
      host?.remove();
      host = frame = panel = trigger = menu = undefined;
    },
  };
  addEventListener("message", (event) => {
    if (
      !frame ||
      event.source !== frame.contentWindow ||
      event.origin !== config.server ||
      event.data?.source !== "dawn-support" ||
      event.data.app !== config.app
    )
      return;
    if (event.data.type === "ready")
      post("init", {
        token: read(storeKey),
        locale: config.locale,
        attributes: config.attributes,
        identity: config.identity,
        ticket: config.ticket,
        observed: context(),
        link_rules: config.linkRules || [],
        identity_version: identityVersion,
      });
    if (
      event.data.type === "session" &&
      event.data.identity_version === identityVersion
    ) {
      save(storeKey, event.data.token);
      delete config.identity;
      delete config.ticket;
      config.onSessionChange?.({
        user: event.data.user,
        verified: event.data.verified,
      });
    }
    if (event.data.type === "identity_required")
      void api.syncIdentity().catch((error) => config.onError?.(error));
    if (event.data.type === "unread") {
      const badge = host.shadowRoot.getElementById("badge");
      badge.style.display = event.data.count ? "block" : "none";
    }
    if (event.data.type === "close") api.close();
    if (event.data.type === "navigate") {
      const url = new URL(event.data.url);
      if (
        !["http:", "https:"].includes(url.protocol) ||
        !(config.linkRules || []).some((rule) =>
          url.href.startsWith(new URL(rule.to, location.href).href),
        )
      )
        return;
      if (config.onNavigate) config.onNavigate(url.href);
      else window.open(url.href, "_blank", "noopener,noreferrer");
      if (config.closeOnMobileNavigate && innerWidth <= 600) api.close();
    }
  });
  window.DawnSupport = api;
  if (script?.dataset.app) {
    const options = { ...script.dataset };
    try {
      options.attributes = JSON.parse(options.attributes || "{}");
    } catch {
      options.attributes = {};
    }
    if (document.body) api.init(options);
    else
      addEventListener("DOMContentLoaded", () => api.init(options), {
        once: true,
      });
  }
})();
