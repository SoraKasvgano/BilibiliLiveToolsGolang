(function () {
  const roles = ["viewer", "operator", "admin"];
  const roleLevel = { viewer: 1, operator: 2, admin: 3 };
  let toastContainer = null;
  let confirmDialog = null;

  function safeRole(value) {
    const role = String(value || "").trim().toLowerCase();
    if (roles.includes(role)) return role;
    return "admin";
  }

  function getRole() {
    const params = new URLSearchParams(window.location.search);
    const queryRole = safeRole(params.get("role"));
    if (params.has("role")) {
      localStorage.setItem("gover_role", queryRole);
      return queryRole;
    }
    return safeRole(localStorage.getItem("gover_role"));
  }

  function setRole(role) {
    const next = safeRole(role);
    localStorage.setItem("gover_role", next);
    return next;
  }

  function canAccess(required, role) {
    const current = safeRole(role || getRole());
    const text = String(required || "").trim();
    if (!text) return true;
    const candidates = text
      .split(",")
      .map((item) => item.trim().toLowerCase())
      .filter((item) => item.length > 0);
    if (!candidates.length) return true;
    return candidates.some((item) => roleLevel[current] >= (roleLevel[item] || 99));
  }

  function applyPermissions() {
    const role = getRole();
    document.querySelectorAll("[data-perm]").forEach((el) => {
      const allowed = canAccess(el.getAttribute("data-perm"), role);
      if (el.tagName === "BUTTON") {
        el.disabled = !allowed;
        el.dataset.disabledByRole = allowed ? "0" : "1";
      } else {
        el.style.display = allowed ? "" : "none";
      }
    });
    const tip = document.getElementById("roleTip");
    if (tip) {
      tip.textContent = `当前角色: ${role}（viewer 只读 / operator 可操作 / admin 全量）`;
    }
  }

  function renderNav(active) {
    const container = document.getElementById("sharedNav");
    if (!container) return;
    const links = [
      { key: "home", label: "功能首页", href: "/app/pages/home.html" },
      { key: "login", label: "扫码登录", href: "/app/pages/login.html" },
      { key: "cameras", label: "摄像头管理", href: "/app/pages/cameras.html" },
      { key: "push", label: "推流配置", href: "/app/pages/push.html" },
      { key: "room", label: "直播间设置", href: "/app/pages/room.html" },
      { key: "material", label: "素材库", href: "/app/pages/material.html" },
      { key: "monitor", label: "监控通知", href: "/app/pages/monitor.html" },
      { key: "advanced", label: "高级控制台", href: "/app/index.html" },
    ];
    container.className = "nav";
    container.innerHTML = "";

    const navMain = document.createElement("div");
    navMain.className = "nav-main";

    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "nav-toggle";
    toggle.id = "navToggle";
    toggle.textContent = "☰";
    toggle.setAttribute("aria-label", "Toggle navigation");
    navMain.appendChild(toggle);

    const brand = document.createElement("span");
    brand.className = "nav-brand";
    brand.textContent = "BilibiliLiveTools Gover";
    navMain.appendChild(brand);

    const navLinks = document.createElement("div");
    navLinks.className = "nav-links";
    navLinks.id = "navLinks";
    for (const item of links) {
      const a = document.createElement("a");
      a.href = item.href;
      a.textContent = item.label;
      if (item.key === active) a.className = "active";
      navLinks.appendChild(a);
    }
    navMain.appendChild(navLinks);
    container.appendChild(navMain);

    const actions = document.createElement("div");
    actions.className = "nav-actions";
    const label = document.createElement("label");
    label.textContent = "角色";
    const roleSelect = document.createElement("select");
    roleSelect.id = "roleSwitcher";
    roleSelect.innerHTML = `
      <option value="viewer">viewer</option>
      <option value="operator">operator</option>
      <option value="admin">admin</option>
    `;
    roleSelect.value = getRole();
    roleSelect.onchange = () => {
      setRole(roleSelect.value);
      applyPermissions();
      showToast(`角色已切换为 ${roleSelect.value}`, "info");
    };
    label.appendChild(roleSelect);
    actions.appendChild(label);
    container.appendChild(actions);

    toggle.onclick = () => {
      navLinks.classList.toggle("open");
    };
  }

  async function requestJSON(path, options = {}) {
    const response = await fetch(path, options);
    const text = await response.text();
    let payload = {};
    try {
      payload = JSON.parse(text);
    } catch {
      payload = { code: -1, message: text || "Non-JSON response" };
    }
    if (!response.ok) {
      const err = new Error(payload.message || `HTTP ${response.status}`);
      err.payload = payload;
      err.status = response.status;
      throw err;
    }
    return payload;
  }

  function ensureToastContainer() {
    if (toastContainer) return toastContainer;
    toastContainer = document.createElement("div");
    toastContainer.className = "toast-container";
    toastContainer.id = "toastContainer";
    document.body.appendChild(toastContainer);
    return toastContainer;
  }

  function showToast(message, type = "info") {
    const text = String(message || "").trim();
    if (!text) return;
    const container = ensureToastContainer();
    const toast = document.createElement("div");
    const safeType = ["success", "error", "info"].includes(type) ? type : "info";
    toast.className = `toast toast-${safeType}`;
    toast.textContent = text;
    container.appendChild(toast);
    setTimeout(() => {
      toast.classList.add("toast-hide");
      setTimeout(() => {
        toast.remove();
      }, 200);
    }, 3000);
  }

  function ensureConfirmDialog() {
    if (confirmDialog) return confirmDialog;
    const overlay = document.createElement("div");
    overlay.className = "modal-overlay";
    overlay.id = "sharedConfirmModal";
    overlay.innerHTML = `
      <div class="modal-dialog" role="dialog" aria-modal="true" aria-labelledby="sharedConfirmTitle">
        <h3 id="sharedConfirmTitle" class="modal-title">确认操作</h3>
        <p id="sharedConfirmBody" class="modal-body"></p>
        <div class="modal-actions">
          <button type="button" id="sharedConfirmCancel" class="btn-ghost">取消</button>
          <button type="button" id="sharedConfirmOk" class="btn-danger">确认</button>
        </div>
      </div>
    `;
    document.body.appendChild(overlay);
    confirmDialog = overlay;
    return confirmDialog;
  }

  function showConfirm(title, message) {
    const overlay = ensureConfirmDialog();
    const titleEl = overlay.querySelector("#sharedConfirmTitle");
    const bodyEl = overlay.querySelector("#sharedConfirmBody");
    const okBtn = overlay.querySelector("#sharedConfirmOk");
    const cancelBtn = overlay.querySelector("#sharedConfirmCancel");
    titleEl.textContent = String(title || "确认操作");
    bodyEl.textContent = String(message || "确定继续吗？");
    overlay.classList.add("show");
    return new Promise((resolve) => {
      const finish = (value) => {
        overlay.classList.remove("show");
        okBtn.onclick = null;
        cancelBtn.onclick = null;
        overlay.onclick = null;
        resolve(Boolean(value));
      };
      okBtn.onclick = () => finish(true);
      cancelBtn.onclick = () => finish(false);
      overlay.onclick = (event) => {
        if (event.target === overlay) finish(false);
      };
    });
  }

  function wrapCollapsibleJSON(elementId) {
    const el = document.getElementById(elementId);
    if (!el || el.tagName !== "PRE" || el.dataset.collapsibleWrapped === "1") return;
    const block = document.createElement("div");
    block.className = "json-block";
    const head = document.createElement("div");
    head.className = "json-head";
    const title = document.createElement("span");
    title.className = "json-title";
    title.textContent = "调试详情";
    const toggle = document.createElement("button");
    toggle.type = "button";
    toggle.className = "json-toggle";
    toggle.textContent = "查看详情";
    head.appendChild(title);
    head.appendChild(toggle);

    const parent = el.parentNode;
    parent.insertBefore(block, el);
    block.appendChild(head);
    block.appendChild(el);
    el.classList.add("json-collapsed");
    el.dataset.collapsibleWrapped = "1";
    toggle.onclick = () => {
      const collapsed = el.classList.toggle("json-collapsed");
      toggle.textContent = collapsed ? "查看详情" : "收起详情";
    };
  }

  function showJSON(elementId, value) {
    const el = document.getElementById(elementId);
    if (!el) return;
    wrapCollapsibleJSON(elementId);
    el.textContent = JSON.stringify(value, null, 2);
  }

  function withButtonLoading(button, running) {
    if (!button) return;
    if (running) {
      if (button.dataset.loading === "1") return;
      button.dataset.loading = "1";
      button.dataset.originHtml = button.innerHTML;
      button.disabled = true;
      button.classList.add("btn-loading");
      button.innerHTML = `<span class="btn-spinner"></span><span>处理中...</span>`;
      return;
    }
    if (button.dataset.loading !== "1") return;
    button.classList.remove("btn-loading");
    button.innerHTML = button.dataset.originHtml || button.textContent || "";
    button.dataset.loading = "0";
    button.disabled = button.dataset.disabledByRole === "1";
  }

  function normalizeSuccessMessage(result, options, button) {
    if (options && typeof options.successMessage === "string" && options.successMessage.trim()) {
      return options.successMessage.trim();
    }
    if (button && button.dataset && button.dataset.successToast) {
      return button.dataset.successToast;
    }
    if (result && typeof result.message === "string" && result.message.trim()) {
      return result.message.trim();
    }
    return "操作成功";
  }

  function bindAction(id, fn, outputId, options) {
    const button = document.getElementById(id);
    if (!button) return;
    button.onclick = async () => {
      withButtonLoading(button, true);
      try {
        const result = await fn();
        if (!options || options.successToast !== false) {
          showToast(normalizeSuccessMessage(result, options || {}, button), "success");
        }
        return result;
      } catch (error) {
        showToast(error.message || String(error), "error");
        if (outputId) {
          showJSON(outputId, {
            code: -1,
            message: error.message || String(error),
            data: error.payload || null,
          });
        }
      } finally {
        withButtonLoading(button, false);
      }
    };
  }

  function setPanelVisible(id, visible) {
    const el = document.getElementById(id);
    if (!el) return;
    el.classList.toggle("panel-hidden", !visible);
  }

  function markField(id) {
    const el = document.getElementById(id);
    if (!el) return;
    el.classList.remove("highlight-flash");
    // Force reflow so repeated highlight works.
    void el.offsetWidth;
    el.classList.add("highlight-flash");
    setTimeout(() => el.classList.remove("highlight-flash"), 1400);
  }

  function markFields(ids) {
    (ids || []).forEach((id) => markField(id));
  }

  function statusBadge(text, type) {
    const safeType = ["success", "danger", "warning", "info", "neutral"].includes(type) ? type : "neutral";
    return `<span class="badge badge-${safeType}">${text}</span>`;
  }

  function createPoller(fn, intervalMs) {
    let timer = null;
    let running = false;
    const interval = Number(intervalMs || 5000);
    async function tick() {
      if (running) return;
      running = true;
      try {
        await fn();
      } finally {
        running = false;
      }
    }
    return {
      start() {
        if (timer) return;
        timer = setInterval(tick, interval);
        tick();
      },
      stop() {
        if (!timer) return;
        clearInterval(timer);
        timer = null;
      },
      tick,
    };
  }

  function initPage(options) {
    const opts = options || {};
    renderNav(opts.active || "");
    applyPermissions();
    document.querySelectorAll("pre[id]").forEach((pre) => {
      wrapCollapsibleJSON(pre.id);
    });
    ensureToastContainer();
  }

  window.GoverShared = {
    initPage,
    requestJSON,
    showJSON,
    bindAction,
    getRole,
    setRole,
    applyPermissions,
    showToast,
    showConfirm,
    wrapCollapsibleJSON,
    setPanelVisible,
    markField,
    markFields,
    statusBadge,
    createPoller,
  };
})();
