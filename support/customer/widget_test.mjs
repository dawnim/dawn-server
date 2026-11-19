import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { runInNewContext } from "node:vm";

const script = readFileSync(
  new URL("./web/widget.js", import.meta.url),
  "utf8",
);

test("文档导航默认保持展开，可配置仅手机收起且复用原iframe", async () => {
  for (const [width, enabled, hidden] of [
    [390, undefined, false],
    [390, true, true],
    [1280, true, false],
  ]) {
    const elements = [],
      listeners = {},
      sent = [],
      opened = [];
    const element = () => {
      const node = {
        style: {},
        dataset: {},
        hidden: false,
        contentWindow: { postMessage: (data) => sent.push(data) },
        append() {},
        setAttribute() {},
        focus() {},
        attachShadow() {
          return { append() {} };
        },
        getBoundingClientRect() {
          return { top: 60 };
        },
      };
      elements.push(node);
      return node;
    };
    const window = { open: (url) => opened.push(url) };
    runInNewContext(script, {
      window,
      URL,
      URLSearchParams,
      AbortSignal,
      innerWidth: width,
      innerHeight: 844,
      location: { origin: "https://site.test", href: "https://site.test/" },
      navigator: { language: "zh-CN" },
      localStorage: { getItem: () => "" },
      fetch: async (url) => ({
        ok: true,
        status: 200,
        json: async () => ({
          data: new URL(url).pathname.endsWith("/configuration")
            ? { origins: ["*"] }
            : {
                token: "test-token",
                user: { type: "guest", unique_id: "test-guest" },
              },
        }),
      }),
      document: {
        currentScript: { src: "https://support.test/widget.js", dataset: {} },
        body: { append() {} },
        createElement: element,
        addEventListener() {},
        removeEventListener() {},
      },
      addEventListener: (name, listener) => {
        listeners[name] = listener;
      },
      setInterval() {},
    });
    const routes = [];
    window.DawnSupport.init({
      server: "https://support.test",
      app: "demo",
      linkRules: [{ from: "https://docs.test/", to: "/docs/" }],
      onNavigate: (url) => routes.push(url),
      closeOnMobileNavigate: enabled,
    });
    await window.DawnSupport.open();
    const panel = elements.find((node) => node.id === "panel");
    const iframe = elements.at(-1);
    const event = {
      source: iframe.contentWindow,
      origin: "https://support.test",
      data: { source: "dawn-support", app: "demo", type: "ready" },
    };
    listeners.message(event);
    assert.equal(sent.at(-1).link_rules[0].to, "/docs/");
    window.DawnSupport.setLocale("en");
    assert.equal(new URL(iframe.src).searchParams.get("locale"), "zh");
    assert.equal(sent.at(-1).type, "locale");
    assert.equal(sent.at(-1).locale, "en");
    event.data = {
      ...event.data,
      type: "navigate",
      url: "https://site.test/docs/install.html#start",
    };
    listeners.message({ ...event, origin: "https://untrusted.test" });
    listeners.message({
      ...event,
      data: { ...event.data, url: "https://site.test/docs-evil/install.html" },
    });
    assert.equal(routes.length, 0);
    listeners.message(event);
    assert.deepEqual(routes, ["https://site.test/docs/install.html#start"]);
    assert.equal(panel.hidden, hidden);
    const count = elements.length;
    await window.DawnSupport.open();
    assert.equal(panel.hidden, false);
    assert.equal(elements.length, count);
    assert.equal(elements.at(-1), iframe);
    assert.equal(opened.length, 0);
  }
});

function widget() {
  class Element {
    style = {};
    dataset = {};
    hidden = false;
    children = [];
    attributes = {};
    contentWindow = { postMessage() {} };
    append(...items) {
      this.children.push(...items);
    }
    contains(target) {
      return target === this;
    }
    attachShadow() {
      this.shadowRoot = new Element();
      return this.shadowRoot;
    }
    setAttribute(k, v) {
      this.attributes[k] = v;
    }
    getBoundingClientRect() {
      return { top: 100, height: 80 };
    }
    get lastChild() { return this.children.at(-1); }
    setPointerCapture() {}
    focus() {}
    remove() {}
  }
  const storage = new Map(),
    calls = [],
    sessions = new Map(),
    changes = [],
    documentListeners = new Map(),
    elements = [],
    navigator = { language: "zh-CN" };
  let next = 0;
  const sandbox = {
    URL,
    URLSearchParams,
    AbortSignal,
    Promise,
    console,
    location: new URL("https://site.example/"),
    navigator,
    innerWidth: 1200,
    innerHeight: 800,
    document: {
      currentScript: {
        src: "https://support.example/support-assets/widget.js",
        dataset: {},
      },
      body: new Element(),
      createElement: (tag) => {
        const element = new Element();
        element.tagName = tag;
        elements.push(element);
        return element;
      },
      addEventListener: (name, listener) => documentListeners.set(name, listener),
      removeEventListener: (name) => documentListeners.delete(name),
      title: "Site",
      referrer: "",
    },
    localStorage: {
      getItem: (k) => storage.get(k),
      setItem: (k, v) => storage.set(k, v),
      removeItem: (k) => storage.delete(k),
    },
    addEventListener() {},
    removeEventListener() {},
    setInterval() {},
    clearInterval() {},
    fetch: async (url, options) => {
      const path = new URL(url).pathname.split("/").at(-1),
        body = options.body ? JSON.parse(options.body) : {};
      const token = options.headers.Authorization?.slice(7);
      calls.push({ path, body, token, method: options.method });
      let data,
        status = 200;
      if (path === "configuration") data = { origins: ["*"] };
      if (path === "visitor-tickets") data = { ticket: `visitor:${token}` };
      if (path === "session") {
        sessions.delete(token);
        status = 204;
      }
      if (path === "identities") {
        data = sessions.get(token);
        if (body.identity || !token) {
          const type = body.identity?.startsWith("member") ? "member" : "guest";
          data = {
            token: `token-${++next}`,
            user: { unique_id: `${next}`, type },
            verified: !!body.identity,
          };
          sessions.set(data.token, data);
        } else if (!data) status = 401;
      }
      return {
        ok: status < 400,
        status,
        json: async () => ({ data, message: "expired" }),
      };
    },
  };
  sandbox.window = sandbox;
  runInNewContext(script, sandbox);
  return { api: sandbox.DawnSupport, calls, changes, documentListeners, elements, navigator };
}

test("外部收起后身份同步中仍可立即重开", async () => {
  const { api, documentListeners, elements, navigator } = widget();
  api.init({ server: "https://support.example", app: "demo" });
  await api.open();
  const panel = elements.find((node) => node.id === "panel"),
    frame = elements.find((node) => node.tagName === "iframe");
  documentListeners.get("click")({ target: {} });
  assert.equal(panel.hidden, true);

  let release;
  navigator.locks = {
    request: (_key, task) => new Promise((resolve) => (release = () => resolve(task()))),
  };
  const syncing = api.syncIdentity();
  await new Promise((resolve) => setImmediate(resolve));
  assert.ok(release);
  const reopening = api.open();
  assert.equal(panel.hidden, false);
  await reopening;
  assert.strictEqual(elements.find((node) => node.tagName === "iframe"), frame);
  release();
  await syncing;
});

test("initialization only creates identity; reverse ticket reaches resolver", async () => {
  const { api, calls } = widget();
  api.init({ server: "https://support.example", app: "demo" });
  await api.syncIdentity();
  assert.equal(calls.filter((c) => c.path === "sessions").length, 0);
  let received;
  await api.identify({ attributes: { nick: "A" } });
  api.destroy();
  api.init({
    server: "https://support.example",
    app: "demo",
    resolveIdentity: (input) => {
      received = input.visitor_ticket;
      return { identity: "guest-site" };
    },
  });
  const current = await api.syncIdentity();
  assert.ok(received.startsWith("visitor:"));
  assert.equal(current.user.type, "guest");
  api.destroy();
});

test("logout revokes member token and re-establishes independent guest", async () => {
  const { api, calls } = widget();
  let member = true;
  api.init({
    server: "https://support.example",
    app: "demo",
    resolveIdentity: () => ({
      identity: member ? "member-site" : "guest-site",
    }),
  });
  const signedIn = await api.syncIdentity();
  assert.equal(signedIn.user.type, "member");
  member = false;
  await api.reset();
  const current = await api.syncIdentity();
  assert.equal(current.user.type, "guest");
  assert.notEqual(current.user.unique_id, signedIn.user.unique_id);
  assert.ok(
    calls.some(
      (c) =>
        c.path === "session" &&
        c.method === "DELETE" &&
        c.token === signedIn.token,
    ),
  );
  api.destroy();
});

test("a late resolver result cannot replace a newer identity", async () => {
  const { api } = widget();
  let release, started;
  const firstStarted = new Promise((resolve) => {
    started = resolve;
  });
  api.init({
    server: "https://support.example",
    app: "demo",
    resolveIdentity: () => {
      started();
      return new Promise((resolve) => {
        release = resolve;
      });
    },
  });
  await firstStarted;
  const newest = api.identify({ identity: "member-current" });
  release({ identity: "guest-late" });
  const current = await newest;
  assert.equal(current.user.type, "member");
  api.destroy();
});


test("自定义菜单支持图标、布局、语言切换和键盘操作", async () => {
  const { api, elements } = widget();
  let selected = false;
  api.init({
    server: "https://support.example", app: "demo", menuLayout: "horizontal",
    menu: [
      { label: (locale) => locale === "zh" ? "客服" : "Support", icon: "/support.svg", onClick: (support) => support.open() },
      { label: (locale) => locale === "zh" ? "聊天" : "Chat", onClick: () => { selected = true; } },
    ],
  });
  const trigger = elements.find((node) => node.id === "trigger");
  const menu = elements.find((node) => node.children.length === 2 && node.style.flexDirection === "row");
  assert.equal(menu.hidden, true);
  trigger.onkeydown({ key: "Enter", preventDefault() {} });
  assert.equal(menu.hidden, false);
  assert.equal(menu.children[0].children[0].src, "/support.svg");
  api.setLocale("en");
  assert.equal(menu.children[0].lastChild.textContent, "Support");
  menu.children[1].onclick();
  assert.equal(selected, true);
  assert.equal(menu.hidden, true);
  api.destroy();
});
