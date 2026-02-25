function pretty(value) {
  return JSON.stringify(value, null, 2);
}

function setBox(id, value) {
  const el = document.getElementById(id);
  if (!el) return;
  el.textContent = typeof value === "string" ? value : pretty(value);
}

const ADMIN_TOKEN_KEY = "gover_admin_token";
const ADMIN_LOGIN_PATH = "/app/pages/admin-login.html";
let pollEnabled = false;

function getAdminToken() {
  return (localStorage.getItem(ADMIN_TOKEN_KEY) || "").trim();
}

function clearAdminToken() {
  localStorage.removeItem(ADMIN_TOKEN_KEY);
  localStorage.removeItem("gover_admin_user");
}

function redirectToAdminLogin() {
  const current = `${window.location.pathname}${window.location.search}${window.location.hash}`;
  const next = encodeURIComponent(current || "/app/index.html");
  window.location.replace(`${ADMIN_LOGIN_PATH}?next=${next}`);
}

async function request(path, options = {}) {
  const opts = options || {};
  const headers = new Headers(opts.headers || {});
  const token = getAdminToken();
  if (token && !headers.has("Authorization")) {
    headers.set("Authorization", `Bearer ${token}`);
  }
  const response = await fetch(path, {
    method: opts.method || "GET",
    headers,
    body: opts.body,
  });
  const text = await response.text();
  let data;
  try {
    data = JSON.parse(text);
  } catch {
    data = { code: -1, message: text || "Non-JSON response" };
  }
  const unauthorized = response.status === 401 || Number(data.code || 0) === -401;
  if (unauthorized && !opts.noAuthRedirect) {
    clearAdminToken();
    redirectToAdminLogin();
  }
  if (!response.ok) {
    throw new Error(data.message || `HTTP ${response.status}`);
  }
  return data;
}

function apiGet(path) {
  return request(path);
}

function apiPost(path, payload) {
  return request(path, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload || {}),
  });
}

async function ensureAuthenticated() {
  const token = getAdminToken();
  if (!token) {
    redirectToAdminLogin();
    return false;
  }
  try {
    const result = await request("/api/v1/auth/status", { noAuthRedirect: true });
    if (result && result.data && result.data.authenticated) {
      return true;
    }
  } catch {
    // Ignore and treat as unauthenticated.
  }
  clearAdminToken();
  redirectToAdminLogin();
  return false;
}

function asNumber(id, fallback = 0) {
  const el = document.getElementById(id);
  if (!el) return fallback;
  const value = Number(el.value || fallback);
  if (Number.isNaN(value)) return fallback;
  return value;
}

function asString(id) {
  const el = document.getElementById(id);
  if (!el) return "";
  return (el.value || "").trim();
}

function isUsbTemplateCommand(commandText) {
  const lower = String(commandText || "").toLowerCase();
  return lower.includes("-f dshow") || lower.includes("video=\"") || lower.includes("-f v4l2") || lower.includes("/dev/video");
}

function recommendedAdvancedCommand(inputType) {
  const type = String(inputType || "").toLowerCase();
  const commonOut = "-vcodec libx264 -pix_fmt yuv420p -r 25 -g 50 -b:v 3500k -maxrate 3500k -bufsize 7000k -preset veryfast -tune zerolatency -f flv {URL}";
  if (type === "rtsp" || type === "onvif") {
    return "ffmpeg -rtsp_transport tcp -i \"rtsp://user:pass@ip/stream\" -an " + commonOut;
  }
  if (type === "mjpeg") {
    return "ffmpeg -f mjpeg -i \"http://ip:port/mjpeg\" -an " + commonOut;
  }
  if (type === "rtmp") {
    return "ffmpeg -i \"rtmp://ip/app/stream\" -an " + commonOut;
  }
  if (type === "gb28181") {
    return "ffmpeg -i \"rtsp://media-server/live/340200...\" -an " + commonOut;
  }
  if (type === "usb_camera" || type === "usb_camera_plus") {
    return "ffmpeg -f dshow -video_size 1280x720 -framerate 30 -i video=\"HD Pro Webcam C920\" -an " + commonOut;
  }
  return "ffmpeg -re -i \"input.mp4\" -an " + commonOut;
}

function suggestPushAdvancedCommand() {
  const model = asNumber("pushModel", 1);
  if (model !== 2) return;
  const inputType = asString("pushInputType");
  const commandEl = document.getElementById("pushCommand");
  if (!commandEl) return;
  const current = commandEl.value || "";
  if (!current.trim()) {
    commandEl.value = recommendedAdvancedCommand(inputType);
    return;
  }
  const networkType = inputType === "rtsp" || inputType === "mjpeg" || inputType === "onvif" || inputType === "rtmp" || inputType === "gb28181";
  if (networkType && isUsbTemplateCommand(current)) {
    commandEl.value = recommendedAdvancedCommand(inputType);
  }
}

function normalizeBitrateKbps(value) {
  const num = Number(value || 0);
  if (!Number.isFinite(num) || num <= 0) return 0;
  if (num > 120000) return 120000;
  return Math.round(num);
}

function syncPushBitrateCustomVisibility() {
  const preset = asString("pushBitratePreset");
  const wrap = document.getElementById("pushBitrateCustomWrap");
  if (!wrap) return;
  wrap.style.display = preset === "custom" ? "" : "none";
}

function applyPushBitrateValue(value) {
  const presetEl = document.getElementById("pushBitratePreset");
  const customEl = document.getElementById("pushBitrateCustom");
  if (!presetEl || !customEl) return;
  const normalized = normalizeBitrateKbps(value);
  if (!normalized) {
    presetEl.value = "auto";
  } else if (["2000", "3500", "5000", "8000", "12000"].includes(String(normalized))) {
    presetEl.value = String(normalized);
  } else {
    presetEl.value = "custom";
    customEl.value = String(normalized);
  }
  if (!customEl.value) {
    customEl.value = "4500";
  }
  syncPushBitrateCustomVisibility();
}

function currentPushBitrateKbps() {
  const preset = asString("pushBitratePreset");
  if (preset === "auto") return 0;
  if (preset === "custom") return normalizeBitrateKbps(asNumber("pushBitrateCustom", 4500));
  return normalizeBitrateKbps(preset);
}

function withError(boxId, fn) {
  return async () => {
    try {
      await fn();
    } catch (error) {
      console.error(error);
      setBox(boxId, { code: -1, message: error.message || String(error) });
    }
  };
}

// 按钮加载状态辅助函数
function setButtonLoading(buttonId, loading) {
  const btn = document.getElementById(buttonId);
  if (!btn) return;
  if (loading) {
    btn.classList.add('loading');
    btn.disabled = true;
    btn.dataset.originalText = btn.textContent;
  } else {
    btn.classList.remove('loading');
    btn.disabled = false;
    if (btn.dataset.originalText) {
      btn.textContent = btn.dataset.originalText;
    }
  }
}

// 带加载状态的错误处理包装器
function withLoadingAndError(buttonId, boxId, fn) {
  return async () => {
    setButtonLoading(buttonId, true);
    try {
      await fn();
    } catch (error) {
      console.error(error);
      setBox(boxId, { code: -1, message: error.message || String(error) });
      showToast(error.message || String(error), 'error');
    } finally {
      setButtonLoading(buttonId, false);
    }
  };
}

// 防抖函数
function debounce(func, wait) {
  let timeout;
  return function executedFunction(...args) {
    const later = () => {
      clearTimeout(timeout);
      func(...args);
    };
    clearTimeout(timeout);
    timeout = setTimeout(later, wait);
  };
}

// 节流函数
function throttle(func, limit) {
  let inThrottle;
  return function(...args) {
    if (!inThrottle) {
      func.apply(this, args);
      inThrottle = true;
      setTimeout(() => inThrottle = false, limit);
    }
  };
}

// Toast 通知函数
function showToast(message, type = 'info') {
  const container = document.getElementById('toastContainer') || createToastContainer();
  const toast = document.createElement('div');
  toast.className = `toast-item toast-${type}`;
  toast.textContent = message;
  container.appendChild(toast);

  setTimeout(() => {
    toast.style.opacity = '0';
    setTimeout(() => toast.remove(), 300);
  }, 3000);
}

function createToastContainer() {
  const container = document.createElement('div');
  container.id = 'toastContainer';
  container.style.cssText = 'position:fixed;top:20px;right:20px;z-index:9999;display:flex;flex-direction:column;gap:10px;';
  document.body.appendChild(container);
  return container;
}

// 输入验证辅助函数
function validateInput(inputId, validator, errorMessage) {
  const input = document.getElementById(inputId);
  if (!input) return true;

  const value = input.value.trim();
  const isValid = validator(value);

  if (!isValid) {
    input.classList.add('error');
    let errorEl = input.nextElementSibling;
    if (!errorEl || !errorEl.classList.contains('input-error')) {
      errorEl = document.createElement('div');
      errorEl.className = 'input-error';
      input.parentNode.insertBefore(errorEl, input.nextSibling);
    }
    errorEl.textContent = errorMessage;
  } else {
    input.classList.remove('error');
    const errorEl = input.nextElementSibling;
    if (errorEl && errorEl.classList.contains('input-error')) {
      errorEl.remove();
    }
  }

  return isValid;
}

let mosaicSources = [];
let mosaicDragIndex = -1;
let mosaicLocalPreviewRunning = false;
let latestMaintenanceStatus = null;
let latestIntegrationFeatures = null;
let latestAdvancedStats = null;
let accountStatusTicker = null;
let qrLoginPopup = null;
const ADVANCED_EXPORT_PRESETS = {
  all: "all",
  basic: "totals,hourlyEvents,hourlyDanmaku,eventTypeTop",
  ops: "totals,hourlyEvents,hourlyDanmaku,eventTypeTop,keywordStats,sessionStats,queueSummary,consumerState",
  alerts: "totals,alertTrend,eventTypeTop,deadLetter",
};

function normalizeRuntimeConfigResult(result) {
  if (!result) return result;
  const copied = JSON.parse(JSON.stringify(result));
  const data = copied.data || {};
  if (Array.isArray(data.restartFields) && data.restartFields.length > 0) {
    data.restartMessage = `以下字段修改后需要重启进程生效: ${data.restartFields.join(", ")}`;
  } else {
    data.restartMessage = "当前修改项均已热加载生效。";
  }
  copied.data = data;
  return copied;
}

async function refreshRuntimeConfig() {
  const result = await apiGet("/api/v1/config");
  const normalized = normalizeRuntimeConfigResult(result);
  const data = result && result.data ? result.data : {};
  const cfg = data.config || {};
  const configFile = data.configFile || cfg.configFile || "";
  document.getElementById("runtimeConfigFile").value = configFile;
  document.getElementById("cfgListenAddr").value = cfg.listenAddr || ":18686";
  document.getElementById("cfgAPIBase").value = cfg.apiBase || "/api/v1";
  document.getElementById("cfgAllowOrigin").value = cfg.allowOrigin || "*";
  document.getElementById("cfgLogBufferSize").value = Number(cfg.logBufferSize || 300);
  document.getElementById("cfgDataDir").value = cfg.dataDir || "";
  document.getElementById("cfgDBPath").value = cfg.dbPath || "";
  document.getElementById("cfgMediaDir").value = cfg.mediaDir || "";
  document.getElementById("cfgEnableDebugLogs").value = cfg.enableDebugLogs ? "true" : "false";
  document.getElementById("cfgAutoStartPush").value = cfg.autoStartPush ? "true" : "false";
  document.getElementById("cfgFFmpegPath").value = cfg.ffmpegPath || "";
  document.getElementById("cfgFFprobePath").value = cfg.ffprobePath || "";
  document.getElementById("cfgBiliAppKey").value = cfg.biliAppKey || "";
  document.getElementById("cfgBiliAppSecret").value = cfg.biliAppSecret || "";
  document.getElementById("cfgBiliPlatform").value = cfg.biliPlatform || "";
  document.getElementById("cfgBiliVersion").value = cfg.biliVersion || "";
  document.getElementById("cfgBiliBuild").value = cfg.biliBuild || "";
  setBox("configBox", normalized);
}

function buildRuntimeConfigPayload() {
  return {
    listenAddr: asString("cfgListenAddr"),
    apiBase: asString("cfgAPIBase"),
    allowOrigin: asString("cfgAllowOrigin"),
    logBufferSize: asNumber("cfgLogBufferSize", 300),
    dataDir: asString("cfgDataDir"),
    dbPath: asString("cfgDBPath"),
    mediaDir: asString("cfgMediaDir"),
    enableDebugLogs: asString("cfgEnableDebugLogs") === "true",
    autoStartPush: asString("cfgAutoStartPush") === "true",
    ffmpegPath: asString("cfgFFmpegPath"),
    ffprobePath: asString("cfgFFprobePath"),
    biliAppKey: asString("cfgBiliAppKey"),
    biliAppSecret: asString("cfgBiliAppSecret"),
    biliPlatform: asString("cfgBiliPlatform"),
    biliVersion: asString("cfgBiliVersion"),
    biliBuild: asString("cfgBiliBuild"),
  };
}

function normalizeMosaicSource(item = {}) {
  const url = String(item.url || "").trim();
  if (!url) return null;
  return {
    url,
    title: String(item.title || "").trim(),
    primary: Boolean(item.primary),
    sourceType: String(item.sourceType || "").trim() || "manual",
    materialId: Number(item.materialId || 0) || 0,
  };
}

function ensureMosaicPrimary(items) {
  const copied = items.map((item) => ({ ...item, primary: Boolean(item.primary) }));
  const primaryIndex = copied.findIndex((item) => item.primary);
  if (copied.length > 0 && primaryIndex < 0) {
    copied[0].primary = true;
  } else if (primaryIndex > 0) {
    const first = copied[0];
    copied[0] = copied[primaryIndex];
    copied[primaryIndex] = first;
  }
  for (let i = 1; i < copied.length; i++) {
    copied[i].primary = false;
  }
  return copied;
}

function setMosaicSources(items) {
  const list = [];
  for (const raw of items || []) {
    const normalized = normalizeMosaicSource(raw);
    if (!normalized) continue;
    if (list.some((item) => item.url === normalized.url)) continue;
    list.push(normalized);
    if (list.length >= 9) break;
  }
  mosaicSources = ensureMosaicPrimary(list);
  syncMosaicTextFromSources();
  renderMosaicSources();
}

function resolveCameraSourceURL(item = {}) {
  const type = String(item.sourceType || "").toLowerCase().trim();
  if (type === "rtsp" || type === "onvif") return String(item.rtspUrl || "").trim();
  if (type === "mjpeg") return String(item.mjpegUrl || "").trim();
  if (type === "rtmp") return String(item.rtmpUrl || "").trim();
  if (type === "gb28181") return String(item.gbPullUrl || "").trim();
  return "";
}

function cameraSourceTypeLabel(type) {
  const normalized = String(type || "").toLowerCase().trim();
  if (normalized === "rtsp") return "RTSP";
  if (normalized === "mjpeg") return "MJPEG";
  if (normalized === "rtmp") return "RTMP";
  if (normalized === "gb28181") return "GB28181";
  if (normalized === "onvif") return "ONVIF";
  if (normalized === "usb") return "USB";
  return normalized || "unknown";
}

async function loadMosaicCameraOptions() {
  const select = document.getElementById("mosaicCameraSelect");
  if (!select) return;
  const params = new URLSearchParams();
  params.set("page", "1");
  params.set("limit", "200");
  const type = asString("mosaicCameraType");
  if (type) params.set("sourceType", type);
  const result = await apiGet(`/api/v1/cameras?${params.toString()}`);
  select.innerHTML = "";
  const rows = result && result.data && Array.isArray(result.data.data) ? result.data.data : [];
  for (const row of rows) {
    const url = resolveCameraSourceURL(row);
    if (!url) continue;
    const option = document.createElement("option");
    option.value = url;
    option.dataset.cameraId = String(row.id || 0);
    option.dataset.title = row.name || "";
    option.dataset.sourceType = row.sourceType || "";
    option.textContent = `${row.id || 0} - ${row.name || "camera"} [${cameraSourceTypeLabel(row.sourceType)}]`;
    select.appendChild(option);
  }
  setBox("mosaicPreviewBox", result);
}

function syncMosaicTextFromSources() {
  const area = document.getElementById("pushMultiUrls");
  if (!area) return;
  area.value = mosaicSources.map((item) => item.url).join("\n");
}

function renderMosaicSources() {
  const board = document.getElementById("mosaicSourceBoard");
  if (!board) return;
  if (!mosaicSources.length) {
    board.innerHTML = "<div>暂无拼屏源，请先添加 URL，或从素材库/摄像头库选择。</div>";
    return;
  }
  board.innerHTML = "";
  mosaicSources.forEach((source, index) => {
    const item = document.createElement("div");
    item.className = "mosaic-item";
    item.draggable = true;
    item.dataset.index = String(index);
    item.dataset.dragging = "0";
    item.innerHTML = `
      <div class="mosaic-handle" title="拖拽排序">☰</div>
      <div>
        <div class="row two">
          <label>源地址
            <input data-field="url" value="${source.url.replace(/"/g, "&quot;")}" />
          </label>
          <label>标题叠加
            <input data-field="title" value="${(source.title || "").replace(/"/g, "&quot;")}" placeholder="例如：主机位" />
          </label>
        </div>
        <div class="row two">
          <label>来源类型
            <input data-field="sourceType" value="${source.sourceType.replace(/"/g, "&quot;")}" />
          </label>
          <label>素材ID(可选)
            <input data-field="materialId" type="number" value="${source.materialId || 0}" />
          </label>
        </div>
        <div class="actions">
          <button data-action="primary">${source.primary ? "主画面" : "设为主画面"}</button>
          <button data-action="remove" class="danger">删除</button>
        </div>
      </div>
    `;
    item.addEventListener("dragstart", () => {
      mosaicDragIndex = index;
      item.dataset.dragging = "1";
    });
    item.addEventListener("dragend", () => {
      mosaicDragIndex = -1;
      item.dataset.dragging = "0";
    });
    item.addEventListener("dragover", (event) => {
      event.preventDefault();
    });
    item.addEventListener("drop", (event) => {
      event.preventDefault();
      const targetIndex = Number(item.dataset.index || "-1");
      if (mosaicDragIndex < 0 || targetIndex < 0 || mosaicDragIndex === targetIndex) return;
      const next = [...mosaicSources];
      const [moved] = next.splice(mosaicDragIndex, 1);
      next.splice(targetIndex, 0, moved);
      setMosaicSources(next);
    });
    item.querySelectorAll("input[data-field]").forEach((input) => {
      input.addEventListener("change", () => {
        const field = input.getAttribute("data-field");
        if (!field) return;
        const next = [...mosaicSources];
        if (!next[index]) return;
        if (field === "materialId") {
          next[index][field] = Number(input.value || 0) || 0;
        } else {
          next[index][field] = (input.value || "").trim();
        }
        setMosaicSources(next);
      });
    });
    item.querySelector('[data-action="primary"]').addEventListener("click", () => {
      const next = [...mosaicSources];
      if (index > 0) {
        const [target] = next.splice(index, 1);
        next.unshift(target);
      }
      next.forEach((entry, idx) => {
        entry.primary = idx === 0;
      });
      setMosaicSources(next);
    });
    item.querySelector('[data-action="remove"]').addEventListener("click", () => {
      const next = mosaicSources.filter((_, idx) => idx !== index);
      setMosaicSources(next);
    });
    board.appendChild(item);
  });
}

function getMosaicPayload() {
  const normalized = ensureMosaicPrimary(
    mosaicSources
      .map((item) => normalizeMosaicSource(item))
      .filter((item) => !!item)
  );
  return {
    urls: normalized.map((item) => item.url),
    meta: normalized,
  };
}

async function refreshAccount() {
  const result = await apiGet("/api/v1/account/status");
  applyAccountStatusView(result);
  setBox("accountBox", result);
}

function showAccountQrImage(src) {
  const img = document.getElementById("accountQrImage");
  if (!img) return;
  if (!src) {
    img.removeAttribute("src");
    img.style.display = "none";
    return;
  }
  img.src = src;
  img.style.display = "block";
}

function setAccountQrInfo(htmlText) {
  const info = document.getElementById("accountQrInfo");
  if (!info) return;
  info.innerHTML = htmlText || "";
}

function startAccountStatusPolling() {
  if (accountStatusTicker) return;
  accountStatusTicker = setInterval(async () => {
    try {
      const result = await apiGet("/api/v1/account/status");
      applyAccountStatusView(result);
      setBox("accountBox", result);
    } catch (error) {
      setAccountQrInfo(`<span style="color:#b91c1c;">状态轮询失败：${error.message || String(error)}</span>`);
    }
  }, 1000);
}

function stopAccountStatusPolling() {
  if (!accountStatusTicker) return;
  clearInterval(accountStatusTicker);
  accountStatusTicker = null;
}

function openQrLoginPopup() {
  const popupUrl = "/app/pages/login.html?autoclose=1";
  const width = 520;
  const height = 760;
  const left = Math.max(0, Math.floor((window.screen.width - width) / 2));
  const top = Math.max(0, Math.floor((window.screen.height - height) / 2));
  if (qrLoginPopup && !qrLoginPopup.closed) {
    qrLoginPopup.focus();
    return;
  }
  qrLoginPopup = window.open(
    popupUrl,
    "gover_qr_login",
    `width=${width},height=${height},left=${left},top=${top},resizable=yes,scrollbars=yes`
  );
  if (!qrLoginPopup) {
    throw new Error("浏览器拦截了弹窗，请允许当前站点弹窗后重试");
  }
}

function applyAccountStatusView(result) {
  const data = result && result.data ? result.data : {};
  const status = Number(data.status || 0);
  if (status === 3) {
    showAccountQrImage("");
    setAccountQrInfo("【<span style=\"color:green;\">登录成功</span>】");
    stopAccountStatusPolling();
    return;
  }
  if (status === 1) {
    showAccountQrImage("");
    setAccountQrInfo("<span style=\"color:#b91c1c;\">点击“生成二维码登录”进行扫码登录</span>");
    stopAccountStatusPolling();
    return;
  }
  if (status === 2 && data.qrCodeStatus) {
    const qr = data.qrCodeStatus;
    if (qr.qrCode) {
      showAccountQrImage(qr.qrCode);
    }
    if (Number(qr.qrCodeEffectiveTime || 0) <= 0) {
      setAccountQrInfo("【<span style=\"color:#b91c1c;\">二维码已失效</span>】请重新生成二维码");
    } else if (qr.isScaned) {
      setAccountQrInfo(`【<span style="color:green;">已扫码</span>】二维码剩余有效期 <span style="color:green;">${Number(qr.qrCodeEffectiveTime || 0)}</span> 秒`);
    } else {
      setAccountQrInfo(`【<span style="color:#b91c1c;">未扫码</span>】二维码剩余有效期 <span style="color:green;">${Number(qr.qrCodeEffectiveTime || 0)}</span> 秒`);
    }
    startAccountStatusPolling();
    return;
  }
  if (status === 4) {
    showAccountQrImage("");
    setAccountQrInfo("加载中，请稍后...");
    startAccountStatusPolling();
    return;
  }
  if (data.qrCodeStatus && data.qrCodeStatus.qrCode) {
    showAccountQrImage(data.qrCodeStatus.qrCode);
  }
  if (result && result.message) {
    setAccountQrInfo(`<span style="color:#b91c1c;">${result.message}</span>`);
  } else {
    setAccountQrInfo("<span style=\"color:#b91c1c;\">未知登录状态</span>");
  }
}

async function refreshRoom() {
  const result = await apiGet("/api/v1/room");
  setBox("roomBox", result);
  if (!result || !result.data) return;
  document.getElementById("roomId").value = result.data.roomId || "";
  document.getElementById("roomAreaId").value = result.data.areaId || "";
  document.getElementById("roomName").value = result.data.roomName || "";
  document.getElementById("roomContent").value = result.data.content || "";
}

async function refreshPush() {
  const result = await apiGet("/api/v1/push/setting");
  setBox("pushBox", result);
  if (!result || !result.data) return;
  const data = result.data;
  document.getElementById("pushModel").value = String(data.model ?? 1);
  document.getElementById("pushInputType").value = data.inputType || "video";
  document.getElementById("pushResolution").value = data.outputResolution || "1280x720";
  document.getElementById("pushQuality").value = String(data.outputQuality ?? 2);
  applyPushBitrateValue(data.outputBitrateKbps || 0);
  document.getElementById("pushRtspUrl").value = data.rtspUrl || "";
  document.getElementById("pushMjpegUrl").value = data.mjpegUrl || "";
  document.getElementById("pushRtmpUrl").value = data.rtmpUrl || "";
  document.getElementById("pushGbPullUrl").value = data.gbPullUrl || "";
  document.getElementById("pushMultiEnabled").value = data.multiInputEnabled ? "true" : "false";
  document.getElementById("pushMultiLayout").value = data.multiInputLayout || "2x2";
  const loadedMeta = Array.isArray(data.multiInputMeta) ? data.multiInputMeta : [];
  const loadedUrls = Array.isArray(data.multiInputUrls) ? data.multiInputUrls : [];
  if (loadedMeta.length) {
    setMosaicSources(loadedMeta);
  } else {
    setMosaicSources(loadedUrls.map((url, idx) => ({ url, primary: idx === 0 })));
  }
  document.getElementById("pushCameraName").value = data.inputDeviceName || "";
  document.getElementById("pushCameraResolution").value = data.inputDeviceResolution || "1280x720";
  document.getElementById("pushCommand").value = data.ffmpegCommand || "";
  document.getElementById("pushAutoRetry").value = data.isAutoRetry ? "true" : "false";
  document.getElementById("pushRetryInterval").value = data.retryInterval || 30;
  document.getElementById("ptzEndpoint").value = data.onvifEndpoint || "";
  document.getElementById("ptzUsername").value = data.onvifUsername || "";
  document.getElementById("ptzPassword").value = data.onvifPassword || "";
  document.getElementById("ptzProfileToken").value = data.onvifProfileToken || "";
  suggestPushAdvancedCommand();
}

async function refreshMaterials() {
  const result = await apiGet("/api/v1/materials?page=1&limit=20");
  setBox("materialBox", result);
}

async function refreshApiKeys() {
  const result = await apiGet("/api/v1/integration/api-keys?limit=200&offset=0");
  setBox("integrationBox", result);
}

async function refreshLogs() {
  const result = await apiGet("/api/v1/logs/ffmpeg");
  setBox("logBox", result);
}

async function refreshBiliAlertSetting() {
  const result = await apiGet("/api/v1/integration/bilibili/alert-setting");
  if (result && result.data) {
    document.getElementById("biliAlertWindow").value = result.data.windowMinutes ?? 10;
    document.getElementById("biliAlertThreshold").value = result.data.threshold ?? 8;
    document.getElementById("biliAlertCooldown").value = result.data.cooldownMinutes ?? 15;
    document.getElementById("biliAlertEventType").value = result.data.webhookEvent || "bilibili.api.alert";
    document.getElementById("biliAlertEnabled").value = result.data.enabled ? "true" : "false";
  }
  setBox("liveDataBox", result);
}

async function refreshDanmakuConsumerSetting() {
  const result = await apiGet("/api/v1/integration/danmaku/consumer/setting");
  const data = result && result.data ? result.data : {};
  const setting = data.setting || {};
  document.getElementById("consumerEnabled").value = setting.enabled ? "true" : "false";
  document.getElementById("consumerProvider").value = setting.provider || "http_polling";
  document.getElementById("consumerEndpoint").value = setting.endpoint || "";
  document.getElementById("consumerAuthToken").value = setting.authToken || "";
  document.getElementById("consumerConfigJson").value = setting.configJson || "{}";
  document.getElementById("consumerPollInterval").value = Number(setting.pollIntervalSec || 3);
  document.getElementById("consumerBatchSize").value = Number(setting.batchSize || 20);
  document.getElementById("consumerRoomIdFilter").value = Number(setting.roomId || 0);
  setBox("liveDataBox", result);
}

async function refreshDanmakuConsumerStatus() {
  const result = await apiGet("/api/v1/integration/danmaku/consumer/status");
  setBox("liveDataBox", result);
}

async function refreshIntegrationTaskSummary() {
  const result = await apiGet("/api/v1/integration/tasks/summary");
  setBox("liveDataBox", result);
}

async function refreshIntegrationQueueSetting() {
  const result = await apiGet("/api/v1/integration/tasks/queue-setting");
  const data = result && result.data ? result.data : {};
  document.getElementById("queueWebhookGapMs").value = Number(data.webhookRateGapMs || 300);
  document.getElementById("queueBotGapMs").value = Number(data.botRateGapMs || 300);
  document.getElementById("queueMaxWorkers").value = Number(data.maxWorkers || 3);
  document.getElementById("queueLeaseIntervalMs").value = Number(data.leaseIntervalMs || 500);
  setBox("liveDataBox", result);
}

async function refreshIntegrationFeatures() {
  const result = await apiGet("/api/v1/integration/features");
  const data = result && result.data ? result.data : {};
  const setting = data.setting || {};
  latestIntegrationFeatures = setting;
  setSelectBool("featureSimpleMode", setting.simpleMode);
  setSelectBool("featureDanmakuConsumer", setting.enableDanmakuConsumer);
  setSelectBool("featureWebhook", setting.enableWebhook);
  setSelectBool("featureBot", setting.enableBot);
  setSelectBool("featureAdvancedStats", setting.enableAdvancedStats);
  setSelectBool("featureTaskQueue", setting.enableTaskQueue);
  applyFeatureFlags(data);
  setBox("liveDataBox", result);
}

async function refreshRuntimeMemory() {
  const result = await apiGet("/api/v1/integration/runtime/memory");
  setBox("liveDataBox", result);
}

function drawLineChart(canvasId, rows, options = {}) {
  const canvas = document.getElementById(canvasId);
  if (!canvas || !canvas.getContext) return;
  const ctx = canvas.getContext("2d");
  const width = canvas.width;
  const height = canvas.height;
  ctx.clearRect(0, 0, width, height);
  ctx.fillStyle = "#f8fafc";
  ctx.fillRect(0, 0, width, height);

  const title = options.title || "趋势图";
  ctx.fillStyle = "#0f172a";
  ctx.font = "bold 14px sans-serif";
  ctx.fillText(title, 12, 18);

  if (!Array.isArray(rows) || rows.length === 0) {
    ctx.fillStyle = "#475569";
    ctx.font = "13px sans-serif";
    ctx.fillText("暂无数据", 14, 44);
    return;
  }

  const points = rows.map((item) => Number(item.count || 0));
  const max = Math.max(...points, 1);
  const left = 48;
  const top = 28;
  const plotWidth = width - left - 20;
  const plotHeight = height - top - 34;

  ctx.strokeStyle = "#94a3b8";
  ctx.lineWidth = 1;
  ctx.beginPath();
  ctx.moveTo(left, top);
  ctx.lineTo(left, top + plotHeight);
  ctx.lineTo(left + plotWidth, top + plotHeight);
  ctx.stroke();

  ctx.strokeStyle = options.color || "#0ea5e9";
  ctx.lineWidth = 2;
  ctx.beginPath();
  rows.forEach((row, index) => {
    const x = left + (plotWidth * index) / Math.max(rows.length - 1, 1);
    const value = Number(row.count || 0);
    const y = top + plotHeight - (value / max) * plotHeight;
    if (index === 0) ctx.moveTo(x, y);
    else ctx.lineTo(x, y);
  });
  ctx.stroke();

  ctx.fillStyle = "#334155";
  ctx.font = "12px sans-serif";
  ctx.fillText(`Max: ${max}`, left + 6, top + 14);
}

function drawBarChart(canvasId, rows, options = {}) {
  const canvas = document.getElementById(canvasId);
  if (!canvas || !canvas.getContext) return;
  const ctx = canvas.getContext("2d");
  const width = canvas.width;
  const height = canvas.height;
  ctx.clearRect(0, 0, width, height);
  ctx.fillStyle = "#f8fafc";
  ctx.fillRect(0, 0, width, height);

  const title = options.title || "柱状图";
  ctx.fillStyle = "#0f172a";
  ctx.font = "bold 14px sans-serif";
  ctx.fillText(title, 12, 18);

  if (!Array.isArray(rows) || rows.length === 0) {
    ctx.fillStyle = "#475569";
    ctx.font = "13px sans-serif";
    ctx.fillText("暂无数据", 14, 44);
    return;
  }

  const list = rows.slice(0, options.limit || 10);
  const values = list.map((row) => Number(row.count || 0));
  const max = Math.max(...values, 1);
  const left = 48;
  const top = 28;
  const plotWidth = width - left - 20;
  const plotHeight = height - top - 34;
  const barWidth = plotWidth / Math.max(list.length, 1);

  ctx.strokeStyle = "#94a3b8";
  ctx.beginPath();
  ctx.moveTo(left, top + plotHeight);
  ctx.lineTo(left + plotWidth, top + plotHeight);
  ctx.stroke();

  ctx.fillStyle = options.color || "#38bdf8";
  list.forEach((row, index) => {
    const value = Number(row.count || 0);
    const h = (value / max) * (plotHeight - 14);
    const x = left + index * barWidth + 4;
    const y = top + plotHeight - h;
    ctx.fillRect(x, y, Math.max(8, barWidth - 8), h);
    ctx.fillStyle = "#334155";
    ctx.font = "11px sans-serif";
    const label = String(row.eventType || row.bucket || row.hour || row.day || "").slice(0, 12);
    ctx.fillText(label, x, top + plotHeight + 12);
    ctx.fillStyle = options.color || "#38bdf8";
  });
}

function drawAdvancedStatsCharts(data) {
  const hourlyEvents = data && Array.isArray(data.hourlyEvents) ? data.hourlyEvents : [];
  const eventTop = data && Array.isArray(data.eventTypeTop) ? data.eventTypeTop : [];
  const alertTrend = data && Array.isArray(data.alertTrend) ? data.alertTrend : [];
  const granularity = data && data.granularity === "day" ? "day" : "hour";
  drawLineChart("advancedStatsChart", hourlyEvents, { title: `事件趋势（${granularity}）`, color: "#0ea5e9" });
  drawBarChart("eventTopChart", eventTop, { title: "事件TOP（eventTypeTop）", color: "#22c55e", limit: 12 });
  drawLineChart("alertTrendChart", alertTrend, { title: `告警趋势（${granularity}）`, color: "#ef4444" });
}

function applyAdvancedExportPreset(preset, forceOverwrite = false) {
  const key = (preset || "").trim().toLowerCase();
  const fieldsInput = document.getElementById("advancedExportFields");
  if (!fieldsInput) return;
  const next = ADVANCED_EXPORT_PRESETS[key] || "all";
  if (forceOverwrite || !fieldsInput.value || fieldsInput.value.trim() === "") {
    fieldsInput.value = next;
    return;
  }
  if (fieldsInput.value.trim() === "all" && key !== "all") {
    fieldsInput.value = next;
  }
}

async function downloadAdvancedStats(format) {
  const hours = asNumber("advancedStatsHours", 24);
  const granularity = encodeURIComponent(asString("advancedStatsGranularity") || "hour");
  const fields = encodeURIComponent(asString("advancedExportFields"));
  const maxRows = asNumber("advancedExportMaxRows", 300);
  const headers = {};
  const token = getAdminToken();
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }
  const response = await fetch(
    `/api/v1/live/stats/advanced/export?hours=${hours}&granularity=${granularity}&format=${encodeURIComponent(format)}&fields=${fields}&maxRows=${maxRows}`,
    { headers }
  );
  if (!response.ok) {
    const text = await response.text();
    throw new Error(text || `HTTP ${response.status}`);
  }
  const blob = await response.blob();
  const suffix = format === "json" ? "json" : "csv";
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = `advanced_stats_${new Date().toISOString().replace(/[:.]/g, "-")}.${suffix}`;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

async function refreshMaintenanceSetting() {
  const result = await apiGet("/api/v1/maintenance/setting");
  if (result && result.data) {
    document.getElementById("maintenanceEnabled").value = result.data.enabled ? "true" : "false";
    document.getElementById("maintenanceDays").value = result.data.retentionDays ?? 7;
    document.getElementById("maintenanceAutoVacuum").value = result.data.autoVacuum ? "true" : "false";
    document.getElementById("maintenanceNowDays").value = result.data.retentionDays ?? 7;
  }
  setBox("maintenanceBox", result);
}

async function refreshMaintenanceStatus() {
  const limit = asNumber("maintenanceHistoryLimit", 20);
  const type = encodeURIComponent(asString("maintenanceHistoryType"));
  const state = encodeURIComponent(asString("maintenanceHistoryState"));
  const result = await apiGet(`/api/v1/maintenance/status?historyLimit=${limit}&type=${type}&status=${state}`);
  latestMaintenanceStatus = result && result.data ? result.data : null;
  setBox("maintenanceBox", result);
}

function parseJSONInput(raw, fallback = {}) {
  const text = (raw || "").trim();
  if (!text) return fallback;
  return JSON.parse(text);
}

function parseLines(raw) {
  const text = (raw || "").trim();
  if (!text) return [];
  return text
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter((item) => item.length > 0);
}

function boolValue(id, fallback = false) {
  const value = asString(id);
  if (value === "true") return true;
  if (value === "false") return false;
  return fallback;
}

function setSelectBool(id, value) {
  const el = document.getElementById(id);
  if (!el) return;
  el.value = value ? "true" : "false";
}

function setButtonEnabled(id, enabled) {
  const el = document.getElementById(id);
  if (!el) return;
  el.disabled = !enabled;
}

function applyFeatureFlags(data = {}) {
  const setting = data.setting || latestIntegrationFeatures || {};
  const effective = data.effective || {};
  const simpleMode = Boolean(setting.simpleMode);
  const pickEnabled = (name, fallback) => {
    if (Object.prototype.hasOwnProperty.call(effective, name)) {
      return Boolean(effective[name]);
    }
    return Boolean(fallback);
  };
  const queueEnabled = pickEnabled("task_queue", !simpleMode && setting.enableTaskQueue);
  const webhookEnabled = pickEnabled("webhook", !simpleMode && setting.enableWebhook);
  const botEnabled = pickEnabled("bot", !simpleMode && setting.enableBot);
  const consumerEnabled = pickEnabled("danmaku_consumer", !simpleMode && setting.enableDanmakuConsumer);
  const advancedStatsEnabled = pickEnabled("advanced_stats", !simpleMode && setting.enableAdvancedStats);

  setButtonEnabled("btnRetryIntegrationTask", queueEnabled);
  setButtonEnabled("btnRetryIntegrationTaskBatch", queueEnabled);
  setButtonEnabled("btnCancelIntegrationTask", queueEnabled);
  setButtonEnabled("btnUpdateIntegrationTaskPriority", queueEnabled);
  setButtonEnabled("btnLoadQueueSetting", queueEnabled);
  setButtonEnabled("btnSaveQueueSetting", queueEnabled);
  setButtonEnabled("btnTestWebhook", queueEnabled && webhookEnabled);
  setButtonEnabled("btnNotifyWebhook", queueEnabled && webhookEnabled);
  setButtonEnabled("btnBotCommand", queueEnabled && botEnabled);
  setButtonEnabled("btnConsumerPollOnce", consumerEnabled);
  setButtonEnabled("btnLiveAdvancedStats", advancedStatsEnabled);
  setButtonEnabled("btnCheckBiliAlert", queueEnabled && webhookEnabled);
}

function buildMosaicPreview() {
  const enabled = asString("pushMultiEnabled") === "true";
  const layout = asString("pushMultiLayout") || "2x2";
  const sources = (mosaicSources || []).map((item) => item.url).filter((item) => !!item);
  if (layout.toLowerCase() === "focus" || layout.toLowerCase().startsWith("focus-")) {
    return {
      enabled,
      layout,
      mode: "focus",
      inputs: sources.length,
      primary: sources[0] || "",
      secondary: sources.slice(1),
    };
  }
  const parts = layout.toLowerCase().split("x");
  let cols = Number(parts[0] || 0);
  let rows = Number(parts[1] || 0);
  if (!Number.isFinite(cols) || cols <= 0) cols = 2;
  if (!Number.isFinite(rows) || rows <= 0) rows = 2;
  if (cols * rows < sources.length) {
    rows = Math.ceil(sources.length / cols);
  }
  return {
    enabled,
    layout: `${cols}x${rows}`,
    inputs: sources.length,
    slots: cols * rows,
    matrix: Array.from({ length: rows }, (_, rowIndex) =>
      Array.from({ length: cols }, (_, colIndex) => {
        const idx = rowIndex * cols + colIndex;
        return sources[idx] || "(empty)";
      })
    ),
  };
}

function buildPushPayload() {
  const mosaic = getMosaicPayload();
  return {
    model: asNumber("pushModel", 1),
    inputType: asString("pushInputType"),
    outputResolution: asString("pushResolution"),
    outputQuality: asNumber("pushQuality", 2),
    outputBitrateKbps: currentPushBitrateKbps(),
    rtspUrl: asString("pushRtspUrl"),
    mjpegUrl: asString("pushMjpegUrl"),
    rtmpUrl: asString("pushRtmpUrl"),
    gbPullUrl: asString("pushGbPullUrl"),
    multiInputEnabled: asString("pushMultiEnabled") === "true",
    multiInputLayout: asString("pushMultiLayout"),
    multiInputUrls: mosaic.urls,
    multiInputMeta: mosaic.meta,
    inputDeviceName: asString("pushCameraName"),
    inputDeviceResolution: asString("pushCameraResolution"),
    ffmpegCommand: document.getElementById("pushCommand").value || "",
    isAutoRetry: asString("pushAutoRetry") === "true",
    retryInterval: asNumber("pushRetryInterval", 30),
    onvifEndpoint: asString("ptzEndpoint"),
    onvifUsername: asString("ptzUsername"),
    onvifPassword: document.getElementById("ptzPassword").value || "",
    onvifProfileToken: asString("ptzProfileToken"),
  };
}

function setMosaicPreviewInfo(message) {
  const el = document.getElementById("mosaicLocalPreviewInfo");
  if (!el) return;
  el.textContent = message || "";
}

function stopMosaicLocalPreview() {
  const img = document.getElementById("mosaicLocalPreviewImage");
  if (!img) return;
  mosaicLocalPreviewRunning = false;
  img.style.display = "none";
  img.src = "";
  setMosaicPreviewInfo("本地预览已关闭。");
}

async function startMosaicLocalPreview() {
  const payload = buildPushPayload();
  const saveResult = await apiPost("/api/v1/push/setting", payload);
  setBox("pushBox", saveResult);
  if (saveResult && Number(saveResult.code) < 0) {
    throw new Error(saveResult.message || "保存推流配置失败，无法开启本地预览");
  }

  const width = Math.max(240, Math.min(1920, asNumber("mosaicLocalPreviewWidth", 960)));
  const fps = Math.max(1, Math.min(20, asNumber("mosaicLocalPreviewFps", 10)));
  const img = document.getElementById("mosaicLocalPreviewImage");
  if (!img) return;
  mosaicLocalPreviewRunning = true;
  img.style.display = "block";
  img.onerror = () => {
    if (!mosaicLocalPreviewRunning) return;
    setMosaicPreviewInfo("本地预览连接失败，请检查拼屏源地址/摄像头状态后重试。");
  };
  img.onload = () => {
    if (!mosaicLocalPreviewRunning) return;
    setMosaicPreviewInfo("本地预览进行中（已降帧降宽，便于调试且节省资源）");
  };
  img.src = `/api/v1/push/preview/mjpeg?width=${width}&fps=${fps}&_ts=${Date.now()}`;
  setMosaicPreviewInfo("正在建立本地预览连接...");
}

function bindActions() {
  document.getElementById("btnLoadRuntimeConfig").onclick = withError("configBox", refreshRuntimeConfig);
  document.getElementById("btnSaveRuntimeConfig").onclick = withError("configBox", async () => {
    const payload = buildRuntimeConfigPayload();
    const result = await apiPost("/api/v1/config", payload);
    await refreshRuntimeConfig();
    setBox("configBox", normalizeRuntimeConfigResult(result));
  });
  document.getElementById("btnReloadRuntimeConfig").onclick = withError("configBox", async () => {
    const result = await apiPost("/api/v1/config/reload", {});
    await refreshRuntimeConfig();
    setBox("configBox", normalizeRuntimeConfigResult(result));
  });

  document.getElementById("btnAccountStatus").onclick = withError("accountBox", refreshAccount);
  document.getElementById("btnQrStart").onclick = withError("accountBox", async () => {
    const result = await apiPost("/api/v1/account/login/qrcode/start", {});
    const qr = result && result.data ? result.data : null;
    if (qr && qr.qrCode) {
      showAccountQrImage(qr.qrCode);
      if (qr.isScaned) {
        setAccountQrInfo(`【<span style="color:green;">已扫码</span>】二维码剩余有效期 <span style="color:green;">${Number(qr.qrCodeEffectiveTime || 0)}</span> 秒`);
      } else {
        setAccountQrInfo(`【<span style="color:#b91c1c;">未扫码</span>】二维码剩余有效期 <span style="color:green;">${Number(qr.qrCodeEffectiveTime || 0)}</span> 秒`);
      }
      startAccountStatusPolling();
    }
    setBox("accountBox", result);
  });
  document.getElementById("btnQrPopup").onclick = withError("accountBox", async () => {
    openQrLoginPopup();
    setAccountQrInfo("扫码弹窗已打开，请在弹窗内完成登录。");
  });
  document.getElementById("btnNeedRefresh").onclick = withError("accountBox", async () => {
    const result = await apiGet("/api/v1/account/cookie/need-refresh");
    setBox("accountBox", result);
  });
  document.getElementById("btnRefreshCookie").onclick = withError("accountBox", async () => {
    const result = await apiPost("/api/v1/account/cookie/refresh", {});
    setBox("accountBox", result);
    await refreshAccount();
  });
  document.getElementById("btnSaveCookie").onclick = withError("accountBox", async () => {
    const content = document.getElementById("cookieContent").value;
    const result = await apiPost("/api/v1/account/cookie", { content });
    setBox("accountBox", result);
    await refreshAccount();
  });
  document.getElementById("btnLogout").onclick = withError("accountBox", async () => {
    const result = await apiPost("/api/v1/account/logout", {});
    stopAccountStatusPolling();
    showAccountQrImage("");
    setAccountQrInfo("<span style=\"color:#b91c1c;\">已登出，点击“生成二维码登录”重新登录</span>");
    setBox("accountBox", result);
    await refreshAccount();
  });
  document.getElementById("btnSaveStreamUrl").onclick = withError("accountBox", async () => {
    const url = document.getElementById("manualStreamUrl").value;
    const result = await apiPost("/api/v1/account/stream-url", { url });
    setBox("accountBox", result);
  });

  document.getElementById("btnLoadRoom").onclick = withError("roomBox", refreshRoom);
  document.getElementById("btnLoadAreas").onclick = withError("roomBox", async () => {
    const result = await apiGet("/api/v1/room/areas");
    setBox("roomBox", result);
  });
  document.getElementById("btnSaveRoom").onclick = withError("roomBox", async () => {
    const payload = {
      roomId: asNumber("roomId"),
      areaId: asNumber("roomAreaId"),
      roomName: asString("roomName"),
    };
    const result = await apiPost("/api/v1/room/update", payload);
    setBox("roomBox", result);
  });
  document.getElementById("btnSaveAnnouncement").onclick = withError("roomBox", async () => {
    const payload = {
      roomId: asNumber("roomId"),
      content: document.getElementById("roomContent").value || "",
    };
    const result = await apiPost("/api/v1/room/announcement", payload);
    setBox("roomBox", result);
  });

  document.getElementById("btnLoadPush").onclick = withError("pushBox", refreshPush);
  document.getElementById("btnSavePush").onclick = withError("pushBox", async () => {
    const payload = buildPushPayload();
    const result = await apiPost("/api/v1/push/setting", payload);
    setBox("pushBox", result);
  });
  document.getElementById("btnStartPush").onclick = withError("pushBox", async () => {
    const result = await apiPost("/api/v1/push/start", {});
    setBox("pushBox", result);
  });
  document.getElementById("btnStopPush").onclick = withError("pushBox", async () => {
    const result = await apiPost("/api/v1/push/stop", {});
    setBox("pushBox", result);
  });
  document.getElementById("btnRestartPush").onclick = withError("pushBox", async () => {
    const result = await apiPost("/api/v1/push/restart", {});
    setBox("pushBox", result);
  });
  document.getElementById("btnPushStatus").onclick = withError("pushBox", async () => {
    const result = await apiGet("/api/v1/push/status");
    setBox("pushBox", result);
  });
  document.getElementById("btnFillPushCommand").onclick = withError("pushBox", async () => {
    document.getElementById("pushCommand").value = recommendedAdvancedCommand(asString("pushInputType"));
    setBox("pushBox", { code: 0, message: "已按当前输入类型填充推荐高级命令" });
  });
  document.getElementById("btnStartMosaicLocalPreview").onclick = withError("mosaicPreviewBox", startMosaicLocalPreview);
  document.getElementById("btnStopMosaicLocalPreview").onclick = withError("mosaicPreviewBox", async () => {
    stopMosaicLocalPreview();
    setBox("mosaicPreviewBox", { code: 0, message: "已关闭本地预览" });
  });
  document.getElementById("pushInputType").addEventListener("change", suggestPushAdvancedCommand);
  document.getElementById("pushModel").addEventListener("change", suggestPushAdvancedCommand);
  document.getElementById("pushBitratePreset").addEventListener("change", syncPushBitrateCustomVisibility);
  document.getElementById("btnAddMosaicSource").onclick = withError("mosaicPreviewBox", async () => {
    const url = asString("mosaicNewSourceUrl");
    if (!url) throw new Error("请输入拼屏源地址");
    const title = asString("mosaicNewSourceTitle");
    const next = [...mosaicSources, { url, title, primary: mosaicSources.length === 0, sourceType: "manual", materialId: 0 }];
    setMosaicSources(next);
    document.getElementById("mosaicNewSourceUrl").value = "";
    document.getElementById("mosaicNewSourceTitle").value = "";
    setBox("mosaicPreviewBox", buildMosaicPreview());
  });
  document.getElementById("pushMultiUrls").addEventListener("change", () => {
    const urls = parseLines(document.getElementById("pushMultiUrls").value || "");
    const next = [];
    for (const url of urls) {
      const existing = mosaicSources.find((item) => item.url === url);
      if (existing) {
        next.push(existing);
      } else {
        next.push({ url, title: "", primary: next.length === 0, sourceType: "manual", materialId: 0 });
      }
    }
    setMosaicSources(next);
  });
  document.getElementById("btnSyncMosaicText").onclick = withError("mosaicPreviewBox", async () => {
    const urls = parseLines(document.getElementById("pushMultiUrls").value || "");
    const merged = [];
    for (const url of urls) {
      const existing = mosaicSources.find((item) => item.url === url);
      if (existing) {
        merged.push(existing);
      } else {
        merged.push({ url, title: "", primary: merged.length === 0, sourceType: "manual", materialId: 0 });
      }
    }
    setMosaicSources(merged);
    setBox("mosaicPreviewBox", buildMosaicPreview());
  });
  document.getElementById("btnLoadMosaicMaterials").onclick = withError("mosaicPreviewBox", async () => {
    const result = await apiGet("/api/v1/materials?page=1&limit=200&fileType=1");
    const select = document.getElementById("mosaicMaterialSelect");
    select.innerHTML = "";
    const rows = result && result.data && Array.isArray(result.data.data) ? result.data.data : [];
    for (const row of rows) {
      const option = document.createElement("option");
      option.value = row.fullPath || row.path || "";
      option.textContent = `${row.id || 0} - ${row.name || row.path || "material"}`;
      option.dataset.materialId = String(row.id || 0);
      option.dataset.title = row.name || "";
      select.appendChild(option);
    }
    setBox("mosaicPreviewBox", result);
  });
  document.getElementById("btnAddSelectedMaterials").onclick = withError("mosaicPreviewBox", async () => {
    const select = document.getElementById("mosaicMaterialSelect");
    const selected = Array.from(select.selectedOptions || []);
    if (!selected.length) throw new Error("请先在素材库列表中多选素材");
    const next = [...mosaicSources];
    for (const option of selected) {
      const url = (option.value || "").trim();
      if (!url) continue;
      if (next.some((item) => item.url === url)) continue;
      next.push({
        url,
        title: option.dataset.title || option.textContent || "",
        primary: next.length === 0,
        sourceType: "material",
        materialId: Number(option.dataset.materialId || 0) || 0,
      });
      if (next.length >= 9) break;
    }
    setMosaicSources(next);
    setBox("mosaicPreviewBox", buildMosaicPreview());
  });
  document.getElementById("btnLoadMosaicCameras").onclick = withError("mosaicPreviewBox", loadMosaicCameraOptions);
  document.getElementById("mosaicCameraType").addEventListener("change", withError("mosaicPreviewBox", loadMosaicCameraOptions));
  document.getElementById("btnAddSelectedCameras").onclick = withError("mosaicPreviewBox", async () => {
    const select = document.getElementById("mosaicCameraSelect");
    const selected = Array.from(select.selectedOptions || []);
    if (!selected.length) throw new Error("请先在摄像头库列表中多选摄像头");
    const next = [...mosaicSources];
    for (const option of selected) {
      const url = (option.value || "").trim();
      if (!url) continue;
      if (next.some((item) => item.url === url)) continue;
      next.push({
        url,
        title: option.dataset.title || option.textContent || "",
        primary: next.length === 0,
        sourceType: option.dataset.sourceType || "camera",
        materialId: 0,
      });
      if (next.length >= 9) break;
    }
    setMosaicSources(next);
    setBox("mosaicPreviewBox", buildMosaicPreview());
  });
  document.getElementById("btnPreviewMosaic").onclick = withError("mosaicPreviewBox", async () => {
    const preview = buildMosaicPreview();
    setBox("mosaicPreviewBox", preview);
  });

  document.getElementById("btnUploadMaterial").onclick = withError("materialBox", async () => {
    const input = document.getElementById("materialUpload");
    const files = input.files;
    if (!files || files.length === 0) {
      throw new Error("请先选择文件");
    }
    const form = new FormData();
    for (const file of files) {
      form.append("file", file);
    }
    const result = await request("/api/v1/materials/upload", { method: "POST", body: form });
    setBox("materialBox", result);
    await refreshMaterials();
  });
  document.getElementById("btnListMaterial").onclick = withError("materialBox", refreshMaterials);

  document.getElementById("btnSaveApiKey").onclick = withError("integrationBox", async () => {
    const payload = {
      name: asString("apiKeyName"),
      apiKey: document.getElementById("apiKeyValue").value || "",
      description: document.getElementById("apiKeyDesc").value || "",
    };
    const result = await apiPost("/api/v1/integration/api-keys", payload);
    setBox("integrationBox", result);
    await refreshApiKeys();
  });
  document.getElementById("btnListApiKey").onclick = withError("integrationBox", refreshApiKeys);
  document.getElementById("btnSaveRule").onclick = withError("integrationBox", async () => {
    const payload = {
      keyword: asString("ruleKeyword"),
      action: asString("ruleAction"),
      ptzDirection: asString("ruleDirection"),
      ptzSpeed: asNumber("ruleSpeed", 1),
      enabled: true,
    };
    const result = await apiPost("/api/v1/integration/danmaku-rules", payload);
    setBox("integrationBox", result);
  });
  document.getElementById("btnListRule").onclick = withError("integrationBox", async () => {
    const result = await apiGet("/api/v1/integration/danmaku-rules?limit=200&offset=0");
    setBox("integrationBox", result);
  });
  document.getElementById("btnSaveWebhook").onclick = withError("integrationBox", async () => {
    const payload = {
      name: asString("webhookName"),
      url: asString("webhookUrl"),
      secret: document.getElementById("webhookSecret").value || "",
      enabled: asString("webhookEnabled") === "true",
    };
    const result = await apiPost("/api/v1/integration/webhooks", payload);
    setBox("integrationBox", result);
  });
  document.getElementById("btnListWebhook").onclick = withError("integrationBox", async () => {
    const result = await apiGet("/api/v1/integration/webhooks?limit=200&offset=0");
    setBox("integrationBox", result);
  });
  document.getElementById("btnTestWebhook").onclick = withError("integrationBox", async () => {
    const payload = {
      id: asNumber("webhookTestId", 0),
      eventType: asString("webhookEventType") || "webhook.test",
    };
    const result = await apiPost("/api/v1/integration/webhooks/test", payload);
    setBox("integrationBox", result);
  });
  document.getElementById("btnNotifyWebhook").onclick = withError("integrationBox", async () => {
    const payload = {
      eventType: asString("webhookEventType") || "manual.notify",
      payload: {
        trigger: "manual",
        time: new Date().toISOString(),
      },
    };
    const result = await apiPost("/api/v1/integration/notify", payload);
    setBox("integrationBox", result);
  });
  document.getElementById("btnDeliveryLogs").onclick = withError("integrationBox", async () => {
    const result = await apiGet("/api/v1/integration/webhooks/delivery-logs?limit=100");
    setBox("integrationBox", result);
  });
  document.getElementById("btnPTZDiscover").onclick = withError("integrationBox", async () => {
    const result = await apiGet("/api/v1/ptz/discover?timeoutMs=8000");
    setBox("integrationBox", result);
    const select = document.getElementById("ptzDiscoveredList");
    select.innerHTML = '<option value="">（请选择自动发现到的设备）</option>';
    const items = result && result.data && Array.isArray(result.data.items) ? result.data.items : [];
    for (const item of items) {
      const endpoint = item.endpoint || (Array.isArray(item.xAddrs) ? item.xAddrs[0] : "");
      if (!endpoint) continue;
      const option = document.createElement("option");
      option.value = endpoint;
      option.textContent = `${endpoint}${item.from ? ` (${item.from})` : ""}`;
      select.appendChild(option);
    }
  });
  document.getElementById("ptzDiscoveredList").onchange = () => {
    const endpoint = document.getElementById("ptzDiscoveredList").value || "";
    if (endpoint) {
      document.getElementById("ptzEndpoint").value = endpoint;
    }
  };
  document.getElementById("btnPTZCapabilities").onclick = withError("integrationBox", async () => {
    const endpoint = encodeURIComponent(asString("ptzEndpoint"));
    const username = encodeURIComponent(asString("ptzUsername"));
    const password = encodeURIComponent(document.getElementById("ptzPassword").value || "");
    const result = await apiGet(`/api/v1/ptz/capabilities?endpoint=${endpoint}&username=${username}&password=${password}`);
    setBox("integrationBox", result);
  });
  document.getElementById("btnPTZProfiles").onclick = withError("integrationBox", async () => {
    const payload = {
      endpoint: asString("ptzEndpoint"),
      username: asString("ptzUsername"),
      password: document.getElementById("ptzPassword").value || "",
    };
    const result = await apiPost("/api/v1/ptz/profiles", payload);
    setBox("integrationBox", result);
  });
  document.getElementById("btnPTZCommand").onclick = withError("integrationBox", async () => {
    const payload = {
      endpoint: asString("ptzEndpoint"),
      username: asString("ptzUsername"),
      password: document.getElementById("ptzPassword").value || "",
      profileToken: asString("ptzProfileToken"),
      presetToken: asString("ptzPresetToken"),
      action: asString("ptzAction"),
      speed: Number(document.getElementById("ptzSpeed").value || 0.3),
      durationMs: asNumber("ptzDurationMs", 700),
      pan: Number(document.getElementById("ptzPan").value || 0),
      tilt: Number(document.getElementById("ptzTilt").value || 0),
      zoom: Number(document.getElementById("ptzZoom").value || 0),
    };
    const result = await apiPost("/api/v1/ptz/command", payload);
    setBox("integrationBox", result);
    if (result && result.data && result.data.presetToken) {
      document.getElementById("ptzPresetToken").value = result.data.presetToken;
    }
  });
  document.getElementById("btnBotCommand").onclick = withError("integrationBox", async () => {
    const payload = {
      provider: asString("botProvider"),
      command: asString("botCommand"),
      params: parseJSONInput(document.getElementById("botParams").value || "{}", {}),
    };
    const result = await apiPost("/api/v1/integration/bot/command", payload);
    setBox("integrationBox", result);
  });

  document.getElementById("btnDispatchDanmaku").onclick = withError("liveDataBox", async () => {
    const payload = {
      roomId: asNumber("danmakuRoomId", 0),
      uid: asNumber("danmakuUID", 0),
      uname: asString("danmakuUname"),
      content: asString("danmakuContent"),
      source: asString("danmakuSource"),
      rawPayload: "",
    };
    const result = await apiPost("/api/v1/integration/danmaku/dispatch", payload);
    setBox("liveDataBox", result);
  });
  document.getElementById("btnListEvents").onclick = withError("liveDataBox", async () => {
    const result = await apiGet("/api/v1/live/events?limit=100");
    setBox("liveDataBox", result);
  });
  document.getElementById("btnListDanmaku").onclick = withError("liveDataBox", async () => {
    const roomId = asNumber("danmakuRoomId", 0);
    const suffix = roomId > 0 ? `?roomId=${roomId}&limit=100` : "?limit=100";
    const result = await apiGet(`/api/v1/live/danmaku${suffix}`);
    setBox("liveDataBox", result);
  });
  document.getElementById("btnLiveStats").onclick = withError("liveDataBox", async () => {
    const result = await apiGet("/api/v1/live/stats?hours=24");
    setBox("liveDataBox", result);
  });
  document.getElementById("btnBiliErrors").onclick = withError("liveDataBox", async () => {
    const result = await apiGet("/api/v1/integration/bilibili/errors?limit=100");
    setBox("liveDataBox", result);
  });
  document.getElementById("btnBiliErrorLogs").onclick = withError("liveDataBox", async () => {
    const limit = 100;
    const endpoint = encodeURIComponent(asString("biliErrorEndpoint"));
    const suffix = endpoint ? `?limit=${limit}&endpoint=${endpoint}` : `?limit=${limit}`;
    const result = await apiGet(`/api/v1/integration/bilibili/error-logs${suffix}`);
    setBox("liveDataBox", result);
  });
  document.getElementById("btnBiliErrorLogDetail").onclick = withError("liveDataBox", async () => {
    const id = asNumber("biliErrorLogId", 0);
    if (id <= 0) {
      throw new Error("请先输入有效的错误日志 ID");
    }
    const result = await apiGet(`/api/v1/integration/bilibili/error-logs/${id}`);
    setBox("liveDataBox", result);
  });
  document.getElementById("btnBiliErrorSummary").onclick = withError("liveDataBox", async () => {
    const result = await apiGet("/api/v1/integration/bilibili/errors/summary?limit=500");
    setBox("liveDataBox", result);
  });
  document.getElementById("btnBiliErrorInsights").onclick = withError("liveDataBox", async () => {
    const result = await apiGet("/api/v1/integration/bilibili/errors/insights?limit=500&windowMinutes=60");
    setBox("liveDataBox", result);
  });
  document.getElementById("btnLoadBiliAlertSetting").onclick = withError("liveDataBox", refreshBiliAlertSetting);
  document.getElementById("btnSaveBiliAlertSetting").onclick = withError("liveDataBox", async () => {
    const payload = {
      enabled: asString("biliAlertEnabled") === "true",
      windowMinutes: asNumber("biliAlertWindow", 10),
      threshold: asNumber("biliAlertThreshold", 8),
      cooldownMinutes: asNumber("biliAlertCooldown", 15),
      webhookEvent: asString("biliAlertEventType") || "bilibili.api.alert",
    };
    const result = await apiPost("/api/v1/integration/bilibili/alert-setting", payload);
    setBox("liveDataBox", result);
  });
  document.getElementById("btnCheckBiliAlert").onclick = withError("liveDataBox", async () => {
    const result = await apiPost("/api/v1/integration/bilibili/errors/check-alert", {});
    setBox("liveDataBox", result);
  });
  document.getElementById("btnLoadConsumerSetting").onclick = withError("liveDataBox", refreshDanmakuConsumerSetting);
  document.getElementById("btnSaveConsumerSetting").onclick = withError("liveDataBox", async () => {
    const configRaw = document.getElementById("consumerConfigJson").value || "{}";
    let configObj = {};
    try {
      configObj = parseJSONInput(configRaw, {});
    } catch (error) {
      throw new Error(`consumerConfigJson 不是合法 JSON: ${error.message || String(error)}`);
    }
    const payload = {
      enabled: asString("consumerEnabled") === "true",
      provider: asString("consumerProvider") || "http_polling",
      endpoint: asString("consumerEndpoint"),
      authToken: asString("consumerAuthToken"),
      configJson: JSON.stringify(configObj),
      pollIntervalSec: asNumber("consumerPollInterval", 3),
      batchSize: asNumber("consumerBatchSize", 20),
      roomId: asNumber("consumerRoomIdFilter", 0),
    };
    const result = await apiPost("/api/v1/integration/danmaku/consumer/setting", payload);
    setBox("liveDataBox", result);
  });
  document.getElementById("consumerProvider").onchange = () => {
    const provider = asString("consumerProvider");
    const configBox = document.getElementById("consumerConfigJson");
    const current = (configBox.value || "").trim();
    if (provider === "bilibili_message_stream" && (!current || current === "{}")) {
      configBox.value = JSON.stringify(
        {
          roomId: asNumber("consumerRoomIdFilter", 0),
          useWbi: true,
          useCookie: true,
          preferWss: true,
          webLocation: "444.8",
          wsPath: "/sub",
          includeCommands: ["DANMU_MSG"],
        },
        null,
        2
      );
    } else if (provider === "http_polling" && (!current || current === "{}")) {
      configBox.value = JSON.stringify(
        {
          method: "GET",
          auth: { mode: "bearer" },
          paging: {
            cursorField: "cursor",
            cursorIn: "query",
            cursorMode: "cursor",
            responseCursorPath: "data.next_cursor|nextCursor",
            itemCursorPath: "id|cursor",
            limitField: "pageSize",
            roomIdField: "roomId",
          },
          mapping: {
            itemsPath: "data.list",
            roomIdPath: "room_id|roomId",
            uidPath: "uid|user.id",
            unamePath: "uname|user.name",
            contentPath: "content|message",
            rawPayloadPath: "raw",
          },
        },
        null,
        2
      );
    }
  };
  document.getElementById("advancedExportPreset").onchange = () => {
    applyAdvancedExportPreset(asString("advancedExportPreset"), true);
  };
  document.getElementById("btnConsumerStatus").onclick = withError("liveDataBox", refreshDanmakuConsumerStatus);
  document.getElementById("btnConsumerPollOnce").onclick = withError("liveDataBox", async () => {
    const result = await apiPost("/api/v1/integration/danmaku/consumer/poll-once", {});
    setBox("liveDataBox", result);
  });
  document.getElementById("btnListIntegrationTasks").onclick = withError("liveDataBox", async () => {
    const status = encodeURIComponent(asString("integrationTaskStatus"));
    const type = encodeURIComponent(asString("integrationTaskType"));
    const result = await apiGet(`/api/v1/integration/tasks?limit=200&status=${status}&type=${type}`);
    setBox("liveDataBox", result);
  });
  document.getElementById("btnIntegrationTaskSummary").onclick = withError("liveDataBox", refreshIntegrationTaskSummary);
  document.getElementById("btnRetryIntegrationTask").onclick = withError("liveDataBox", async () => {
    const id = asNumber("integrationTaskRetryId", 0);
    if (id <= 0) throw new Error("请先输入有效的任务ID");
    const result = await apiPost("/api/v1/integration/tasks/retry", { id });
    setBox("liveDataBox", result);
  });
  document.getElementById("btnRetryIntegrationTaskBatch").onclick = withError("liveDataBox", async () => {
    const payload = {
      status: asString("integrationTaskBatchStatus"),
      type: asString("integrationTaskType"),
      limit: asNumber("integrationTaskBatchLimit", 200),
    };
    const result = await apiPost("/api/v1/integration/tasks/retry-batch", payload);
    setBox("liveDataBox", result);
  });
  document.getElementById("btnCancelIntegrationTask").onclick = withError("liveDataBox", async () => {
    const id = asNumber("integrationTaskCancelId", 0);
    if (id <= 0) throw new Error("请先输入有效的任务ID");
    const result = await apiPost("/api/v1/integration/tasks/cancel", { id });
    setBox("liveDataBox", result);
  });
  document.getElementById("btnUpdateIntegrationTaskPriority").onclick = withError("liveDataBox", async () => {
    const id = asNumber("integrationTaskCancelId", 0);
    const priority = asNumber("integrationTaskPriority", 100);
    if (id <= 0) throw new Error("请先输入任务ID");
    if (priority <= 0) throw new Error("priority 必须大于 0");
    const result = await apiPost("/api/v1/integration/tasks/priority", { id, priority });
    setBox("liveDataBox", result);
  });
  document.getElementById("btnLoadQueueSetting").onclick = withError("liveDataBox", refreshIntegrationQueueSetting);
  document.getElementById("btnSaveQueueSetting").onclick = withError("liveDataBox", async () => {
    const payload = {
      webhookRateGapMs: asNumber("queueWebhookGapMs", 300),
      botRateGapMs: asNumber("queueBotGapMs", 300),
      maxWorkers: asNumber("queueMaxWorkers", 3),
      leaseIntervalMs: asNumber("queueLeaseIntervalMs", 500),
    };
    const result = await apiPost("/api/v1/integration/tasks/queue-setting", payload);
    setBox("liveDataBox", result);
    await refreshIntegrationQueueSetting();
  });
  document.getElementById("btnLiveAdvancedStats").onclick = withError("liveDataBox", async () => {
    const hours = asNumber("advancedStatsHours", 24);
    const granularity = encodeURIComponent(asString("advancedStatsGranularity") || "hour");
    const result = await apiGet(`/api/v1/live/stats/advanced?hours=${hours}&granularity=${granularity}`);
    setBox("liveDataBox", result);
    latestAdvancedStats = result && result.data ? result.data : null;
    drawAdvancedStatsCharts(latestAdvancedStats);
  });
  document.getElementById("btnExportAdvancedStatsCsv").onclick = withError("liveDataBox", async () => {
    await downloadAdvancedStats("csv");
    setBox("liveDataBox", { code: 0, message: "统计CSV已导出" });
  });
  document.getElementById("btnExportAdvancedStatsJson").onclick = withError("liveDataBox", async () => {
    await downloadAdvancedStats("json");
    setBox("liveDataBox", { code: 0, message: "统计JSON已导出" });
  });
  document.getElementById("btnLoadIntegrationFeatures").onclick = withError("liveDataBox", refreshIntegrationFeatures);
  document.getElementById("btnSaveIntegrationFeatures").onclick = withError("liveDataBox", async () => {
    const payload = {
      simpleMode: boolValue("featureSimpleMode", false),
      enableDanmakuConsumer: boolValue("featureDanmakuConsumer", false),
      enableWebhook: boolValue("featureWebhook", true),
      enableBot: boolValue("featureBot", true),
      enableAdvancedStats: boolValue("featureAdvancedStats", true),
      enableTaskQueue: boolValue("featureTaskQueue", true),
    };
    const result = await apiPost("/api/v1/integration/features", payload);
    setBox("liveDataBox", result);
    await refreshIntegrationFeatures();
  });
  document.getElementById("btnRuntimeMemory").onclick = withError("liveDataBox", refreshRuntimeMemory);
  document.getElementById("btnRuntimeGC").onclick = withError("liveDataBox", async () => {
    const result = await apiPost("/api/v1/integration/runtime/gc", {});
    setBox("liveDataBox", result);
  });

  document.getElementById("btnLoadLogs").onclick = withError("logBox", refreshLogs);

  document.getElementById("btnLoadMaintenance").onclick = withError("maintenanceBox", refreshMaintenanceSetting);
  document.getElementById("btnSaveMaintenance").onclick = withError("maintenanceBox", async () => {
    const payload = {
      enabled: asString("maintenanceEnabled") === "true",
      retentionDays: asNumber("maintenanceDays", 7),
      autoVacuum: asString("maintenanceAutoVacuum") === "true",
    };
    const result = await apiPost("/api/v1/maintenance/setting", payload);
    setBox("maintenanceBox", result);
  });
  document.getElementById("btnMaintenanceStatus").onclick = withError("maintenanceBox", refreshMaintenanceStatus);
  document.getElementById("btnCleanupNow").onclick = withError("maintenanceBox", async () => {
    const payload = {
      days: asNumber("maintenanceNowDays", 7),
      vacuum: asString("maintenanceAutoVacuum") === "true",
    };
    const result = await apiPost("/api/v1/maintenance/cleanup", payload);
    setBox("maintenanceBox", result);
    await refreshMaintenanceStatus();
  });
  document.getElementById("btnVacuumNow").onclick = withError("maintenanceBox", async () => {
    const result = await apiPost("/api/v1/maintenance/vacuum", {});
    setBox("maintenanceBox", result);
    await refreshMaintenanceStatus();
  });
  document.getElementById("btnCancelMaintenance").onclick = withError("maintenanceBox", async () => {
    const currentJobId = latestMaintenanceStatus && latestMaintenanceStatus.current ? latestMaintenanceStatus.current.id || "" : "";
    const result = await apiPost("/api/v1/maintenance/cancel", { jobId: currentJobId });
    setBox("maintenanceBox", result);
    await refreshMaintenanceStatus();
  });
}

async function init() {
  const authOK = await ensureAuthenticated();
  if (!authOK) return;

  bindActions();
  applyAdvancedExportPreset(asString("advancedExportPreset") || "all", true);
  window.addEventListener("message", (event) => {
    const data = event && event.data ? event.data : {};
    if (!data || data.type !== "gover-login-success") return;
    stopAccountStatusPolling();
    showAccountQrImage("");
    setAccountQrInfo("【<span style=\"color:green;\">扫码登录成功</span>】Cookie 已同步");
    refreshAccount().catch((error) => {
      setBox("accountBox", { code: -1, message: error.message || String(error) });
    });
  });
  setMosaicSources([]);
  applyPushBitrateValue(0);
  stopMosaicLocalPreview();
  await refreshRuntimeConfig();
  await refreshAccount();
  await refreshRoom();
  await refreshPush();
  await loadMosaicCameraOptions();
  await refreshMaterials();
  await refreshApiKeys();
  await refreshLogs();
  await refreshBiliAlertSetting();
  await refreshIntegrationFeatures();
  await refreshDanmakuConsumerSetting();
  await refreshIntegrationTaskSummary();
  await refreshIntegrationQueueSetting();
  await refreshMaintenanceSetting();
  await refreshMaintenanceStatus();
  drawAdvancedStatsCharts(latestAdvancedStats);
  pollEnabled = true;
}

init().catch((error) => {
  console.error(error);
  setBox("accountBox", { code: -1, message: error.message || String(error) });
});

setInterval(() => {
  if (!pollEnabled) return;
  refreshAccount().catch(() => {});
}, 3000);

setInterval(() => {
  if (!pollEnabled) return;
  refreshMaintenanceStatus().catch(() => {});
}, 5000);
