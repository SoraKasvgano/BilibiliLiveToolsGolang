(function () {
  const roles = ["viewer", "operator", "admin"];
  const roleLevel = { viewer: 1, operator: 2, admin: 3 };

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
      { key: "dashboard", label: "主控制台", href: "/app/index.html" },
      { key: "push", label: "Push 迁移页", href: "/app/pages/push.html" },
      { key: "room", label: "Room 迁移页", href: "/app/pages/room.html" },
      { key: "material", label: "Material 迁移页", href: "/app/pages/material.html" },
      { key: "monitor", label: "Monitor 迁移页", href: "/app/pages/monitor.html" },
      { key: "legacy", label: "Legacy 兼容资源", href: "/legacy/layuiadmin/index.js" },
    ];
    container.className = "nav";
    container.innerHTML = "";
    for (const item of links) {
      const a = document.createElement("a");
      a.href = item.href;
      a.textContent = item.label;
      if (item.key === active) a.className = "active";
      container.appendChild(a);
    }
    const spacer = document.createElement("span");
    spacer.className = "spacer";
    container.appendChild(spacer);

    const label = document.createElement("label");
    label.textContent = "角色";
    label.style.margin = "0";
    label.style.fontSize = "12px";
    label.style.color = "#cbd5e1";

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
    };
    label.appendChild(roleSelect);
    container.appendChild(label);
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
      throw new Error(payload.message || `HTTP ${response.status}`);
    }
    return payload;
  }

  function showJSON(elementId, value) {
    const el = document.getElementById(elementId);
    if (!el) return;
    el.textContent = JSON.stringify(value, null, 2);
  }

  function bindAction(id, fn, outputId) {
    const el = document.getElementById(id);
    if (!el) return;
    el.onclick = async () => {
      try {
        await fn();
      } catch (error) {
        showJSON(outputId, { code: -1, message: error.message || String(error) });
      }
    };
  }

  function initPage(options) {
    const opts = options || {};
    renderNav(opts.active || "");
    applyPermissions();
  }

  window.GoverShared = {
    initPage,
    requestJSON,
    showJSON,
    bindAction,
    getRole,
    setRole,
    applyPermissions,
  };
})();
