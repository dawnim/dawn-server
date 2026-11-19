(() => {
  "use strict";
  const $ = (id) => document.getElementById(id);
  const english = {
    这次服务体验如何: "How was your experience?",
    非常不满意: "Very dissatisfied",
    不满意: "Dissatisfied",
    一般: "Neutral",
    满意: "Satisfied",
    非常满意: "Very satisfied",
    "补充建议（选填）": "Additional feedback (optional)",
    提交评价: "Submit feedback",
    表情: "Emoji",
    在线客服: "Customer support",
    跟随系统: "System",
    亮色: "Light",
    暗色: "Dark",
    关闭: "Close",
    加载更早消息: "Load earlier messages",
    转人工: "Talk to a person",
    评价服务: "Rate service",
    发送: "Send",
    请输入消息: "Write a message",
    AI客服: "AI assistant",
    等待人工接待: "Waiting for an agent",
    人工客服: "Support agent",
    会话已结束: "Conversation ended",
    连接中: "Connecting",
    已连接: "Connected",
    连接已断开: "Disconnected",
    "因长时间离线，本次人工接待已结束":
      "This support session ended after a prolonged disconnection",
    "因长时间离线，本次人工排队已取消":
      "Your support queue request was cancelled after a prolonged disconnection",
    请求失败: "Request failed",
    正在回复: "Replying",
    回复已停止: "Reply stopped",
    暂未生成回复: "No reply generated",
    "发送失败，点击重试": "Failed to send. Click to retry",
    "回复失败，请重试或转人工": "Reply failed. Try again or talk to an agent",
    "请给本次服务评分（1～5）": "Rate this service (1–5)",
    感谢您的评价: "Thank you for your feedback",
    已申请人工客服: "An agent has been requested",
    客服已接待: "An agent has joined",
    已转交其他客服: "Transferred to another agent",
    已恢复AI接待: "AI assistance resumed",
    客户评价: "Customer feedback",
    "凭证已失效，请刷新页面": "Your session has expired. Reload the page",
    重新连接: "Reconnect",
    确认操作: "Confirm action",
    拒绝操作: "Decline action",
    操作已完成: "Action completed",
    操作已拒绝: "Action declined",
    操作执行失败: "Action failed",
  };
  Object.assign(english, {
    "客服申请结束，请客户确认": "Your agent asks to end this conversation",
    等待客户确认结束: "Waiting for your confirmation",
    同意结束: "Agree to end",
    继续咨询: "Keep chatting",
    撤销结束申请: "Withdraw end request",
    客户已同意结束: "You agreed to end this conversation",
    客户继续咨询: "Conversation continued",
    客服已撤销结束申请: "Your agent withdrew the end request",
    结束申请已失效: "This end request has expired",
    重试回复: "Retry reply",
    重试发送: "Retry send",
    该次接待已经评价: "This session has already been rated",
  });
  let language = (new URLSearchParams(location.search).get("locale") || navigator.language).startsWith("zh") ? "zh" : "en";
  const t = (key) => (language === "zh" ? key : english[key] || key);
  Object.assign(english, {
    结束咨询: "End conversation",
    好评: "Good",
    差评: "Poor",
    还有疑问: "More questions",
    已评价: "Rated",
  });
  const params = new URLSearchParams(location.search);
  const appKey = decodeURIComponent(
    location.pathname.split("/").filter(Boolean).pop(),
  );
  const base = `/api/support/${encodeURIComponent(appKey)}`;
  const embedded = params.get("embed") === "1" && parent !== window;
  document.documentElement.classList.toggle("embedded", embedded);
  $("close").hidden = !embedded;
  const parentOrigin = params.get("parent_origin");
  let linkRules = [],
    identityVersion = 0;
  document.addEventListener("click", (event) => {
    const link = event.target.closest("a[data-support-navigate]");
    if (
      !link ||
      event.defaultPrevented ||
      event.button ||
      event.metaKey ||
      event.ctrlKey ||
      event.shiftKey ||
      event.altKey
    )
      return;
    event.preventDefault();
    tell("navigate", { url: link.href });
  });
  const storeKey = `dawn.support:${appKey}`;
  const requestID = () =>
    crypto.randomUUID
      ? crypto.randomUUID()
      : Array.from(crypto.getRandomValues(new Uint8Array(16)), (value) =>
          value.toString(16).padStart(2, "0"),
        ).join("");
  function read(key, store) {
    try {
      return (store || window.localStorage).getItem(key) || "";
    } catch {
      return "";
    }
  }
  function save(key, value, store) {
    try {
      store ||= window.localStorage;
      value ? store.setItem(key, value) : store.removeItem(key);
    } catch {
      /* Storage can be disabled by the browser. */
    }
  }
  function tell(type, data) {
    if (embedded && parentOrigin)
      parent.postMessage(
        { source: "dawn-support", type, app: appKey, ...data },
        parentOrigin,
      );
  }
  let token = read(storeKey),
    conversation,
    configuration,
    socket,
    stopped = false,
    reconnectTimer,
    retry = 0,
    resume = "",
    sequence = Date.now(),
    previewTimer,
    initialized = false;
  const messages = new Map(),
    nodes = new Map(),
    seen = new Set(),
    streams = new Map();
  let beforeId,
    hasMore = false,
    loadingHistory = false,
    sending = false,
    lastFrame = 0,
    animation = 0,
    lastRead = 0,
    widgetVisible = true,
    unread = 0;
  const ratingComments = new Map(),
    ratingExpanded = new Set(),
    ratingPending = new Set();
  let segmenter = new Intl.Segmenter(language, { granularity: "grapheme" });
  const systemTheme = matchMedia("(prefers-color-scheme: dark)");
  let themeMode = "system";
  function theme(value) {
    themeMode = ["system", "light", "dark"].includes(value) ? value : "system";
    document.documentElement.dataset.theme =
      themeMode === "system"
        ? systemTheme.matches
          ? "dark"
          : "light"
        : themeMode;
    const labels = { system: "跟随系统", light: "亮色", dark: "暗色" };
    const paths = {
      system:
        '<rect x="3" y="4" width="18" height="13" rx="2"/><path d="M8 21h8M12 17v4"/>',
      light:
        '<circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M2 12h2m16 0h2M5 5l1.5 1.5m11 11L19 19M5 19l1.5-1.5m11-11L19 5"/>',
      dark: '<path d="M21 12.8A9 9 0 0 1 11.2 3 9 9 0 1 0 21 12.8Z"/>',
    };
    $("theme").innerHTML = `<svg viewBox="0 0 24 24">${paths[themeMode]}</svg>`;
    $("theme").title = t(labels[themeMode]);
    $("theme").ariaLabel = t(labels[themeMode]);
  }
  function locale(value) {
    language = value.startsWith("zh") ? "zh" : "en";
    segmenter = new Intl.Segmenter(language, { granularity: "grapheme" });
    document.documentElement.lang = language;
    document.title = configuration?.name || t("在线客服");
    document.querySelectorAll("[data-t]").forEach((element) => {
      element.textContent = t(element.dataset.t);
    });
    $("input").placeholder = t("请输入消息");
    $("close").ariaLabel = t("关闭");
    $("emoji-toggle").ariaLabel = $("emoji-toggle").title = t("表情");
    $("emoji-panel").ariaLabel = t("表情");
    $("connection").textContent = t(socket?.readyState === WebSocket.OPEN ? "已连接" : socket?.readyState === WebSocket.CLOSED ? "连接已断开" : "连接中");
    theme(themeMode);
    const top = $("messages").scrollTop;
    status();
    $("messages").scrollTop = top;
  }
  locale(language);
  theme(params.get("theme") || read(`${storeKey}:theme`) || "system");
  systemTheme.addEventListener("change", () => {
    if (themeMode === "system") theme("system");
  });
  $("theme").onclick = () => {
    theme(
      ["system", "light", "dark"][
        (["system", "light", "dark"].indexOf(themeMode) + 1) % 3
      ],
    );
    save(`${storeKey}:theme`, themeMode);
  };
  $("close").onclick = () => tell("close", {});
  function notice(message) {
    $("notice").textContent = message;
    $("notice").hidden = !message;
  }
  async function api(path, method = "GET", data) {
    const response = await fetch(path, {
      method,
      credentials: "omit",
      headers: {
        "Content-Type": "application/json",
        "Accept-Language": language,
        ...(token ? { Authorization: `Bearer ${token}` } : {}),
      },
      ...(data === undefined ? {} : { body: JSON.stringify(data) }),
    });
    const result = response.status === 204 ? {} : await response.json();
    if (!response.ok) {
      const error = new Error(result.message || t("请求失败"));
      error.status = response.status;
      throw error;
    }
    return result;
  }
  function applyConversation(updated) {
    if (conversation && updated.version < conversation.version) return;
    if (
      conversation &&
      updated.version === conversation.version &&
      updated.support_session?.id === conversation.support_session?.id &&
      (updated.support_session?.version || 0) <
        (conversation.support_session?.version || 0)
    )
      return;
    conversation = updated;
    status();
  }
  async function syncConversation() {
    const result = await api(`${base}/conversations/${conversation.id}`);
    applyConversation(result.data);
  }
  function status() {
    $("status").textContent = t(
      {
        ai: "AI客服",
        waiting: "等待人工接待",
        human: "人工客服",
        closed: "会话已结束",
      }[conversation?.status] || "AI客服",
    );
    const session = conversation?.support_session;
    $("end").hidden =
      !session ||
      !["waiting", "active", "closing_pending"].includes(session.status);
    if (session?.status === "closing_pending")
      $("status").textContent = t("等待客户确认结束");
    render();
    $("human").disabled =
      !conversation || ["waiting", "human"].includes(conversation.status);
  }
  function scrollBottom(force = false) {
    const element = $("messages");
    if (
      force ||
      element.scrollHeight - element.scrollTop - element.clientHeight < 150
    )
      element.scrollTop = element.scrollHeight;
  }
  function paint(message) {
    let node = nodes.get(message.request_id || message.id);
    if (!node) {
      node = document.createElement("article");
      node.className = "message";
      const bubble = document.createElement("div");
      bubble.className = "bubble";
      const meta = document.createElement("small"),
        sources = document.createElement("div"),
        actions = document.createElement("div");
      sources.className = "sources";
      actions.className = "actions";
      const avatar = document.createElement("span");
      avatar.className = "message-avatar";
      const own = message.sender_realm?.startsWith("support:");
      if (!own && configuration?.avatar) {
        const img = document.createElement("img");
        img.src = configuration.avatar;
        img.alt = "";
        avatar.append(img);
      } else if (own)
        avatar.innerHTML =
          '<svg viewBox="0 0 24 24"><circle cx="12" cy="8" r="4"/><path d="M4 21v-2a8 8 0 0 1 16 0v2"/></svg>';
      else avatar.append($("avatar-fallback").firstChild.cloneNode(true));
      node.append(bubble, meta, sources, actions, avatar);
      nodes.set(message.request_id || message.id, node);
    }
    node.classList.toggle("own", message.sender_realm?.startsWith("support:"));
    node.classList.toggle("event", message.type === "event");
    const events = {
      human: "已申请人工客服",
      claim: "客服已接待",
      transfer: "已转交其他客服",
      ai: "已恢复AI接待",
      close: "客服申请结束，请客户确认",
      cancel_close: "客服已撤销结束申请",
      closing_accepted: "客户已同意结束",
      offline_closed: "因长时间离线，本次人工接待已结束",
      offline_cancelled: "因长时间离线，本次人工排队已取消",
      closing_rejected: "客户继续咨询",
    };
    const content =
      message.type === "event"
        ? t(events[message.content] || message.content)
        : (message.display ?? message.content ?? "");
    const bubble = node.firstChild;
    bubble.classList.toggle("chat-markdown", message.type !== "event");
    if (message.type === "event") bubble.textContent = content;
    else bubble.innerHTML = DawnMarkdown.renderChatMarkdown(content);
    const emptyReply = message.sender_realm === "ai" && !content;
    node.classList.toggle("empty-reply", emptyReply);
    node.classList.remove("has-retry");
    if (emptyReply) {
      const placeholder = document.createElement("span");
      placeholder.className = "reply-placeholder";
      const replying = ["queued", "streaming"].includes(message.status);
      placeholder.textContent = replying
        ? t("正在回复")
        : t(message.status === "cancelled" ? "回复已停止" : "暂未生成回复");
      placeholder.classList.toggle("replying", replying);
      bubble.append(placeholder);
    }
    node.children[1].textContent = ["queued", "streaming"].includes(
      message.status,
    )
      ? t("正在回复")
      : message.status === "failed"
        ? t(
            message.sender_realm?.startsWith("support:")
              ? "发送失败，点击重试"
              : "回复失败，请重试或转人工",
          )
        : message.status === "cancelled"
          ? t("回复已停止")
          : new Date(message.created_at).toLocaleTimeString(language, {
              hour: "2-digit",
              minute: "2-digit",
            });
    if (
      message.status === "failed" &&
      message.sender_realm?.startsWith("support:")
    ) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "reply-retry";
      button.title = button.ariaLabel = t("重试发送");
      button.innerHTML =
        '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 7v5h-5"/><path d="M19.2 12a7.2 7.2 0 1 0-1.7 4.7M20 12l-3-3"/></svg>';
      button.onclick = (event) => {
        event.stopPropagation();
        void deliver(message);
      };
      node.classList.add("has-retry");
      bubble.append(button);
    }
    const sources = node.children[2];
    sources.replaceChildren();
    const linked = new Set();
    for (const source of message.payload?.sources || []) {
      if (!source.location || linked.has(source.location)) continue;
      linked.add(source.location);
      try {
        const url = new URL(source.location);
        if (!["https:", "http:"].includes(url.protocol)) continue;
        const link = document.createElement("a");
        link.href = url.href;
        link.rel = "noopener noreferrer";
        link.target = "_blank";
        link.textContent = source.title;
        sources.append(link);
      } catch {
        /* File references have no public link. */
      }
    }
    const actions = node.children[3];
    // 正文与来源使用同一规则；只转换接入方明确声明的URL前缀。
    for (const link of node.querySelectorAll("a[href]")) {
      const rule = linkRules.find((rule) => link.href.startsWith(rule.from));
      if (!rule) continue;
      const url = new URL(
        rule.to + link.href.slice(rule.from.length),
        parentOrigin || location.origin,
      );
      if (!["http:", "https:"].includes(url.protocol)) continue;
      link.href = url.href;
      link.dataset.supportNavigate = "";
    }
    const activeSession = conversation?.support_session;
    const sessionId = message.payload?.session_id;
    const rated =
      (activeSession?.id === sessionId && activeSession?.rating != null) ||
      [...messages.values()].find(
        (row) =>
          row.content === "客户评价" && row.payload?.session_id === sessionId,
      );
    if (
      message.type === "event" &&
      ["close", "closing_accepted", "offline_closed"].includes(message.content)
    ) {
      const state = JSON.stringify([
        activeSession?.id,
        activeSession?.version,
        activeSession?.status,
        !!rated,
      ]);
      if (actions.dataset.state === state) return node;
      actions.dataset.state = state;
    }
    actions.replaceChildren();
    if (
      message.type === "event" &&
      ["close", "closing_accepted", "offline_closed"].includes(
        message.content,
      ) &&
      sessionId &&
      !rated
    ) {
      const pending = message.content === "close";
      const valid =
        !pending ||
        (activeSession?.id === sessionId &&
          activeSession.version === message.payload.session_version &&
          activeSession.status === "closing_pending");
      if (valid) {
        const comment = document.createElement("textarea");
        comment.placeholder = t("补充建议（选填）");
        comment.maxLength = 2000;
        comment.rows = 2;
        comment.value = ratingComments.get(sessionId) || "";
        comment.oninput = () => ratingComments.set(sessionId, comment.value);
        const extra = document.createElement("details"),
          summary = document.createElement("summary");
        summary.textContent = t("补充建议（选填）");
        extra.append(summary, comment);
        extra.open = ratingExpanded.has(sessionId);
        extra.ontoggle = () => {
          if (extra.open) ratingExpanded.add(sessionId);
          else ratingExpanded.delete(sessionId);
        };
        for (const [rating, label] of [
          [3, "好评"],
          [2, "一般"],
          [1, "差评"],
        ]) {
          const button = document.createElement("button");
          button.type = "button";
          button.textContent = t(label);
          button.disabled = ratingPending.has(sessionId);
          button.onclick = async () => {
            ratingPending.add(sessionId);
            for (const item of actions.querySelectorAll("button"))
              item.disabled = true;
            try {
              await api(
                `${base}/conversations/${conversation.id}/ratings`,
                "POST",
                {
                  session_id: sessionId,
                  version: message.payload.session_version,
                  rating,
                  comment: comment.value,
                },
              );
              await history();
              await syncConversation();
              ratingComments.delete(sessionId);
              notice(t("感谢您的评价"));
            } catch (error) {
              notice(error.message);
              for (const item of actions.querySelectorAll("button"))
                item.disabled = false;
            } finally {
              ratingPending.delete(sessionId);
            }
          };
          actions.append(button);
        }
        if (pending) {
          const button = document.createElement("button");
          button.type = "button";
          button.textContent = t("还有疑问");
          button.onclick = async () => {
            button.disabled = true;
            try {
              const result = await api(
                `${base}/conversations/${conversation.id}/closing-request`,
                "PUT",
                {
                  session_id: sessionId,
                  version: activeSession.version,
                  approved: false,
                },
              );
              applyConversation(result.data);
            } catch (error) {
              notice(error.message);
              await syncConversation().catch(() => {});
              button.disabled = false;
            }
          };
          actions.append(button);
        }
        actions.append(extra);
      }
    }
    if (
      rated &&
      ["close", "closing_accepted", "offline_closed"].includes(message.content)
    )
      actions.textContent = t("已评价");
    if (
      message.sender_realm === "ai" &&
      ["failed", "cancelled"].includes(message.status) &&
      conversation?.status === "ai"
    ) {
      const button = document.createElement("button");
      button.type = "button";
      button.className = "reply-retry";
      button.title = button.ariaLabel = t("重试回复");
      button.innerHTML =
        '<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 7v5h-5"/><path d="M19.2 12a7.2 7.2 0 1 0-1.7 4.7M20 12l-3-3"/></svg>';
      button.onclick = async (event) => {
        event.stopPropagation();
        button.disabled = true;
        try {
          const result = await api(
            `${base}/conversations/${conversation.id}/messages/${message.id}/retries`,
            "POST",
            {},
          );
          receive(result.data);
        } catch (error) {
          notice(error.message);
          button.disabled = false;
        }
      };
      node.classList.add("has-retry");
      node.firstChild.append(button);
    }
    if (message.type === "action") {
      if (
        message.payload?.action_status === "waiting" &&
        message.status !== "cancelled"
      ) {
        for (const [approved, label] of [
          [true, "确认操作"],
          [false, "拒绝操作"],
        ]) {
          const button = document.createElement("button");
          button.type = "button";
          button.textContent = t(label);
          button.onclick = async () => {
            button.disabled = true;
            try {
              await api(`${base}/actions/${message.payload.action_id}`, "PUT", {
                approved,
              });
            } catch (error) {
              notice(error.message);
              button.disabled = false;
            }
          };
          actions.append(button);
        }
      } else {
        actions.textContent = t(
          {
            completed: "操作已完成",
            cancelled: "操作已拒绝",
            failed: "操作执行失败",
          }[message.payload?.action_status] || "回复已停止",
        );
      }
    }
    return node;
  }
  function render() {
    const atBottom =
      $("messages").scrollHeight -
        $("messages").scrollTop -
        $("messages").clientHeight <
      150;
    const rows = [...messages.values()].sort(
      (a, b) => (a.id || Infinity) - (b.id || Infinity),
    );
    for (const message of rows) $("history").append(paint(message));
    if (atBottom) scrollBottom(true);
  }
  function frame(now) {
    const delta = Math.min(80, now - (lastFrame || now));
    lastFrame = now;
    for (const [id, stream] of streams) {
      stream.credit += (delta * stream.rate) / 1000;
      const count = Math.min(
        stream.target.length - stream.position,
        Math.floor(stream.credit),
      );
      if (count > 0) {
        stream.position += count;
        stream.credit -= count;
        stream.message.display = stream.target
          .slice(0, stream.position)
          .join("");
        paint(stream.message);
        scrollBottom();
      }
      if (stream.position >= stream.target.length) {
        streams.delete(id);
        void readPosition();
      }
    }
    animation = streams.size ? requestAnimationFrame(frame) : 0;
    if (!animation) lastFrame = 0;
  }
  function receive(saved, animate = false) {
    if (saved.visibility && saved.visibility !== "public") return;
    const key =
      saved.request_id ||
      [...messages].find(([, item]) => item.id === saved.id)?.[0] ||
      saved.id;
    const previous = messages.get(key);
    if (
      animate &&
      previous &&
      ["completed", "cancelled", "failed"].includes(previous.status)
    )
      return;
    if (
      animate &&
      previous &&
      ((saved.attempt || 0) < (previous.attempt || 0) ||
        (saved.attempt === previous.attempt &&
          saved.sequence <= previous.sequence))
    )
      return;
    if (
      !widgetVisible &&
      saved.status === "completed" &&
      previous?.status !== "completed" &&
      !saved.sender_realm?.startsWith("support:")
    ) {
      unread++;
      tell("unread", { count: unread });
    }
    const message = previous || { ...saved };
    if (animate && previous && saved.attempt > previous.attempt)
      message.display = "";
    let displayed = previous
      ? (message.display ?? message.content ?? "")
      : animate
        ? ""
        : saved.content || "";
    if (saved.content !== undefined && !saved.content.startsWith(displayed))
      displayed = "";
    Object.assign(message, saved);
    messages.set(key, message);
    if (
      message.type !== "event" &&
      (animate || streams.has(message.id)) &&
      !["cancelled", "failed"].includes(message.status)
    ) {
      const old = streams.get(message.id),
        now = performance.now(),
        target = [...segmenter.segment(message.content || "")].map(
          (part) => part.segment,
        );
      const position = Math.min(
        [...segmenter.segment(displayed)].length,
        target.length,
      );
      const interval = old
        ? Math.min(1800, Math.max(100, now - old.arrived))
        : Math.min(
            1600,
            Math.max(180, Date.now() - (message.created_at || Date.now())),
          );
      const rate = Math.max(
        12,
        Math.min(600, (target.length - position) / (interval / 1000)),
      );
      message.display = displayed;
      streams.set(message.id, {
        message,
        target,
        position,
        rate,
        arrived: now,
        credit: old?.credit || 0,
      });
      if (!animation) animation = requestAnimationFrame(frame);
    } else {
      streams.delete(message.id);
      delete message.display;
    }
    render();
    void readPosition();
  }
  let reading = false;
  async function readPosition() {
    if (
      reading ||
      document.hidden ||
      !widgetVisible ||
      !conversation ||
      $("messages").scrollHeight -
        $("messages").scrollTop -
        $("messages").clientHeight >
        60
    )
      return;
    const last = [...messages.values()]
      .filter(
        (message) =>
          message.id &&
          message.status === "completed" &&
          !streams.has(message.id),
      )
      .sort((a, b) => b.id - a.id)[0];
    if (!last || last.id <= lastRead) return;
    reading = true;
    try {
      await api(
        `${base}/conversations/${conversation.id}/read-position`,
        "PUT",
        { message_id: last.id },
      );
      lastRead = Math.max(lastRead, last.id);
    } catch {
      /* A reconnect retries the position. */
    } finally {
      reading = false;
    }
  }
  async function history(older = false) {
    if (!conversation || loadingHistory) return;
    loadingHistory = true;
    const height = $("messages").scrollHeight;
    try {
      const result = await api(
        `${base}/conversations/${conversation.id}/messages${older && beforeId ? `?before_id=${beforeId}` : ""}`,
      );
      for (const message of result.data.reverse()) receive(message);
      beforeId = result.meta.before_id;
      hasMore = result.meta.has_more;
      $("older").hidden = !hasMore;
      if (older) $("messages").scrollTop = $("messages").scrollHeight - height;
      else scrollBottom(true);
    } finally {
      loadingHistory = false;
    }
  }
  $("older").onclick = () =>
    history(true).catch((error) => notice(error.message));
  $("messages").onscroll = () => void readPosition();
  function preview() {
    if (previewTimer) return;
    if (!configuration?.input_preview || socket?.readyState !== WebSocket.OPEN)
      return;
    previewTimer = setTimeout(() => {
      previewTimer = undefined;
      if (socket?.readyState === WebSocket.OPEN)
        socket.send(
          JSON.stringify({
            type: "support.input",
            request_id: requestID(),
            data: {
              conversation_id: conversation.id,
              content: $("input").value,
              sequence: ++sequence,
            },
          }),
        );
    }, 180);
  }
  for (const emoji of [
    "😀",
    "😄",
    "😆",
    "😂",
    "😉",
    "😊",
    "🙂",
    "😋",
    "😌",
    "😍",
    "😘",
    "😎",
    "🤓",
    "🤗",
    "🤔",
    "🙄",
    "😅",
    "🥹",
    "😭",
    "😢",
    "😤",
    "😱",
    "🥳",
    "🤝",
    "👍",
    "👎",
    "👌",
    "🙏",
    "👏",
    "💪",
    "👋",
    "🙌",
    "❤️",
    "💚",
    "💛",
    "💙",
    "🎉",
    "🎁",
    "🌹",
    "☕",
    "✨",
    "✅",
    "💡",
    "📌",
    "🚀",
    "⭐",
    "🔥",
    "💯",
  ]) {
    const button = document.createElement("button");
    button.type = "button";
    button.textContent = emoji;
    button.onmousedown = (event) => event.preventDefault();
    button.onclick = () => {
      const input = $("input");
      input.setRangeText(
        emoji,
        input.selectionStart,
        input.selectionEnd,
        "end",
      );
      input.focus();
      preview();
    };
    $("emoji-panel").append(button);
  }
  $("emoji-toggle").onclick = () => {
    $("emoji-panel").hidden = !$("emoji-panel").hidden;
    $("emoji-toggle").setAttribute(
      "aria-expanded",
      String(!$("emoji-panel").hidden),
    );
  };
  $("input").oninput = preview;
  $("input").onkeydown = (event) => {
    if (
      event.key === "Enter" &&
      !event.shiftKey &&
      !event.isComposing &&
      matchMedia("(pointer:fine)").matches
    ) {
      event.preventDefault();
      $("composer").requestSubmit();
    }
  };
  async function deliver(message) {
    message.status = "sending";
    render();
    try {
      const result = await api(
        `${base}/conversations/${conversation.id}/messages`,
        "POST",
        { request_id: message.request_id, content: message.content },
      );
      receive(result.data);
      void syncConversation().catch(() => {});
      notice("");
    } catch (error) {
      message.status = "failed";
      render();
      notice(error.message);
    }
  }
  $("composer").onsubmit = async (event) => {
    event.preventDefault();
    const content = $("input").value;
    if (!content.trim() || !conversation || sending) return;
    sending = true;
    $("send").disabled = true;
    $("input").value = "";
    $("emoji-panel").hidden = true;
    $("emoji-toggle").setAttribute("aria-expanded", "false");
    preview();
    const message = {
      id: 0,
      request_id: requestID(),
      conversation_id: conversation.id,
      sender_realm: `support:${conversation.app_id}`,
      content,
      type: "text",
      status: "sending",
      created_at: Date.now(),
    };
    messages.set(message.request_id, message);
    render();
    scrollBottom(true);
    await deliver(message);
    sending = false;
    $("send").disabled = false;
  };
  $("human").onclick = async () => {
    try {
      const result = await api(
        `${base}/conversations/${conversation.id}/human-requests`,
        "POST",
        {},
      );
      applyConversation(result.data);
    } catch (error) {
      notice(error.message);
    }
  };
  $("end").onclick = async () => {
    const session = conversation?.support_session;
    if (!session) return;
    $("end").disabled = true;
    try {
      const result = await api(
        `${base}/conversations/${conversation.id}/endings`,
        "POST",
        { session_id: session.id, version: session.version },
      );
      applyConversation(result.data);
      await history();
    } catch (error) {
      notice(error.message);
      await syncConversation().catch(() => {});
    } finally {
      $("end").disabled = false;
    }
  };
  function reconnect() {
    if (stopped) return;
    clearTimeout(reconnectTimer);
    $("connection").textContent = t("连接中");
    reconnectTimer = setTimeout(connect, Math.min(30000, 500 * 2 ** retry++));
  }
  async function connect() {
    if (stopped) return;
    try {
      const result = await api(`${base}/realtime-tickets`, "POST", { resume });
      const url = new URL(result.data.ws_url, location.href);
      url.protocol =
        url.protocol === "https:"
          ? "wss:"
          : url.protocol === "http:"
            ? "ws:"
            : url.protocol;
      const connection = new WebSocket(url);
      socket = connection;
      connection.onopen = () =>
        connection.send(
          JSON.stringify({ type: "auth", ticket: result.data.ticket }),
        );
      let processing = Promise.resolve();
      connection.onmessage = (event) => {
        processing = processing
          .then(async () => {
            const packet = JSON.parse(event.data),
              key = `${packet.channel}:${packet.id}`;
            if (packet.type === "ready") {
              resume = packet.data.resume;
              retry = 0;
              $("connection").textContent = t("已连接");
              await history();
              await syncConversation();
            } else if (packet.type === "resync_required") {
              await history();
              await syncConversation();
            } else if (
              packet.type.startsWith("chat.message.") &&
              !seen.has(key)
            ) {
              const message = packet.data.data || packet.data;
              if (message.conversation_id === conversation.id)
                receive(message, packet.type === "chat.message.delta");
            } else if (packet.type === "support.reception.updated") {
              const updated = packet.data.data || packet.data;
              if (updated.conversation_id === conversation.id) {
                applyConversation(updated.conversation);
              }
            }
            if (packet.id) {
              seen.add(key);
              if (seen.size > 2000) seen.delete(seen.values().next().value);
              if (connection.readyState === WebSocket.OPEN)
                connection.send(
                  JSON.stringify({
                    type: "ack",
                    id: packet.id,
                    channel: packet.channel,
                  }),
                );
            }
          })
          .catch(() => connection.close());
      };
      connection.onclose = () => {
        if (socket === connection) reconnect();
      };
      connection.onerror = () => connection.close();
    } catch (error) {
      if (resume) {
        resume = "";
        reconnect();
        return;
      }
      if (error.status === 401 && embedded) {
        initialized = false;
        tell("identity_required", {});
        return;
      }
      if (error.status === 401) {
        notice(t("凭证已失效，请刷新页面"));
        $("connection").textContent = t("连接已断开");
        return;
      }
      reconnect();
    }
  }
  async function initialize(input = {}) {
    if (
      initialized &&
      !input.identity &&
      !input.ticket &&
      (!input.token || input.token === token)
    ) {
      if (conversation && (input.attributes || input.observed))
        await api(`${base}/context`, "PATCH", {
          attributes: input.attributes,
          observed: input.observed,
        }).catch((error) => notice(error.message));
      return;
    }
    initialized = true;
    identityVersion = input.identity_version || identityVersion;
    if (input.token) token = input.token;
    const attributes = Object.fromEntries(
      [...params].filter(
        ([key]) => !["embed", "parent_origin", "theme", "locale", "entry"].includes(key),
      ),
    );
    const observed = {
      page_url: location.href.split("#")[0],
      referrer: document.referrer,
      language: navigator.language,
      time_zone: Intl.DateTimeFormat().resolvedOptions().timeZone,
      screen_width: screen.width,
      screen_height: screen.height,
      viewport_width: innerWidth,
      viewport_height: innerHeight,
      ...input.observed,
    };
    try {
      configuration = (
        await api(
          `${base}/configuration?entry=${encodeURIComponent(params.get("entry") || "default")}`,
        )
      ).data;
      if (
        embedded &&
        configuration.origins?.length &&
        !configuration.origins.includes("*") &&
        !configuration.origins.includes(parentOrigin)
      )
        throw new Error(t("请求失败"));
      if (!params.has("theme") && !read(`${storeKey}:theme`))
        theme(configuration.theme);
      $("brand").textContent = configuration.name;
      document.title = configuration.name;
      if (configuration.avatar) {
        $("avatar").src = configuration.avatar;
        $("avatar").hidden = false;
        $("avatar-fallback").hidden = true;
      }
      $("greeting").textContent = configuration.greeting || "";
      const result = await api(`${base}/sessions`, "POST", {
        entry_key: params.get("entry") || "default",
        conversation_id: conversation?.id,
        attributes: { ...attributes, ...input.attributes },
        observed,
        identity: input.identity,
        ticket: input.ticket,
      });
      token = result.data.session.token || token;
      conversation = result.data.conversation;
      save(storeKey, token);
      tell("session", {
        token,
        user: result.data.session.user,
        verified: result.data.session.verified,
        identity_version: identityVersion,
      });
      status();
      $("input").disabled = false;
      $("send").disabled = false;
      socket?.close();
      socket = undefined;
      clearTimeout(reconnectTimer);
      resume = "";
      messages.clear();
      nodes.clear();
      $("history").replaceChildren();
      await history();
      await connect();
    } catch (error) {
      if (
        error.status === 401 &&
        embedded &&
        !input.identity &&
        !input.ticket
      ) {
        initialized = false;
        tell("identity_required", {});
        return;
      }
      if (error.status === 401 && token && !input.identity && !input.ticket) {
        token = "";
        save(storeKey, "");
        initialized = false;
        return initialize({ ...input, token: "" });
      }
      notice(error.message);
    }
  }
  if (embedded) {
    let initialization = Promise.resolve();
    addEventListener("message", (event) => {
      if (
        event.source !== parent ||
        event.origin !== parentOrigin ||
        event.data?.source !== "dawn-support-host"
      )
        return;
      if (event.data.type === "init") linkRules = event.data.link_rules || [];
      if (event.data.type === "init" || event.data.type === "identify")
        initialization = initialization
          .then(() => initialize(event.data))
          .catch((error) => notice(error.message));
      if (event.data.type === "locale" || (event.data.type === "init" && event.data.locale)) locale(event.data.locale);
      if (event.data.type === "theme") theme(event.data.theme);
      if (event.data.type === "visible") {
        widgetVisible = event.data.visible;
        if (widgetVisible) {
          unread = 0;
          tell("unread", { count: 0 });
          void readPosition();
        }
      }
      if (event.data.type === "context" && conversation)
        void api(`${base}/context`, "PATCH", {
          observed: event.data.observed,
        }).catch(() => {});
      if (event.data.type === "reset") {
        stopped = true;
        socket?.close();
        void api(`${base}/session`, "DELETE").finally(() => {
          save(storeKey, "");
          location.reload();
        });
      }
    });
    tell("ready", {});
  } else {
    const fragment = new URLSearchParams(location.hash.slice(1));
    const ticket = fragment.get("ticket");
    const identity = fragment.get("identity");
    if (ticket || identity)
      window.history.replaceState(
        null,
        "",
        location.pathname + location.search,
      );
    void initialize({ ticket, identity });
  }
  addEventListener("pagehide", () => {
    stopped = true;
    clearTimeout(reconnectTimer);
    clearTimeout(previewTimer);
    cancelAnimationFrame(animation);
    socket?.close();
  });
  addEventListener("visibilitychange", () => {
    if (!document.hidden) {
      void readPosition();
      if (socket?.readyState !== WebSocket.OPEN) reconnect();
    }
  });
})();
