const state = {
  token: localStorage.getItem("valheim_panel_token") || "",
  user: null,
  settings: {},
  overview: null,
  instances: [],
  currentId: localStorage.getItem("valheim_panel_instance") || "",
  current: null,
  section: sectionFromHash(),
  detailTab: "overview",
  logTab: "server",
  logs: { server: "", steam: "" },
  modPackages: [],
  recommendedMods: [],
  installedMods: [],
  backups: [],
};

const $ = (selector, root = document) => root.querySelector(selector);
const $$ = (selector, root = document) => [...root.querySelectorAll(selector)];

function escapeHtml(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function formatDate(value) {
  if (!value) return "-";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return escapeHtml(value);
  return date.toLocaleString("zh-CN", { hour12: false });
}

function statusText(instance) {
  if (instance.status?.running) return "运行中";
  if (instance.status?.installed) return "已停止";
  return "未安装";
}

async function api(path, options = {}) {
  const headers = { ...(options.headers || {}) };
  if (state.token) headers.authorization = `Bearer ${state.token}`;
  let body;
  if (options.rawBody !== undefined) {
    body = options.rawBody;
  } else if (options.body !== undefined) {
    headers["content-type"] = "application/json";
    body = JSON.stringify(options.body);
  }
  const response = await fetch(`/api${path}`, {
    method: options.method || "GET",
    headers,
    body,
  });
  const text = await response.text();
  let payload = null;
  try {
    payload = text ? JSON.parse(text) : null;
  } catch {
    payload = { raw: text };
  }
  if (response.status === 401) {
    logout(false);
    throw new Error(payload?.error || "登录已失效");
  }
  if (!response.ok) throw new Error(payload?.error || `请求失败 (${response.status})`);
  return payload;
}

function toast(message, type = "info") {
  const root = $("#toast-root");
  const item = document.createElement("div");
  item.className = `toast ${type === "error" ? "error" : ""}`;
  item.textContent = message;
  root.append(item);
  setTimeout(() => item.remove(), 4200);
}

function showLogin() {
  $("#login-view").hidden = false;
  $("#app-view").hidden = true;
}

function showApp() {
  $("#login-view").hidden = true;
  $("#app-view").hidden = false;
}

function logout(notify = true) {
  if (state.token) {
    fetch("/api/auth/logout", {
      method: "POST",
      headers: { authorization: `Bearer ${state.token}` },
    }).catch(() => {});
  }
  state.token = "";
  state.user = null;
  localStorage.removeItem("valheim_panel_token");
  showLogin();
  if (notify) toast("已退出登录");
}

async function boot() {
  bindGlobalEvents();
  if (!state.token) {
    showLogin();
    return;
  }
  try {
    const payload = await api("/auth/me");
    state.user = payload.user;
    $("#sidebar-user").textContent = state.user.username;
    showApp();
    await loadAll();
    await setSection(state.section, true);
  } catch {
    logout(false);
  }
}

async function loadAll() {
  await Promise.all([loadOverview(), loadInstances(), loadSettings()]);
  if (!state.currentId && state.instances.length) {
    state.currentId = state.instances[0].id;
    localStorage.setItem("valheim_panel_instance", state.currentId);
  }
  if (state.currentId) await loadCurrent();
  renderAll();
}

async function loadOverview() {
  state.overview = await api("/overview");
  state.settings = { ...state.settings, ...(state.overview.settings || {}) };
}

async function loadInstances() {
  state.instances = await api("/instances");
  if (state.currentId && !state.instances.some((instance) => instance.id === state.currentId)) {
    state.currentId = state.instances[0]?.id || "";
    localStorage.setItem("valheim_panel_instance", state.currentId);
  }
}

async function loadSettings() {
  state.settings = await api("/settings");
  const system = await api("/system");
  renderSystemInfo(system);
  renderSettingsForm();
}

async function loadCurrent() {
  if (!state.currentId) {
    state.current = null;
    return;
  }
  state.current = await api(`/instances/${encodeURIComponent(state.currentId)}`);
  state.installedMods = await api(`/instances/${encodeURIComponent(state.currentId)}/mods`);
}

async function refreshCurrent() {
  await Promise.all([loadOverview(), loadInstances()]);
  await loadCurrent();
  renderAll();
}

function renderAll() {
  renderTopMeta();
  renderOverview();
  renderInstanceCards();
  renderInstancePicker();
  renderDetail();
  renderRecommendedMods();
  renderMods();
  renderBackups();
  renderLogs();
}

function renderTopMeta() {
  const chip = $("#current-instance-chip");
  if (!chip) return;
  if (!state.current) {
    chip.textContent = "当前实例：未选择";
    return;
  }
  chip.textContent = `当前实例：${state.current.name} · ${statusText(state.current)}`;
}

function renderOverview() {
  const overview = state.overview || {};
  $("#overview-metrics").innerHTML = [
    metric("服务器实例", overview.instances ?? 0, `${overview.installed ?? 0} 个已安装`),
    metric("运行中", overview.running ?? 0, "实时进程状态"),
    metric("磁盘占用", overview.disk || "0 B", "实例目录合计"),
    metric("SteamCMD", overview.steamcmd ? "已发现" : "未发现", overview.platform || "-"),
  ].join("");

  $("#overview-instances").innerHTML = state.instances.length
    ? state.instances.map((instance) => `
      <div class="instance-row">
        <div>
          <strong>${escapeHtml(instance.name)}</strong>
          <p>${escapeHtml(instance.server?.world || "-")} · UDP ${instance.server?.port || "-"} · ${instance.status?.mods || 0} 个模组</p>
        </div>
        <span class="status-pill ${instance.status?.running ? "running" : ""}">${statusText(instance)}</span>
      </div>
    `).join("")
    : `<div class="empty-state">还没有服务器实例。先创建一个实例，再从控制台执行安装。</div>`;

  const current = state.current;
  $("#quick-actions").innerHTML = [
    quickRow("新建 Valheim 实例", "配置名称、世界、端口和密码", "open-create"),
    current ? quickRow("安装或更新服务器", "SteamCMD / App 896660", "install", current.id) : "",
    current ? quickRow(current.status?.running ? "停止当前实例" : "启动当前实例", `UDP ${current.server?.port || "-"}`, current.status?.running ? "stop" : "start", current.id) : "",
    current ? quickRow("创建世界备份", "保存世界与 BepInEx 配置", "backup", current.id) : "",
  ].filter(Boolean).join("");
}

function metric(label, value, note) {
  return `
    <div class="metric">
      <span class="metric-label">${escapeHtml(label)}</span>
      <strong class="metric-value">${escapeHtml(value)}</strong>
      <span class="metric-note">${escapeHtml(note)}</span>
    </div>
  `;
}

function quickRow(title, note, action, id = "") {
  return `
    <button type="button" class="quick-row" data-action="${action}" data-id="${escapeHtml(id)}">
      <span>
        <strong>${escapeHtml(title)}</strong>
        <p>${escapeHtml(note)}</p>
      </span>
      <span class="muted">›</span>
    </button>
  `;
}

function renderInstancePicker() {
  const picker = $("#instance-picker");
  picker.innerHTML = state.instances.length
    ? state.instances.map((instance) => `<option value="${escapeHtml(instance.id)}" ${instance.id === state.currentId ? "selected" : ""}>${escapeHtml(instance.name)} · ${statusText(instance)}</option>`).join("")
    : `<option value="">暂无实例</option>`;
}

function renderInstanceCards() {
  const root = $("#instance-cards");
  root.innerHTML = state.instances.length
    ? state.instances.map((instance) => `
      <article class="instance-card" data-action="select-instance" data-id="${escapeHtml(instance.id)}">
        <div class="instance-card-head">
          <div>
            <h3>${escapeHtml(instance.name)}</h3>
            <p>${escapeHtml(instance.description || "Valheim dedicated server")}</p>
          </div>
          <span class="status-pill ${instance.status?.running ? "running" : ""}">${statusText(instance)}</span>
        </div>
        <div class="instance-card-meta">
          <div class="mini-meta"><span>世界</span><strong>${escapeHtml(instance.server?.world || "-")}</strong></div>
          <div class="mini-meta"><span>端口</span><strong>${instance.server?.port || "-"}/UDP</strong></div>
          <div class="mini-meta"><span>BepInEx</span><strong>${instance.status?.bepinexInstalled ? "已安装" : "未安装"}</strong></div>
          <div class="mini-meta"><span>磁盘</span><strong>${escapeHtml(instance.status?.disk || "0 B")}</strong></div>
        </div>
        <div class="action-row">
          <button type="button" class="button small secondary" data-action="select-instance" data-id="${escapeHtml(instance.id)}">管理</button>
          <button type="button" class="button small" data-action="${instance.status?.running ? "stop" : "start"}" data-id="${escapeHtml(instance.id)}">${instance.status?.running ? "停止" : "启动"}</button>
          <button type="button" class="button small ghost" data-action="edit-instance" data-id="${escapeHtml(instance.id)}">编辑</button>
          <button type="button" class="button small danger" data-action="delete-instance" data-id="${escapeHtml(instance.id)}">删除</button>
        </div>
      </article>
    `).join("")
    : `<div class="empty-state">当前没有实例。创建后可直接从面板安装 Valheim Dedicated Server。</div>`;
}

function renderDetail() {
  const root = $("#instance-detail");
  const instance = state.current;
  if (!instance) {
    root.hidden = true;
    return;
  }
  root.hidden = false;
  $("#detail-title").textContent = instance.name;
  $("#detail-subtitle").textContent = `${instance.server?.world || "-"} · UDP ${instance.server?.port || "-"} · ${instance.paths?.installDir || ""}`;
  $("#detail-actions").innerHTML = [
    `<button type="button" class="button secondary" data-action="${instance.status?.installed ? "update" : "install"}" data-id="${escapeHtml(instance.id)}">${instance.status?.installed ? "更新服务端" : "安装服务端"}</button>`,
    `<button type="button" class="button" data-action="${instance.status?.running ? "stop" : "start"}" data-id="${escapeHtml(instance.id)}">${instance.status?.running ? "停止" : "启动"}</button>`,
    `<button type="button" class="button ghost" data-action="restart" data-id="${escapeHtml(instance.id)}">重启</button>`,
  ].join("");
  $$(".tab", $("#detail-tabs")).forEach((tab) => tab.classList.toggle("active", tab.dataset.detailTab === state.detailTab));

  const content = $("#detail-content");
  if (state.detailTab === "overview") content.innerHTML = renderDetailOverview(instance);
  if (state.detailTab === "config") {
    content.innerHTML = renderConfigForm(instance);
    $("#instance-config-form")?.addEventListener("submit", saveInstanceConfig);
  }
  if (state.detailTab === "mods") content.innerHTML = renderModsMarkup();
  if (state.detailTab === "backups") content.innerHTML = renderBackupsMarkup();
  if (state.detailTab === "logs") content.innerHTML = renderLogsMarkup();
}

function renderDetailOverview(instance) {
  const launch = [
    "-name", instance.server?.name,
    "-port", instance.server?.port,
    "-world", instance.server?.world,
    "-password", instance.server?.password ? "********" : "(empty)",
    "-public", instance.server?.public ? "1" : "0",
    "-savedir", instance.paths?.saveDir,
    instance.server?.crossplay ? "-crossplay" : "",
  ].filter(Boolean).join(" ");
  return `
    <div class="detail-grid">
      <section class="info-block">
        <h3>运行状态</h3>
        <dl class="info-list">
          <dt>状态</dt><dd>${statusText(instance)}</dd>
          <dt>PID</dt><dd>${instance.status?.pid || "-"}</dd>
          <dt>安装目录</dt><dd>${escapeHtml(instance.paths?.installDir || "-")}</dd>
          <dt>存档目录</dt><dd>${escapeHtml(instance.paths?.saveDir || "-")}</dd>
          <dt>磁盘占用</dt><dd>${escapeHtml(instance.status?.disk || "0 B")}</dd>
        </dl>
      </section>
      <section class="info-block">
        <h3>模组环境</h3>
        <dl class="info-list">
          <dt>BepInEx</dt><dd>${instance.status?.bepinexInstalled ? "已安装" : "未安装"}</dd>
          <dt>BepInEx 版本</dt><dd>${escapeHtml(instance.bepinex?.version || "-")}</dd>
          <dt>已安装模组</dt><dd>${instance.status?.mods || 0}</dd>
          <dt>启用模组</dt><dd>${instance.status?.enabledMods || 0}</dd>
        </dl>
      </section>
      <section class="info-block" style="grid-column: 1 / -1">
        <h3>启动参数</h3>
        <pre class="log-output" style="min-height: auto; max-height: 180px">${escapeHtml(launch)}</pre>
      </section>
    </div>
  `;
}

function renderConfigForm(instance) {
  return `
    <form id="instance-config-form" class="form-grid">
      <label><span>实例名称</span><input name="name" value="${escapeHtml(instance.name)}" required></label>
      <label><span>服务器名称</span><input name="serverName" value="${escapeHtml(instance.server?.name)}" required></label>
      <label><span>世界名称</span><input name="world" value="${escapeHtml(instance.server?.world)}" required></label>
      <label><span>UDP 端口</span><input name="port" type="number" min="1024" max="65535" value="${instance.server?.port}"></label>
      <label><span>服务器密码</span><input name="password" value="${escapeHtml(instance.server?.password || "")}" autocomplete="new-password"></label>
      <label><span>自动保存间隔（秒）</span><input name="saveInterval" type="number" min="0" max="86400" value="${instance.server?.saveInterval ?? 1800}"></label>
      <label><span>实例 ID</span><input name="instanceId" value="${escapeHtml(instance.server?.instanceId || "")}"></label>
      <label><span>额外启动参数</span><input name="extraArgs" value="${escapeHtml(instance.server?.extraArgs || "")}" placeholder="-preset ..."></label>
      <label class="check-line"><input name="public" type="checkbox" ${instance.server?.public ? "checked" : ""}><span>公开服务器</span></label>
      <label class="check-line"><input name="crossplay" type="checkbox" ${instance.server?.crossplay ? "checked" : ""}><span>启用 Crossplay</span></label>
      <label class="check-line"><input name="bepinexEnabled" type="checkbox" ${instance.bepinex?.enabled ? "checked" : ""}><span>启动时优先使用 BepInEx</span></label>
      <label class="check-line"><input name="backupEnabled" type="checkbox" ${instance.backup?.enabled ? "checked" : ""}><span>启用自动备份策略</span></label>
      <label><span>保留备份数量</span><input name="backupKeep" type="number" min="1" max="100" value="${instance.backup?.keep || 7}"></label>
      <label><span>备份间隔（小时）</span><input name="backupIntervalHours" type="number" min="1" max="168" value="${instance.backup?.intervalHours || 24}"></label>
      <label style="grid-column:1/-1"><span>描述</span><input name="description" value="${escapeHtml(instance.description || "")}"></label>
      <div class="form-actions"><button type="submit" class="button primary">保存配置</button></div>
    </form>
  `;
}

async function saveInstanceConfig(event) {
  event.preventDefault();
  const form = new FormData(event.currentTarget);
  const body = {
    name: form.get("name"),
    description: form.get("description"),
    server: {
      name: form.get("serverName"),
      world: form.get("world"),
      port: Number(form.get("port")),
      password: form.get("password"),
      saveInterval: Number(form.get("saveInterval")),
      instanceId: form.get("instanceId"),
      extraArgs: form.get("extraArgs"),
      public: form.get("public") === "on",
      crossplay: form.get("crossplay") === "on",
    },
    bepinex: {
      enabled: form.get("bepinexEnabled") === "on",
    },
    backup: {
      enabled: form.get("backupEnabled") === "on",
      keep: Number(form.get("backupKeep")),
      intervalHours: Number(form.get("backupIntervalHours")),
    },
  };
  try {
    state.current = await api(`/instances/${encodeURIComponent(state.currentId)}/config`, { method: "PUT", body });
    toast("配置已保存");
    await refreshCurrent();
  } catch (error) {
    toast(error.message, "error");
  }
}

function renderModsMarkup() {
  return `
    <div class="mods-layout">
      <section class="panel">
        <div class="panel-head">
          <div><p class="eyebrow">THUNDERSTORE</p><h2>在线搜索</h2></div>
        </div>
        <div class="search-row">
          <input id="detail-mod-search" type="search" placeholder="模组名、作者或功能">
          <button type="button" class="button secondary" data-action="search-mods">搜索</button>
        </div>
        <div id="detail-mod-results" class="mod-results">${renderModResults()}</div>
      </section>
      <section class="panel">
        <div class="panel-head">
          <div><p class="eyebrow">INSTALLED</p><h2>已安装模组</h2></div>
          <button type="button" class="button small secondary" data-action="install-bepinex">安装 BepInEx</button>
        </div>
        <div id="detail-installed-mods" class="installed-mods">${renderInstalledMods()}</div>
      </section>
    </div>
  `;
}

function renderModResults() {
  if (!state.modPackages.length) return `<div class="empty-state">搜索 Thunderstore 上的 Valheim 模组，或者直接上传本地 ZIP。</div>`;
  return `<div class="table-head mod-table-head"><span>模组</span><span>版本</span><span>依赖</span><span>操作</span></div>` +
    state.modPackages.map((pkg) => `
    <article class="mod-card data-row">
      <div class="mod-card-main mod-item">
        <div class="mod-preview">${pkg.icon ? `<img src="${escapeHtml(pkg.icon)}" alt="">` : `<span>${escapeHtml((pkg.name || "?").slice(0, 1))}</span>`}</div>
        <div class="mod-card-head">
          <div>
            <h3>${escapeHtml(pkg.name)}</h3>
            <p>${escapeHtml(pkg.description || "")}</p>
          </div>
        </div>
        <div class="tag-row">
          <span class="tag">${escapeHtml(pkg.owner)}</span>
        </div>
      </div>
      <span class="data-cell">v${escapeHtml(pkg.version)}</span>
      <span class="data-cell">${pkg.dependencies?.length || 0}</span>
      <button type="button" class="button small secondary" data-action="install-mod" data-key="${escapeHtml(pkg.key)}">安装</button>
    </article>
  `).join("");
}

function renderRecommendedMods() {
  const root = $("#recommended-mods");
  if (!root) return;
  if (!state.recommendedMods.length) {
    root.innerHTML = `<div class="empty-state">正在读取热门模组推荐。</div>`;
    return;
  }
  root.innerHTML = state.recommendedMods.map((pkg) => {
    const installed = state.installedMods.some((mod) => mod.key === pkg.key);
    return `
      <article class="recommendation-card">
        <div class="mod-preview">${pkg.icon ? `<img src="${escapeHtml(pkg.icon)}" alt="">` : `<span>${escapeHtml((pkg.name || "?").slice(0, 1))}</span>`}</div>
        <div class="recommendation-copy">
          <strong>${escapeHtml(pkg.name)}</strong>
          <span>${escapeHtml(pkg.owner)} · v${escapeHtml(pkg.version || "latest")}</span>
          <p>${escapeHtml(pkg.description || "")}</p>
        </div>
        <button type="button" class="button small ${installed ? "ghost" : "secondary"}" data-action="install-mod" data-key="${escapeHtml(pkg.key)}" ${installed ? "disabled" : ""}>${installed ? "已安装" : "安装"}</button>
      </article>
    `;
  }).join("");
}

function renderInstalledMods() {
  if (!state.installedMods.length) return `<div class="empty-state">当前实例还没有安装模组。</div>`;
  return `<div class="table-head mod-table-head"><span>模组</span><span>版本</span><span>状态</span><span>操作</span></div>` +
    state.installedMods.map((mod) => `
    <article class="installed-mod data-row">
      <div class="installed-mod-head">
        <div>
          <h3>${escapeHtml(mod.name)}</h3>
          <p>${escapeHtml(mod.fullName)}</p>
        </div>
      </div>
      <span class="data-cell">v${escapeHtml(mod.version)}</span>
      <span class="status-pill ${mod.enabled ? "running" : ""}">${mod.enabled ? "已启用" : "已禁用"}</span>
      <div class="action-row">
        <button type="button" class="button small ${mod.enabled ? "" : "secondary"}" data-action="toggle-mod" data-key="${escapeHtml(mod.key)}" data-enabled="${mod.enabled ? "1" : "0"}">${mod.enabled ? "禁用" : "启用"}</button>
        <button type="button" class="button small ghost" data-action="edit-mod-config" data-key="${escapeHtml(mod.key)}">配置</button>
        <button type="button" class="button small danger" data-action="delete-mod" data-key="${escapeHtml(mod.key)}">删除</button>
      </div>
    </article>
  `).join("");
}

function renderMods() {
  $("#mod-search-results").innerHTML = renderModResults();
  $("#installed-mods").innerHTML = renderInstalledMods();
}

function renderBackupsMarkup() {
  return `
    <div class="section-toolbar" style="margin-bottom:12px">
      <div><p class="eyebrow">WORLD SNAPSHOTS</p><h2>备份</h2></div>
      <button type="button" class="button primary" data-action="backup">立即备份</button>
    </div>
    <div id="detail-backup-list" class="backup-list">${renderBackupsList()}</div>
  `;
}

function renderBackupsList() {
  if (!state.backups.length) return `<div class="empty-state">当前实例还没有备份。</div>`;
  return `<div class="table-head backup-table-head"><span>备份文件</span><span>创建时间</span><span>大小</span><span>操作</span></div>` +
    state.backups.map((backup) => `
    <div class="backup-row data-row">
      <div>
        <strong>${escapeHtml(backup.name)}</strong>
      </div>
      <span class="data-cell">${formatDate(backup.createdAt)}</span>
      <span class="data-cell">${escapeHtml(backup.sizeText || "-")}</span>
      <div class="action-row">
        <button type="button" class="button small secondary" data-action="restore-backup" data-name="${escapeHtml(backup.name)}">恢复</button>
        <button type="button" class="button small ghost" data-action="download-backup" data-name="${escapeHtml(backup.name)}">下载</button>
        <button type="button" class="button small danger" data-action="delete-backup" data-name="${escapeHtml(backup.name)}">删除</button>
      </div>
    </div>
  `).join("");
}

function renderBackups() {
  $("#backup-list").innerHTML = renderBackupsList();
}

function renderLogsMarkup() {
  const value = state.logs[state.logTab] || "暂无日志。";
  return `
    <div class="log-tabs">
      <button type="button" class="tab ${state.logTab === "server" ? "active" : ""}" data-log-tab="server">Server</button>
      <button type="button" class="tab ${state.logTab === "steam" ? "active" : ""}" data-log-tab="steam">SteamCMD</button>
    </div>
    <pre id="detail-log-output" class="log-output">${escapeHtml(value)}</pre>
  `;
}

function renderLogs() {
  const value = state.logs[state.logTab] || "暂无日志。";
  if ($("#log-output")) $("#log-output").textContent = value;
}

function renderSystemInfo(system) {
  const root = $("#system-info");
  if (!root) return;
  root.innerHTML = [
    ["系统", `${system.platform} / ${system.arch}`],
    ["后端", system.node],
    ["数据目录", system.dataDir],
    ["SteamCMD", system.steamcmd || "未发现"],
    ["工作目录", system.cwd],
  ].map(([key, value]) => `<dt>${escapeHtml(key)}</dt><dd>${escapeHtml(value)}</dd>`).join("");
  const status = $("#steamcmd-state");
  const path = $("#steamcmd-path");
  if (status) status.textContent = system.steamcmd ? "已安装" : "未安装";
  if (path) path.textContent = system.steamcmd || "尚未检测到 SteamCMD";
}

function renderSettingsForm() {
  const form = $("#settings-form");
  if (!form) return;
  for (const [key, value] of Object.entries(state.settings || {})) {
    const field = form.elements.namedItem(key);
    if (!field) continue;
    if (field.type === "checkbox") field.checked = Boolean(value);
    else field.value = value ?? "";
  }
}

async function loadSectionData(section = state.section) {
  if (section === "overview") await loadOverview();
  if (section === "instances") await loadInstances();
  if (section === "mods" || section === "backups" || section === "logs") {
    if (state.currentId && !state.current) await loadCurrent();
  }
  if (section === "mods" && state.currentId && !state.modPackages.length) {
    await searchMods("");
  }
  if (section === "mods" && !state.recommendedMods.length) {
    const recommended = await api("/mods/recommended");
    state.recommendedMods = recommended.packages || [];
  }
  if (section === "backups" && state.currentId) {
    state.backups = await api(`/instances/${encodeURIComponent(state.currentId)}/backups`);
  }
  if (section === "logs" && state.currentId) {
    state.logs = await api(`/instances/${encodeURIComponent(state.currentId)}/logs?lines=${$("#log-lines")?.value || 300}`);
  }
}

async function setSection(section, skipLoad = false) {
  closeModal();
  state.section = section;
  if (location.hash !== `#/${section}`) {
    history.replaceState(null, "", `#/${section}`);
  }
  $$("#main-nav .nav-item").forEach((item) => item.classList.toggle("active", item.dataset.section === section));
  $$(".section-panel").forEach((panel) => panel.hidden = panel.id !== `section-${section}`);
  const titleMap = {
    overview: ["服务器总览", "VIKING SERVER / OVERVIEW"],
    instances: ["控制面板", "INSTANCES / CONTROL"],
    mods: ["模组管理", "BEPINEX / THUNDERSTORE"],
    backups: ["存档管理", "WORLD SNAPSHOTS"],
    logs: ["日志管理", "RUNTIME OUTPUT"],
    settings: ["平台管理", "PLATFORM SETTINGS"],
  };
  $("#page-title").textContent = titleMap[section][0];
  $("#page-eyebrow").textContent = titleMap[section][1];
  if (!skipLoad) {
    try {
      await loadSectionData(section);
      renderAll();
    } catch (error) {
      toast(error.message, "error");
    }
  }
}

async function selectInstance(id) {
  state.currentId = id;
  localStorage.setItem("valheim_panel_instance", id);
  try {
    await loadCurrent();
    await loadSectionData();
    renderAll();
  } catch (error) {
    toast(error.message, "error");
  }
}

async function searchMods(query) {
  const input = state.section === "mods" ? $("#mod-search-input") : $("#detail-mod-search");
  const value = query ?? input?.value ?? "";
  const result = await api(`/mods/search?q=${encodeURIComponent(value)}&pageSize=40`);
  state.modPackages = result.packages || [];
  renderMods();
  if ($("#detail-mod-results")) $("#detail-mod-results").innerHTML = renderModResults();
}

async function watchTask(task, label) {
  if (!task?.id) return;
  toast(`${label}任务已开始`);
  const timer = setInterval(async () => {
    try {
      const current = await api(`/tasks/${encodeURIComponent(task.id)}`);
      if (current.status === "running") return;
      clearInterval(timer);
      if (current.status === "success") {
        toast(`${label}完成`);
        await loadSettings();
        await refreshCurrent();
      } else {
        toast(`${label}失败：${current.error || "未知错误"}`, "error");
      }
    } catch (error) {
      clearInterval(timer);
      toast(error.message, "error");
    }
  }, 1200);
}

async function installSteamcmd() {
  try {
    const task = await api("/platform/steamcmd/install", { method: "POST" });
    await watchTask(task, "SteamCMD 安装");
  } catch (error) {
    toast(error.message, "error");
  }
}

async function runInstanceAction(action, id = state.currentId) {
  if (!id) return toast("请先选择实例", "error");
  try {
    const result = await api(`/instances/${encodeURIComponent(id)}/action`, { method: "POST", body: { action } });
    if (result?.id && result?.type) {
      await watchTask(result, action === "update" ? "更新服务器" : "安装服务器");
      return;
    }
    toast(action === "start" ? "实例已启动" : action === "stop" ? "实例已停止" : "实例已重启");
    await refreshCurrent();
  } catch (error) {
    toast(error.message, "error");
  }
}

async function installBepInEx() {
  if (!state.currentId) return toast("请先选择实例", "error");
  try {
    const task = await api(`/instances/${encodeURIComponent(state.currentId)}/bepinex`, { method: "POST" });
    await watchTask(task, "BepInEx 安装");
  } catch (error) {
    toast(error.message, "error");
  }
}

async function installMod(key) {
  if (!state.currentId) return toast("请先选择实例", "error");
  const pkg = state.modPackages.find((item) => item.key === key);
  try {
    const task = await api(`/instances/${encodeURIComponent(state.currentId)}/mods/install`, {
      method: "POST",
      body: { key },
    });
    await watchTask(task, `安装 ${pkg?.name || key}`);
  } catch (error) {
    toast(error.message, "error");
  }
}

async function toggleMod(key, enabled) {
  try {
    await api(`/instances/${encodeURIComponent(state.currentId)}/mods/${encodeURIComponent(key)}/toggle`, {
      method: "POST",
      body: { enabled: !enabled },
    });
    toast(enabled ? "模组已禁用" : "模组已启用");
    await loadCurrent();
    renderAll();
  } catch (error) {
    toast(error.message, "error");
  }
}

async function deleteMod(key) {
  if (!confirm("确认删除这个模组及其已跟踪文件？")) return;
  try {
    await api(`/instances/${encodeURIComponent(state.currentId)}/mods/${encodeURIComponent(key)}`, { method: "DELETE" });
    await loadCurrent();
    renderAll();
    toast("模组已删除");
  } catch (error) {
    toast(error.message, "error");
  }
}

async function editModConfig(key) {
  try {
    const config = await api(`/instances/${encodeURIComponent(state.currentId)}/mods/${encodeURIComponent(key)}/config`);
    openModal(`模组配置 · ${key}`, `
      <div class="form-stack">
        ${config.files.length ? config.files.map((file, index) => `
          <label>
            <span>${escapeHtml(file.path)}</span>
            <textarea data-config-path="${escapeHtml(file.path)}" data-config-index="${index}" rows="16">${escapeHtml(file.content)}</textarea>
          </label>
        `).join("") : `<div class="empty-state">该模组没有可编辑的 BepInEx 配置。</div>`}
      </div>
      <div class="modal-actions">
        <button type="button" class="button ghost" data-action="close-modal">取消</button>
        <button type="button" class="button primary" data-action="save-mod-config" data-key="${escapeHtml(key)}">保存</button>
      </div>
    `);
  } catch (error) {
    toast(error.message, "error");
  }
}

async function saveModConfig(key) {
  const textareas = $$("[data-config-path]", $("#modal-root"));
  try {
    for (const textarea of textareas) {
      await api(`/instances/${encodeURIComponent(state.currentId)}/mods/${encodeURIComponent(key)}/config`, {
        method: "PUT",
        body: { path: textarea.dataset.configPath, content: textarea.value },
      });
    }
    closeModal();
    toast("模组配置已保存");
  } catch (error) {
    toast(error.message, "error");
  }
}

async function createBackup() {
  if (!state.currentId) return toast("请先选择实例", "error");
  try {
    await api(`/instances/${encodeURIComponent(state.currentId)}/backups`, { method: "POST", body: {} });
    state.backups = await api(`/instances/${encodeURIComponent(state.currentId)}/backups`);
    renderBackups();
    if ($("#detail-backup-list")) $("#detail-backup-list").innerHTML = renderBackupsList();
    toast("备份已创建");
  } catch (error) {
    toast(error.message, "error");
  }
}

async function restoreBackup(name) {
  if (!confirm(`确认恢复备份 ${name}？实例会先停止，现有世界文件会被覆盖。`)) return;
  try {
    await api(`/instances/${encodeURIComponent(state.currentId)}/backups/${encodeURIComponent(name)}/restore`, { method: "POST" });
    await refreshCurrent();
    toast("备份已恢复");
  } catch (error) {
    toast(error.message, "error");
  }
}

async function deleteBackup(name) {
  if (!confirm(`确认删除备份 ${name}？`)) return;
  try {
    await api(`/instances/${encodeURIComponent(state.currentId)}/backups/${encodeURIComponent(name)}`, { method: "DELETE" });
    state.backups = await api(`/instances/${encodeURIComponent(state.currentId)}/backups`);
    renderBackups();
    if ($("#detail-backup-list")) $("#detail-backup-list").innerHTML = renderBackupsList();
    toast("备份已删除");
  } catch (error) {
    toast(error.message, "error");
  }
}

async function downloadBackup(name) {
  try {
    const response = await fetch(`/api/instances/${encodeURIComponent(state.currentId)}/backups/${encodeURIComponent(name)}/download`, {
      headers: { authorization: `Bearer ${state.token}` },
    });
    if (!response.ok) throw new Error("下载失败");
    const blob = await response.blob();
    const url = URL.createObjectURL(blob);
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = name;
    anchor.click();
    URL.revokeObjectURL(url);
  } catch (error) {
    toast(error.message, "error");
  }
}

function openModal(title, body) {
  const root = $("#modal-root");
  root.hidden = false;
  root.innerHTML = `
    <section class="modal">
      <div class="modal-head">
        <h2>${escapeHtml(title)}</h2>
        <button type="button" class="button small ghost" data-action="close-modal">关闭</button>
      </div>
      <div class="modal-body">${body}</div>
    </section>
  `;
}

function closeModal() {
  const root = $("#modal-root");
  root.hidden = true;
  root.innerHTML = "";
}

function openInstanceModal(instance = null) {
  const editing = Boolean(instance);
  openModal(editing ? "编辑实例" : "新建 Valheim 实例", `
    <form id="instance-editor" class="form-grid">
      <label><span>实例名称</span><input name="name" value="${escapeHtml(instance?.name || "")}" required></label>
      <label><span>服务器名称</span><input name="serverName" value="${escapeHtml(instance?.server?.name || "")}" required></label>
      <label><span>世界名称</span><input name="world" value="${escapeHtml(instance?.server?.world || "Dedicated")}" required></label>
      <label><span>UDP 端口</span><input name="port" type="number" min="1024" max="65535" value="${instance?.server?.port || state.settings.defaultPort || 2456}"></label>
      <label><span>服务器密码</span><input name="password" value="${escapeHtml(instance?.server?.password || "")}" placeholder="至少 5 个字符"></label>
      <label><span>自动保存间隔（秒）</span><input name="saveInterval" type="number" min="0" max="86400" value="${instance?.server?.saveInterval ?? 1800}"></label>
      <label class="check-line"><input name="public" type="checkbox" ${instance?.server?.public !== false ? "checked" : ""}><span>公开服务器</span></label>
      <label class="check-line"><input name="crossplay" type="checkbox" ${instance?.server?.crossplay ? "checked" : ""}><span>启用 Crossplay</span></label>
      <label style="grid-column:1/-1"><span>描述</span><input name="description" value="${escapeHtml(instance?.description || "")}"></label>
      <div class="modal-actions" style="grid-column:1/-1">
        <button type="button" class="button ghost" data-action="close-modal">取消</button>
        <button type="submit" class="button primary">${editing ? "保存" : "创建实例"}</button>
      </div>
    </form>
  `);
  $("#instance-editor").addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const body = {
      name: form.get("name"),
      description: form.get("description"),
      server: {
        name: form.get("serverName"),
        world: form.get("world"),
        port: Number(form.get("port")),
        password: form.get("password"),
        saveInterval: Number(form.get("saveInterval")),
        public: form.get("public") === "on",
        crossplay: form.get("crossplay") === "on",
      },
    };
    try {
      const saved = editing
        ? await api(`/instances/${encodeURIComponent(instance.id)}`, { method: "PUT", body })
        : await api("/instances", { method: "POST", body });
      state.currentId = saved.id;
      state.current = saved;
      localStorage.setItem("valheim_panel_instance", saved.id);
      closeModal();
      renderAll();
      await refreshCurrent();
      toast(editing ? "实例已更新" : "实例已创建");
    } catch (error) {
      toast(error.message, "error");
    }
  });
}

async function deleteInstance(id) {
  const instance = state.instances.find((item) => item.id === id);
  if (!confirm(`确认删除实例 ${instance?.name || id}？实例目录会保留，除非你执行彻底清理。`)) return;
  try {
    await api(`/instances/${encodeURIComponent(id)}?purge=0`, { method: "DELETE" });
    if (state.currentId === id) state.currentId = "";
    await refreshCurrent();
    toast("实例已移除");
  } catch (error) {
    toast(error.message, "error");
  }
}

function bindGlobalEvents() {
  $("#login-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const errorNode = $("#login-error");
    errorNode.hidden = true;
    try {
      const payload = await api("/auth/login", {
        method: "POST",
        body: {
          username: $("#login-username").value,
          password: $("#login-password").value,
        },
      });
      state.token = payload.token;
      state.user = payload.user;
      state.settings = payload.settings;
      localStorage.setItem("valheim_panel_token", state.token);
      $("#sidebar-user").textContent = state.user.username;
      showApp();
      await loadAll();
    } catch (error) {
      errorNode.textContent = error.message;
      errorNode.hidden = false;
    }
  });

  $("#logout-button").addEventListener("click", () => logout());
  $("#refresh-button").addEventListener("click", async () => {
    try {
      await refreshCurrent();
      toast("数据已刷新");
    } catch (error) {
      toast(error.message, "error");
    }
  });
  $("#new-instance-button").addEventListener("click", () => openInstanceModal());
  $("#new-instance-button-2").addEventListener("click", () => openInstanceModal());
  $("#instance-picker").addEventListener("change", (event) => selectInstance(event.target.value));
  $("#mod-search-button").addEventListener("click", () => searchMods().catch((error) => toast(error.message, "error")));
  $("#mod-search-input").addEventListener("keydown", (event) => {
    if (event.key === "Enter") searchMods().catch((error) => toast(error.message, "error"));
  });
  $("#install-bepinex-button").addEventListener("click", installBepInEx);
  $("#upload-mod-button").addEventListener("click", () => $("#hidden-upload").click());
  $("#hidden-upload").addEventListener("change", async (event) => {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file || !state.currentId) return;
    try {
      const task = await api(`/instances/${encodeURIComponent(state.currentId)}/mods/upload?filename=${encodeURIComponent(file.name)}`, {
        method: "POST",
        rawBody: file,
      });
      await watchTask(task, "上传模组");
    } catch (error) {
      toast(error.message, "error");
    }
  });
  $("#create-backup-button").addEventListener("click", createBackup);
  $("#log-refresh-button").addEventListener("click", refreshLogs);
  $("#log-lines").addEventListener("change", refreshLogs);
  $("#settings-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const body = {
      steamcmdPath: form.get("steamcmdPath"),
      steamAppId: form.get("steamAppId"),
      installRoot: form.get("installRoot"),
      thunderstoreCommunity: form.get("thunderstoreCommunity"),
      defaultPort: Number(form.get("defaultPort")),
      defaultMaxPlayers: Number(form.get("defaultMaxPlayers")),
      autoInstallBepInEx: form.get("autoInstallBepInEx") === "on",
      autoUpdateMods: form.get("autoUpdateMods") === "on",
    };
    try {
      state.settings = await api("/settings", { method: "PUT", body });
      toast("面板设置已保存");
      await loadOverview();
      renderOverview();
    } catch (error) {
      toast(error.message, "error");
    }
  });
  $("#password-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const formElement = event.currentTarget;
    const form = new FormData(formElement);
    try {
      await api("/auth/password", {
        method: "PUT",
        body: {
          currentPassword: form.get("currentPassword"),
          newPassword: form.get("newPassword"),
        },
      });
      formElement.reset();
      toast("密码已修改");
    } catch (error) {
      toast(error.message, "error");
    }
  });
  $("#install-steamcmd-button").addEventListener("click", installSteamcmd);

  document.addEventListener("click", async (event) => {
    const nav = event.target.closest(".nav-item");
    if (nav) {
      await setSection(nav.dataset.section);
      return;
    }
    const detailTab = event.target.closest("[data-detail-tab]");
    if (detailTab) {
      state.detailTab = detailTab.dataset.detailTab;
      if (state.detailTab === "backups") state.backups = await api(`/instances/${encodeURIComponent(state.currentId)}/backups`);
      if (state.detailTab === "logs") state.logs = await api(`/instances/${encodeURIComponent(state.currentId)}/logs?lines=300`);
      renderDetail();
      return;
    }
    const logTab = event.target.closest("[data-log-tab]");
    if (logTab) {
      state.logTab = logTab.dataset.logTab;
      renderLogs();
      if ($("#detail-content") && state.detailTab === "logs") renderDetail();
      return;
    }
    const target = event.target.closest("[data-action]");
    if (!target) return;
    const action = target.dataset.action;
    const id = target.dataset.id || state.currentId;
    if (action === "open-create") openInstanceModal();
    if (action === "select-instance") await selectInstance(id);
    if (action === "edit-instance") openInstanceModal(state.instances.find((instance) => instance.id === id));
    if (action === "delete-instance") await deleteInstance(id);
    if (["start", "stop", "restart"].includes(action)) await runInstanceAction(action, id);
    if (action === "install" || action === "update") {
      try {
        const task = await api(`/instances/${encodeURIComponent(id)}/install`, { method: "POST" });
        await watchTask(task, action === "update" ? "更新服务器" : "安装服务器");
      } catch (error) {
        toast(error.message, "error");
      }
    }
    if (action === "install-bepinex") await installBepInEx();
    if (action === "search-mods") await searchMods($("#detail-mod-search")?.value || $("#mod-search-input")?.value || "");
    if (action === "install-mod") await installMod(target.dataset.key);
    if (action === "toggle-mod") await toggleMod(target.dataset.key, target.dataset.enabled === "1");
    if (action === "delete-mod") await deleteMod(target.dataset.key);
    if (action === "edit-mod-config") await editModConfig(target.dataset.key);
    if (action === "save-mod-config") await saveModConfig(target.dataset.key);
    if (action === "backup") await createBackup();
    if (action === "restore-backup") await restoreBackup(target.dataset.name);
    if (action === "delete-backup") await deleteBackup(target.dataset.name);
    if (action === "download-backup") await downloadBackup(target.dataset.name);
    if (action === "close-modal") closeModal();
  });

  window.addEventListener("hashchange", () => {
    const section = sectionFromHash();
    if (section !== state.section) setSection(section).catch((error) => toast(error.message, "error"));
  });

  setInterval(async () => {
    if (!state.token) return;
    try {
      await loadInstances();
      renderInstancePicker();
      renderOverview();
      renderInstanceCards();
      if (state.section === "logs" && state.currentId) await refreshLogs(true);
      if (state.detailTab === "logs" && state.current) await refreshLogs(true);
    } catch {
      // Silent background refresh.
    }
  }, 6000);
}

function sectionFromHash() {
  const value = location.hash.replace(/^#\/?/, "");
  return ["overview", "instances", "mods", "backups", "logs", "settings"].includes(value) ? value : "overview";
}

async function refreshLogs(silent = false) {
  if (!state.currentId) return;
  try {
    state.logs = await api(`/instances/${encodeURIComponent(state.currentId)}/logs?lines=${$("#log-lines")?.value || 300}`);
    renderLogs();
    if ($("#detail-log-output")) $("#detail-log-output").textContent = state.logs[state.logTab] || "暂无日志。";
  } catch (error) {
    if (!silent) toast(error.message, "error");
  }
}

boot();
