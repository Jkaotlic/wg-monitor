(function () {
  const state = {
    summary: null,
    filter: "all",
    query: "",
    selected: null,
    deployAgent: null,
    editAgent: null,
    autoRefresh: true,
    lastUpdatedAt: 0,
    buttonStates: new Map(),
    opkgCron: new Map(),
    entwareClean: new Map(),
    versions: new Map(),
    agentConfig: new Map()
  };

  const els = {
    addAgentBtn: document.getElementById("addAgentBtn"),
    refreshBtn: document.getElementById("refreshBtn"),
    logoutBtn: document.getElementById("logoutBtn"),
    authDot: document.getElementById("authDot"),
    authState: document.getElementById("authState"),
    summaryText: document.getElementById("summaryText"),
    kpiAgents: document.getElementById("kpiAgents"),
    kpiOnline: document.getElementById("kpiOnline"),
    kpiAlerts: document.getElementById("kpiAlerts"),
    kpiDeploys: document.getElementById("kpiDeploys"),
    kpiVersion: document.getElementById("kpiVersion"),
    kpiOnlineText: document.getElementById("kpiOnlineText"),
    kpiAlertsText: document.getElementById("kpiAlertsText"),
    kpiDeploysText: document.getElementById("kpiDeploysText"),
    agentsBody: document.getElementById("agentsBody"),
    searchInput: document.getElementById("searchInput"),
    agentDrawer: document.getElementById("agentDrawer"),
    deployModal: document.getElementById("deployModal"),
    deployTitle: document.getElementById("deployTitle"),
    deployVersionInput: document.getElementById("deployVersionInput"),
    confirmDeployBtn: document.getElementById("confirmDeployBtn"),
    addAgentModal: document.getElementById("addAgentModal"),
    newAgentNickname: document.getElementById("newAgentNickname"),
    newAgentKind: document.getElementById("newAgentKind"),
    newAgentGroup: document.getElementById("newAgentGroup"),
    customGroupField: document.getElementById("customGroupField"),
    newAgentCustomGroup: document.getElementById("newAgentCustomGroup"),
    newAgentThread: document.getElementById("newAgentThread"),
    newAgentDeployMode: document.getElementById("newAgentDeployMode"),
    newAgentArch: document.getElementById("newAgentArch"),
    newAgentRing: document.getElementById("newAgentRing"),
    newAgentAWGMURL: document.getElementById("newAgentAWGMURL"),
    newAgentAWGMAuth: document.getElementById("newAgentAWGMAuth"),
    newAgentSSHHost: document.getElementById("newAgentSSHHost"),
    newAgentSSHPort: document.getElementById("newAgentSSHPort"),
    newAgentSSHUser: document.getElementById("newAgentSSHUser"),
    newAgentExpectedMAC: document.getElementById("newAgentExpectedMAC"),
    createAgentBtn: document.getElementById("createAgentBtn"),
    addAgentError: document.getElementById("addAgentError"),
    resultDrawer: document.getElementById("resultDrawer"),
    drawerTitle: document.getElementById("drawerTitle"),
    resultOutput: document.getElementById("resultOutput"),
    closeDrawerBtn: document.getElementById("closeDrawerBtn"),
    toast: document.getElementById("toast"),
    backendUpdateBtn: document.getElementById("backendUpdateBtn"),
    backendUpdateText: document.getElementById("backendUpdateText"),
    autoRefreshBtn: document.getElementById("autoRefreshBtn"),
    autoRefreshText: document.getElementById("autoRefreshText"),
    freshness: document.getElementById("freshness"),
    freshnessDot: document.getElementById("freshnessDot"),
    editAgentModal: document.getElementById("editAgentModal"),
    editAgentTitle: document.getElementById("editAgentTitle"),
    editAgentAWGMURL: document.getElementById("editAgentAWGMURL"),
    editAgentAWGMAuth: document.getElementById("editAgentAWGMAuth"),
    editAgentSSHHost: document.getElementById("editAgentSSHHost"),
    editAgentSSHPort: document.getElementById("editAgentSSHPort"),
    editAgentSSHUser: document.getElementById("editAgentSSHUser"),
    editAgentDeployMode: document.getElementById("editAgentDeployMode"),
    editAgentArch: document.getElementById("editAgentArch"),
    editAgentRing: document.getElementById("editAgentRing"),
    editAgentExpectedMAC: document.getElementById("editAgentExpectedMAC"),
    editAgentError: document.getElementById("editAgentError"),
    saveAgentBtn: document.getElementById("saveAgentBtn"),
    agentConfigModal: document.getElementById("agentConfigModal"),
    agentConfigTitle: document.getElementById("agentConfigTitle"),
    cfgIntervalSec: document.getElementById("cfgIntervalSec"),
    cfgExternalReachFail: document.getElementById("cfgExternalReachFail"),
    cfgExternalReachEnabled: document.getElementById("cfgExternalReachEnabled"),
    cfgAwgmBaseURL: document.getElementById("cfgAwgmBaseURL"),
    cfgAwgmLogin: document.getElementById("cfgAwgmLogin"),
    cfgAllowReboot: document.getElementById("cfgAllowReboot"),
    cfgAllowFirmware: document.getElementById("cfgAllowFirmware"),
    agentConfigError: document.getElementById("agentConfigError"),
    applyAgentConfigBtn: document.getElementById("applyAgentConfigBtn")
  };

  const AUTO_REFRESH_MS = 20000;

  function setAuth(ok) {
    els.authDot.classList.toggle("ok", ok);
    els.authState.textContent = ok ? "unlocked" : "locked";
  }

  function setButtonState(button, value) {
    if (!button) return;
    button.dataset.state = value || "idle";
    button.disabled = value === "queued" || value === "waiting";
  }

  function isButtonBusy(button) {
    if (!button) return false;
    return button.disabled || button.dataset.state === "queued" || button.dataset.state === "waiting";
  }

  function setActionState(nickname, action, value) {
    state.buttonStates.set(nickname + ":" + action, value);
    let selector = `[data-agent="${cssEscape(nickname)}"][data-command="${cssEscape(action)}"], [data-agent="${cssEscape(nickname)}"][data-maint="${cssEscape(action)}"]`;
    if (action === "self_update") {
      selector += `, [data-agent="${cssEscape(nickname)}"][data-update-latest]`;
    }
    document.querySelectorAll(selector).forEach((button) => {
      setButtonState(button, value);
    });
  }

  function buttonState(nickname, action) {
    return state.buttonStates.get(nickname + ":" + action) || "idle";
  }

  function toast(text) {
    els.toast.textContent = text;
    els.toast.classList.remove("hidden");
    window.clearTimeout(toast.timer);
    toast.timer = window.setTimeout(() => els.toast.classList.add("hidden"), 3200);
  }

  async function api(path, options) {
    const init = Object.assign({ headers: {} }, options || {});
    init.headers = Object.assign({}, init.headers);
    init.credentials = "same-origin";
    const res = await fetch(path, init);
    const ct = res.headers.get("content-type") || "";
    const body = ct.includes("application/json") ? await res.json() : await res.text();
    if (!res.ok) {
      const message = body && body.message ? body.message : String(body || res.statusText);
      if (res.status === 401) {
        window.location.href = "/dashboard/login";
        return null;
      }
      const error = new Error(message);
      error.status = res.status;
      error.code = body && body.code;
      throw error;
    }
    return body;
  }

  async function refresh() {
    setButtonState(els.refreshBtn, "waiting");
    try {
      const summary = await api("/v1/dashboard/summary");
      state.summary = summary;
      state.lastUpdatedAt = Date.now();
      setAuth(true);
      setButtonState(els.refreshBtn, "ok");
      render();
      updateFreshness();
      window.setTimeout(() => setButtonState(els.refreshBtn, "idle"), 700);
    } catch (err) {
      setAuth(false);
      setButtonState(els.refreshBtn, "error");
      renderError(err.message);
    }
  }

  // overlayOpen is true while a modal or the result drawer is showing — used to
  // pause auto-refresh so it never yanks the UI out from under an open form.
  function overlayOpen() {
    return Boolean(document.querySelector(".modal:not(.hidden)")) ||
      !els.resultDrawer.classList.contains("hidden");
  }

  function setAutoRefresh(on) {
    state.autoRefresh = on;
    if (els.autoRefreshBtn) {
      els.autoRefreshBtn.setAttribute("aria-pressed", on ? "true" : "false");
      els.autoRefreshBtn.classList.toggle("btn-ghost", !on);
      els.autoRefreshBtn.classList.toggle("btn-secondary", on);
      const icon = els.autoRefreshBtn.querySelector(".ti");
      if (icon) icon.className = "ti " + (on ? "ti-player-pause" : "ti-player-play");
    }
    if (els.autoRefreshText) els.autoRefreshText.textContent = on ? "Auto" : "Manual";
  }

  function updateFreshness() {
    if (!els.freshness) return;
    if (!state.lastUpdatedAt) {
      els.freshness.textContent = "никогда";
      if (els.freshnessDot) els.freshnessDot.classList.remove("ok", "warn");
      return;
    }
    const sec = Math.max(0, Math.round((Date.now() - state.lastUpdatedAt) / 1000));
    let text;
    if (sec < 5) text = "обновлено только что";
    else if (sec < 90) text = `обновлено ${sec}с назад`;
    else text = `обновлено ${Math.round(sec / 60)}м назад`;
    els.freshness.textContent = state.autoRefresh ? text : text + " · auto off";
    if (els.freshnessDot) {
      els.freshnessDot.classList.toggle("ok", sec < 60);
      els.freshnessDot.classList.toggle("warn", sec >= 60);
    }
  }

  function autoRefreshTick() {
    if (state.autoRefresh && !overlayOpen() && !isButtonBusy(els.refreshBtn)) {
      refresh();
    }
  }

  function render() {
    const summary = state.summary;
    if (!summary) {
      renderEmpty("No data");
      return;
    }
    const totals = summary.totals || {};
    els.kpiAgents.textContent = totals.agents || 0;
    els.kpiOnline.textContent = totals.online || 0;
    els.kpiAlerts.textContent = totals.alerts || 0;
    els.kpiDeploys.textContent = totals.pending_deploys || 0;
    const latest = latestVersion();
    els.kpiVersion.textContent = latest && latest !== summary.version ? `${summary.version || "backend ?"} -> ${latest}` : (summary.version || "version unknown");
    const needsUpdate = latest && summary.version && latest !== summary.version;
    if (els.backendUpdateBtn) {
      els.backendUpdateBtn.classList.toggle("hidden", !needsUpdate);
      if (needsUpdate && els.backendUpdateText) els.backendUpdateText.textContent = `Update → ${latest}`;
    }
    const sleeping = totals.sleeping || 0;
    const reporting = (totals.online || 0) === 1 ? "1 agent reporting" : (totals.online || 0) + " agents reporting";
    els.kpiOnlineText.textContent = sleeping ? `${reporting}, ${sleeping} sleeping` : reporting;
    els.kpiAlertsText.textContent = (totals.alerts || 0) ? "требуют внимания" : "hard-инцидентов нет";
    els.kpiDeploysText.textContent = (totals.pending_deploys || 0) ? "ждут подтверждения heartbeat" : "очередь deploy пустая";
    els.summaryText.textContent = summarySentence(summary);
    renderGroupOptions();

    const agents = visibleAgents(summary);
    if (!agents.length) {
      renderEmpty("No matching agents");
      renderSelectedDrawer();
      return;
    }
    els.agentsBody.innerHTML = agents.map(agentRow).join("");
    renderSelectedDrawer();
  }

  function visibleAgents(summary) {
    return (summary.agents || []).filter((agent) => {
      const query = state.query.toLowerCase();
      const searchable = [
        agent.nickname,
        agent.kind,
        agent.agent_version,
        agent.expected_exit_ip,
        agent.awg_iface,
        agent.awgm_url,
        groupLabel(agent.telegram_chat_id)
      ].join(" ").toLowerCase();
      const matchesQuery = !query || searchable.includes(query);
      const matchesFilter = state.filter === "all" || agent.status === state.filter;
      return matchesQuery && matchesFilter;
    });
  }

  function latestVersion() {
    return state.summary?.latest_version || state.summary?.version || "";
  }

  function summarySentence(summary) {
    const totals = summary.totals || {};
    if (totals.alerts > 0) return `Есть ${totals.alerts} hard-инцидент(ов). Открой агента и начни с Diagnostics или Force recheck.`;
    if (totals.pending_deploys > 0) return `${totals.pending_deploys} deploy ожидает подтверждения от heartbeat.`;
    if ((totals.sleeping || 0) > 0) return `${totals.sleeping} мобильн. агент(ов) sleeping; они не online до свежего отчета.`;
    if ((totals.offline || 0) > 0) return `${totals.offline} агент(ов) offline или еще ни разу не отчитались.`;
    return "Флот выглядит спокойно: активных hard-инцидентов нет.";
  }

  function renderEmpty(text) {
    els.agentsBody.innerHTML = `<tr><td colspan="7" class="empty-cell">${escapeHTML(text)}</td></tr>`;
  }

  function renderError(text) {
    els.kpiAgents.textContent = "-";
    els.kpiOnline.textContent = "-";
    els.kpiAlerts.textContent = "-";
    els.kpiDeploys.textContent = "-";
    els.kpiVersion.textContent = "version unknown";
    els.summaryText.textContent = text || "Request failed";
    if (!state.summary) renderEmpty(text || "Request failed");
  }

  function agentRow(agent) {
    const incidents = (agent.active_incidents || []).map((incident) => {
      return `<span class="badge badge-danger">${escapeHTML(incident.check_name)} ${incident.fail_count || ""}</span>`;
    }).join("");
    const pending = agent.pending_version
      ? `<span class="badge badge-warning">${escapeHTML(agent.pending_version)}</span>`
      : `<span class="badge badge-muted">idle</span>`;
    const rowClass = agent.pending_version && agent.status !== "alert" ? "status-pending" : "status-" + (agent.status || "offline");
    const awg = agent.awgm_url
      ? `<span class="badge badge-info">URL saved</span><div class="cell-note">${escapeHTML(shortURL(agent.awgm_url))}</div>`
      : `<span class="badge badge-muted">no URL</span><div class="cell-note">быстрый вход недоступен</div>`;
    return `
      <tr class="${rowClass}" data-row-agent="${escapeAttr(agent.nickname)}">
        <td class="agent-cell">
          <div class="agent-main">
            <span class="agent-name">${escapeHTML(agent.nickname)}</span>
            <span class="agent-meta">${escapeHTML(agent.kind || "static")} / ${escapeHTML(agent.awg_iface || "-")} / ${escapeHTML(agent.expected_exit_ip || "-")}</span>
          </div>
        </td>
        <td>${statusBadge(agent)}<div class="cell-note">${escapeHTML(statusText(agent))}</div></td>
        <td><span class="badge badge-info">${escapeHTML(agent.agent_version || "-")}</span><div class="cell-note">${pending}</div></td>
        <td><span class="badge ${agent.has_topic ? "badge-success" : "badge-muted"}">${escapeHTML(groupLabel(agent.telegram_chat_id))}</span><div class="cell-note">${escapeHTML(topicLabel(agent.telegram_thread_id))}</div></td>
        <td>${awg}</td>
        <td><div class="incident-list">${incidents || '<span class="badge badge-success">clear</span>'}</div></td>
        <td>
          <div class="action-strip">
            <button class="mini-btn" type="button" title="Run diagnostics" data-state="${buttonState(agent.nickname, "diag_now")}" data-command="diag_now" data-agent="${escapeAttr(agent.nickname)}">Diagnostics</button>
            <button class="mini-btn" type="button" title="${escapeAttr(recheckTitle(agent))}" data-state="${buttonState(agent.nickname, "force_recheck")}" data-command="force_recheck" data-agent="${escapeAttr(agent.nickname)}">${escapeHTML(recheckShortLabel(agent))}</button>
            <button class="mini-btn" type="button" title="Show route status" data-state="${buttonState(agent.nickname, "route_status")}" data-command="route_status" data-agent="${escapeAttr(agent.nickname)}">Routes</button>
            <button class="mini-btn" type="button" title="Show tunnel status" data-state="${buttonState(agent.nickname, "tunnels_status")}" data-command="tunnels_status" data-agent="${escapeAttr(agent.nickname)}">Tunnels</button>
            <button class="mini-btn danger" type="button" title="Restart AWG Manager" data-state="${buttonState(agent.nickname, "awgmgr")}" data-maint="awgmgr" data-agent="${escapeAttr(agent.nickname)}">Restart AWG</button>
            ${latestDeployButton(agent, "mini-btn primary", "Update")}
            <button class="mini-btn" type="button" title="Deploy a specific version" data-deploy="${escapeAttr(agent.nickname)}">Version</button>
          </div>
        </td>
      </tr>
    `;
  }

  function latestDeployButton(agent, className, label) {
    const latest = latestVersion();
    const current = agent.agent_version || "";
    const pending = agent.pending_version || "";
    const disabled = !latest || current === latest || Boolean(pending);
    const stateValue = pending ? "waiting" : buttonState(agent.nickname, "self_update");
    const suffix = latest ? ` ${latest}` : "";
    const text = pending ? `Pending ${pending}` : (current === latest ? `Latest${suffix}` : label);
    return `<button class="${escapeAttr(className)}" title="Update to latest${escapeAttr(suffix)}" type="button" data-state="${escapeAttr(stateValue)}" data-update-latest="1" data-agent="${escapeAttr(agent.nickname)}" ${disabled ? "disabled" : ""}>${escapeHTML(text)}</button>`;
  }

  function statusBadge(agent) {
    if (agent.status === "alert") return '<span class="badge badge-danger">alert</span>';
    if (agent.status === "online") return '<span class="badge badge-success">online</span>';
    if (agent.status === "sleeping") return '<span class="badge badge-warning">sleeping</span>';
    return '<span class="badge badge-muted">offline</span>';
  }

  function statusText(agent) {
    if (agent.status === "alert") {
      const first = (agent.active_incidents || [])[0];
      if (first) return `${first.check_name}: ${first.fail_count || 1} fail подряд`;
      return "есть hard-инцидент";
    }
    if (agent.status === "online") return "последний отчет " + formatLastSeen(agent);
    if (agent.status === "sleeping") return "мобильный спит; последний отчет " + formatLastSeen(agent);
    if (!agent.last_seen_at) return "еще не отчитывался";
    return "последний отчет " + formatLastSeen(agent);
  }

  function formatLastSeen(agent) {
    if (!agent.last_seen_at) return "never";
    if (typeof agent.last_seen_age_sec === "number") {
      const sec = agent.last_seen_age_sec;
      if (sec < 90) return sec + "s ago";
      const min = Math.round(sec / 60);
      if (min < 90) return min + "m ago";
      const hour = Math.round(min / 60);
      if (hour < 48) return hour + "h ago";
      return Math.round(hour / 24) + "d ago";
    }
    return agent.last_seen_at;
  }

  function groupLabel(chatID) {
    const telegram = state.summary?.telegram || {};
    if (!chatID || chatID === telegram.primary_chat_id) return `Primary group (${telegram.primary_chat_id || "default"})`;
    return `Group ${chatID}`;
  }

  function topicLabel(threadID) {
    return threadID ? `topic ${threadID}` : "topic не привязан";
  }

  function shortURL(value) {
    try {
      const u = new URL(value);
      return u.host;
    } catch (_) {
      return value;
    }
  }

  function renderSelectedDrawer() {
    const selected = state.selected && (state.summary?.agents || []).find((a) => a.nickname === state.selected);
    if (!selected) {
      els.agentDrawer.innerHTML = `<div class="drawer-empty"><span class="ti ti-router"></span><strong>Выбери агента</strong><p>Клик по строке откроет понятную карточку с проверками, Telegram topic и AWG Manager.</p></div>`;
      return;
    }
    const incidents = (selected.active_incidents || []).map((i) => `<span class="badge badge-danger">${escapeHTML(i.check_name)} ${i.fail_count || ""}</span>`).join("") || '<span class="badge badge-success">clear</span>';
    const cronStatus = state.opkgCron.get(selected.nickname);
    const cleanStatus = state.entwareClean.get(selected.nickname);
    const versionStatus = state.versions.get(selected.nickname);
    const agentConfigView = state.agentConfig.get(selected.nickname);
    const awgBlock = selected.awgm_url
      ? `<a class="action-btn primary" href="${escapeAttr(selected.awgm_url)}" target="_blank" rel="noreferrer noopener"><span class="ti ti-external-link"></span>Open AWG Manager</a><p class="drawer-note">Откроется веб-интерфейс AWG Manager. Логин остается на стороне AWG Manager.</p>`
      : `<button class="action-btn disabled" type="button" disabled><span class="ti ti-external-link"></span>Open AWG</button><p class="drawer-note">AWG Manager URL не сохранен. Добавь его через deploy sync или awgm-url-patch.</p>`;
    els.agentDrawer.innerHTML = `
      <div class="drawer-card">
        <div class="drawer-header">
          <div>
            <div class="eyebrow">agent detail</div>
            <h2>${escapeHTML(selected.nickname)}</h2>
            <p class="drawer-note">${escapeHTML(statusLongText(selected))}</p>
          </div>
          <button class="icon-btn" type="button" title="Edit router settings" data-edit-agent="${escapeAttr(selected.nickname)}"><span class="ti ti-edit"></span></button>
        </div>
        <section class="drawer-section">
          <h3>Состояние</h3>
          <div class="incident-list">${incidents}</div>
          <div class="drawer-list">
            ${drawerKV("Last seen", formatLastSeen(selected))}
            ${drawerKV("Version", selected.agent_version || "-")}
            ${drawerKV("Pending", selected.pending_version || "idle")}
          </div>
        </section>
        <section class="drawer-section">
          <h3>Telegram</h3>
          <p class="drawer-note">${selected.has_topic ? "Topic привязан, уведомления могут идти в отдельную тему." : "Topic не привязан. Уведомления не будут точечно попадать в тему этого роутера."}</p>
          <div class="drawer-list">
            ${drawerKV("Group", groupLabel(selected.telegram_chat_id))}
            ${drawerKV("Topic", topicLabel(selected.telegram_thread_id))}
          </div>
        </section>
        <section class="drawer-section">
          <h3>AWG Manager</h3>
          ${awgBlock}
          <div class="drawer-list">
            ${drawerKV("Deploy mode", selected.deploy_mode || "-")}
            ${drawerKV("AWG URL", selected.awgm_url || "-")}
            ${versionStatus ? drawerKV("AWG Manager", versionStatus.awgmgr_version || "-") : ""}
            ${versionStatus ? drawerKV("HR-Neo", versionStatus.hrneo_installed ? (versionStatus.hrneo_version || "installed") : "not installed") : ""}
            ${versionStatus ? drawerKV("Firmware", versionStatus.firmware_current || "-") : ""}
          </div>
          <div class="drawer-actions drawer-actions-top">
            ${drawerCommandButton(selected, "version_audit", "Versions")}
          </div>
        </section>
        <section class="drawer-section drawer-section-recovery">
          <h3>Resurrect / recovery</h3>
          <p class="drawer-note">Сменился домен или настройки роутера? Отредактируй агента, затем верни его в строй: wake запросит свежий отчёт, Restart AWG перезапустит AWG Manager на роутере.</p>
          <div class="drawer-actions">
            <button class="action-btn" type="button" data-edit-agent="${escapeAttr(selected.nickname)}"><span class="ti ti-edit"></span>Edit settings</button>
            ${drawerCommandButton(selected, "force_recheck", recheckLongLabel(selected))}
            <button class="action-btn" type="button" data-state="${buttonState(selected.nickname, "awgmgr")}" data-maint="awgmgr" data-agent="${escapeAttr(selected.nickname)}"><span class="ti ti-server"></span>Restart AWG</button>
          </div>
        </section>
        <section class="drawer-section">
          <h3>On-router config</h3>
          <p class="drawer-note">Безопасные ключи config.yaml на самом роутере (interval, awg-manager, external_reach, maintenance). Load читает текущие, Edit перепишет конфиг и перезапустит агента. backend URL тут НЕ меняется — это wizard.</p>
          <div class="incident-list">
            ${agentConfigView ? '<span class="badge badge-success">loaded</span>' : '<span class="badge badge-muted">not loaded</span>'}
            ${agentConfigView && agentConfigView.interval_sec ? `<span class="badge badge-info">interval ${escapeHTML(String(agentConfigView.interval_sec))}s</span>` : ""}
            ${agentConfigView && agentConfigView.awgm_base_url ? `<span class="badge badge-info">${escapeHTML(shortURL(agentConfigView.awgm_base_url))}</span>` : ""}
          </div>
          <div class="drawer-actions">
            ${drawerCommandButton(selected, "agent_config_get", "Load config")}
            ${agentConfigView ? `<button class="action-btn warn" type="button" data-agent-config="${escapeAttr(selected.nickname)}"><span class="ti ti-settings"></span>Edit & restart</button>` : ""}
          </div>
        </section>
        <section class="drawer-section">
          <h3>Checks</h3>
          <div class="drawer-actions">
            ${drawerCommandButton(selected, "diag_now", "Diagnostics")}
            ${drawerCommandButton(selected, "force_recheck", recheckLongLabel(selected))}
            ${drawerCommandButton(selected, "route_status", "Routes")}
            ${drawerCommandButton(selected, "tunnels_status", "Tunnels")}
            ${drawerCommandButton(selected, "pingcheck_status", "PingCheck")}
            ${drawerCommandButton(selected, "check_direct", "Direct")}
            ${drawerCommandButton(selected, "check_via_tunnel", "Via tunnel")}
            <button class="action-btn" type="button" data-state="${buttonState(selected.nickname, "awgmgr")}" data-maint="awgmgr" data-agent="${escapeAttr(selected.nickname)}"><span class="ti ti-server"></span>Restart AWG</button>
            ${latestDeployButton(selected, "action-btn primary", "Update to latest")}
            <button class="action-btn" type="button" data-deploy="${escapeAttr(selected.nickname)}"><span class="ti ti-rocket"></span>Custom version</button>
          </div>
        </section>
        <section class="drawer-section">
          <h3>OPKG cron</h3>
          <p class="drawer-note">Managed router-side schedule for opkg update + upgrade with /opt space guard and trimmed logs.</p>
          <div class="incident-list">
            ${cronStatus ? opkgCronBadge(cronStatus) : '<span class="badge badge-muted">not checked</span>'}
            ${cronStatus && cronStatus.schedule ? `<span class="badge badge-info">${escapeHTML(cronStatus.schedule)}</span>` : ""}
          </div>
          <label class="form-field schedule-field">
            <span>Run time</span>
            <input id="opkgCronSchedule" type="time" value="${escapeAttr(cronScheduleToTime(cronStatus && cronStatus.schedule, "04:30"))}">
          </label>
          <div class="drawer-actions">
            ${drawerCommandButton(selected, "opkg_cron_status", "Check")}
            ${drawerCommandButton(selected, "opkg_cron_install", "Install schedule")}
            ${drawerCommandButton(selected, "opkg_cron_logs", "Logs")}
            ${drawerCommandButton(selected, "opkg_cron_remove", "Remove")}
          </div>
        </section>
        <section class="drawer-section">
          <h3>Entware cleanup</h3>
          <p class="drawer-note">Managed cleanup for Entware temp/cache files with /opt space guard, memory snapshot and trimmed logs.</p>
          <div class="incident-list">
            ${cleanStatus ? entwareCleanBadge(cleanStatus) : '<span class="badge badge-muted">not checked</span>'}
            ${cleanStatus && cleanStatus.schedule ? `<span class="badge badge-info">${escapeHTML(cleanStatus.schedule)}</span>` : ""}
            ${cleanStatus && cleanStatus.mem_available_kb ? `<span class="badge badge-info">${escapeHTML(formatKB(cleanStatus.mem_available_kb))} free RAM</span>` : ""}
          </div>
          <label class="form-field schedule-field">
            <span>Run time</span>
            <input id="entwareCleanSchedule" type="time" value="${escapeAttr(cronScheduleToTime(cleanStatus && cleanStatus.schedule, "05:15"))}">
          </label>
          <div class="drawer-actions">
            ${drawerCommandButton(selected, "entware_clean_status", "Check")}
            ${drawerCommandButton(selected, "entware_clean_install", "Install schedule")}
            ${drawerCommandButton(selected, "entware_clean_run", "Run now")}
            ${drawerCommandButton(selected, "entware_clean_logs", "Logs")}
            ${drawerCommandButton(selected, "entware_clean_remove", "Remove")}
          </div>
        </section>
      </div>
    `;
  }

  function statusLongText(agent) {
    if (agent.status === "alert") return "Есть hard-инцидент. Начни с Diagnostics или Force recheck, затем смотри результат команды.";
    if (agent.status === "online") return "Агент онлайн: последний отчет " + formatLastSeen(agent) + ". Активных hard-инцидентов нет.";
    if (agent.status === "sleeping") return "Мобильный агент давно не присылал отчет и считается sleeping, а не online. Нажми Force recheck, если нужен свежий wake/report.";
    if (!agent.last_seen_at) return "Агент создан, но еще ни разу не прислал отчет.";
    return "Агент сейчас offline или давно не отчитывался. Последний отчет " + formatLastSeen(agent) + ".";
  }

  function recheckShortLabel(agent) {
    return agent.kind === "mobile" && agent.status === "sleeping" ? "Wake" : "Recheck";
  }

  function recheckLongLabel(agent) {
    return agent.kind === "mobile" && agent.status === "sleeping" ? "Wake / recheck" : "Force recheck";
  }

  function recheckTitle(agent) {
    return agent.kind === "mobile" && agent.status === "sleeping"
      ? "Wake mobile agent and request a fresh report"
      : "Force a fresh agent report";
  }

  function drawerKV(label, value) {
    return `<div class="drawer-kv"><span>${escapeHTML(label)}</span><strong>${escapeHTML(value)}</strong></div>`;
  }

  function drawerCommandButton(agent, action, label) {
    return `<button class="action-btn" type="button" data-state="${buttonState(agent.nickname, action)}" data-command="${escapeAttr(action)}" data-agent="${escapeAttr(agent.nickname)}">${escapeHTML(label)}</button>`;
  }

  async function enqueueCommand(nickname, action, args) {
    setActionState(nickname, action, "queued");
    const body = JSON.stringify({ action, args: args || {} });
    const res = await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}/commands`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body
    });
    toast(action + " queued");
    setActionState(nickname, action, "waiting");
    pollResult(nickname, res.cmd_id, action);
  }

  async function logout() {
    try {
      await fetch("/v1/dashboard/logout", { method: "POST", credentials: "same-origin" });
    } finally {
      window.location.href = "/dashboard/login";
    }
  }

  async function restartAWGM(nickname) {
    setActionState(nickname, "awgmgr", "queued");
    const res = await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}/maintenance`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "awgmgr" })
    });
    toast("awgmgr restart queued");
    setActionState(nickname, "awgmgr", "waiting");
    pollResult(nickname, res.cmd_id, "awgmgr");
  }

  async function deployAgent() {
    const version = els.deployVersionInput.value.trim();
    if (!version || !state.deployAgent) return;
    setButtonState(els.confirmDeployBtn, "waiting");
    const nickname = state.deployAgent;
    const res = await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}/deploy`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target_version: version })
    });
    closeModal();
    toast("deploy queued");
    pollResult(nickname, res.cmd_id, "self_update");
    refresh();
  }

  async function deployBackend() {
    const version = latestVersion();
    if (!version) { toast("latest version is not known yet"); return; }
    setButtonState(els.backendUpdateBtn, "waiting");
    await api("/v1/dashboard/backend/deploy", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target_version: version })
    });
    toast("Backend update queued → " + version + ". Перезагружаю страницу через 15 с...");
    window.setTimeout(() => window.location.reload(), 15000);
  }

  async function deployLatest(nickname) {
    const version = latestVersion();
    if (!version) {
      toast("latest version is not known yet");
      return;
    }
    setActionState(nickname, "self_update", "queued");
    const res = await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}/deploy`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ target_version: version })
    });
    toast("latest update queued: " + version);
    setActionState(nickname, "self_update", "waiting");
    pollResult(nickname, res.cmd_id, "self_update");
    refresh();
  }

  async function createEnrollment() {
    els.addAgentError.textContent = "";
    const group = selectedGroup();
    if (!els.newAgentNickname.value.trim()) {
      els.addAgentError.textContent = "Nickname required";
      return;
    }
    if (!group.ok) {
      els.addAgentError.textContent = group.error;
      return;
    }
    setButtonState(els.createAgentBtn, "waiting");
    try {
      const payload = {
        nickname: els.newAgentNickname.value.trim(),
        kind: els.newAgentKind.value,
        telegram_chat_id: group.chatID,
        telegram_thread_id: Number(els.newAgentThread.value || 0),
        custom_telegram_chat: group.custom,
        deploy_mode: els.newAgentDeployMode.value,
        awgm_url: els.newAgentAWGMURL.value.trim(),
        awgm_auth: els.newAgentAWGMAuth.value,
        ssh_host: els.newAgentSSHHost.value.trim(),
        ssh_port: Number(els.newAgentSSHPort.value || 0),
        ssh_user: els.newAgentSSHUser.value.trim(),
        arch: els.newAgentArch.value,
        ring: els.newAgentRing.value,
        expected_mac: els.newAgentExpectedMAC.value.trim()
      };
      const res = await api("/v1/dashboard/enrollments", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      });
      closeAddAgent();
      toast("enrollment created");
      showEnrollmentResult(res);
      await refresh();
    } catch (err) {
      els.addAgentError.textContent = err.message;
      setButtonState(els.createAgentBtn, "error");
    }
  }

  function selectedGroup() {
    const value = els.newAgentGroup.value;
    if (value === "custom") {
      const raw = els.newAgentCustomGroup.value.trim();
      if (!raw) return { ok: false, error: "Custom chat id required" };
      const chatID = Number(raw);
      if (!Number.isFinite(chatID) || chatID === 0) return { ok: false, error: "Custom chat id must be non-zero" };
      return { ok: true, chatID, custom: true };
    }
    return { ok: true, chatID: Number(value || 0), custom: false };
  }

  function showEnrollmentResult(res) {
    els.drawerTitle.textContent = "Enrollment / " + res.nickname;
    setResultHTML(formatEnrollmentResult(res));
    els.resultDrawer.classList.remove("hidden");
  }

  async function pollResult(nickname, cmdID, title) {
    if (!cmdID) return;
    els.drawerTitle.textContent = title + " / " + nickname;
    setResultHTML(`<div class="result-section"><strong>Waiting for agent</strong><p>Command id: ${escapeHTML(cmdID)}</p></div>`);
    els.resultDrawer.classList.remove("hidden");
    for (let i = 0; i < 12; i++) {
      try {
        const res = await api(`/v1/dashboard/commands/${encodeURIComponent(cmdID)}?nickname=${encodeURIComponent(nickname)}&wait_sec=5`);
        setResultHTML(formatCommandResult(res));
        rememberCommandResult(nickname, title, res);
        setActionState(nickname, title, res.status === "ok" ? "ok" : "error");
        refresh();
        return;
      } catch (err) {
        if (!(err.status === 404 && err.code === "result_not_ready")) {
          setResultHTML(`<div class="result-section"><strong>Command result failed</strong><p>Command id: ${escapeHTML(cmdID)}</p><p>${escapeHTML(err.message)}</p></div>`);
          setActionState(nickname, title, "error");
          refresh();
          return;
        }
        setResultHTML(`<div class="result-section"><strong>Waiting for agent</strong><p>Command id: ${escapeHTML(cmdID)}</p><p>${escapeHTML(err.message)}</p></div>`);
        await sleep(1200);
      }
    }
    setResultHTML(`<div class="result-section"><strong>Timed out waiting for agent</strong><p>Command id: ${escapeHTML(cmdID)}</p><p>No command result arrived after repeated polling.</p></div>`);
    setActionState(nickname, title, "error");
    refresh();
  }

  function openDeploy(nickname) {
    state.deployAgent = nickname;
    els.deployTitle.textContent = "Custom version / " + nickname;
    els.deployVersionInput.value = latestVersion();
    setButtonState(els.confirmDeployBtn, "idle");
    els.deployModal.classList.remove("hidden");
    els.deployVersionInput.focus();
  }

  function closeModal() {
    els.deployModal.classList.add("hidden");
    state.deployAgent = null;
    setButtonState(els.confirmDeployBtn, "idle");
  }

  function openAddAgent() {
    els.newAgentNickname.value = "";
    els.newAgentKind.value = "static";
    els.newAgentThread.value = "";
    els.newAgentCustomGroup.value = "";
    els.newAgentDeployMode.value = "";
    els.newAgentArch.value = "";
    els.newAgentRing.value = "";
    els.newAgentAWGMURL.value = "";
    els.newAgentAWGMAuth.value = "";
    els.newAgentSSHHost.value = "";
    els.newAgentSSHPort.value = "";
    els.newAgentSSHUser.value = "";
    els.newAgentExpectedMAC.value = "";
    els.addAgentError.textContent = "";
    setButtonState(els.createAgentBtn, "idle");
    renderGroupOptions();
    els.addAgentModal.classList.remove("hidden");
    els.newAgentNickname.focus();
  }

  function closeAddAgent() {
    els.addAgentModal.classList.add("hidden");
    setButtonState(els.createAgentBtn, "idle");
  }

  function openEditAgent(nickname) {
    const agent = (state.summary?.agents || []).find((a) => a.nickname === nickname);
    if (!agent) return;
    state.editAgent = nickname;
    els.editAgentTitle.textContent = "Edit agent / " + nickname;
    els.editAgentAWGMURL.value = agent.awgm_url || "";
    els.editAgentAWGMAuth.value = agent.awgm_auth || "";
    els.editAgentSSHHost.value = agent.ssh_host || "";
    els.editAgentSSHPort.value = agent.ssh_port ? String(agent.ssh_port) : "";
    els.editAgentSSHUser.value = agent.ssh_user || "";
    els.editAgentDeployMode.value = agent.deploy_mode || "";
    els.editAgentArch.value = agent.arch || "";
    els.editAgentRing.value = agent.ring || "";
    els.editAgentExpectedMAC.value = agent.expected_mac || "";
    els.editAgentError.textContent = "";
    setButtonState(els.saveAgentBtn, "idle");
    els.editAgentModal.classList.remove("hidden");
    els.editAgentAWGMURL.focus();
  }

  function closeEditAgent() {
    els.editAgentModal.classList.add("hidden");
    state.editAgent = null;
    setButtonState(els.saveAgentBtn, "idle");
  }

  async function saveAgentEdit() {
    const nickname = state.editAgent;
    if (!nickname) return;
    els.editAgentError.textContent = "";
    setButtonState(els.saveAgentBtn, "waiting");
    try {
      const payload = {
        awgm_url: els.editAgentAWGMURL.value.trim(),
        awgm_auth: els.editAgentAWGMAuth.value,
        ssh_host: els.editAgentSSHHost.value.trim(),
        ssh_port: Number(els.editAgentSSHPort.value || 0),
        ssh_user: els.editAgentSSHUser.value.trim(),
        deploy_mode: els.editAgentDeployMode.value,
        arch: els.editAgentArch.value,
        ring: els.editAgentRing.value,
        expected_mac: els.editAgentExpectedMAC.value.trim()
      };
      await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      });
      closeEditAgent();
      toast("agent settings saved");
      await refresh();
    } catch (err) {
      els.editAgentError.textContent = err.message;
      setButtonState(els.saveAgentBtn, "error");
    }
  }

  function openAgentConfig(nickname) {
    const cfg = state.agentConfig.get(nickname);
    if (!cfg) {
      toast("Сначала нажми «Load config» — нужен текущий конфиг с роутера");
      return;
    }
    state.agentConfigNick = nickname;
    els.agentConfigTitle.textContent = "Agent config / " + nickname;
    els.cfgIntervalSec.value = cfg.interval_sec != null ? String(cfg.interval_sec) : "";
    els.cfgExternalReachFail.value = cfg.external_reach_fail_threshold != null ? String(cfg.external_reach_fail_threshold) : "";
    els.cfgExternalReachEnabled.value = cfg.external_reach_enabled ? "true" : "false";
    els.cfgAwgmBaseURL.value = cfg.awgm_base_url || "";
    els.cfgAwgmLogin.value = cfg.awgm_login || "";
    els.cfgAllowReboot.value = cfg.allow_router_reboot ? "true" : "false";
    els.cfgAllowFirmware.value = cfg.allow_firmware_install ? "true" : "false";
    els.agentConfigError.textContent = "";
    setButtonState(els.applyAgentConfigBtn, "idle");
    els.agentConfigModal.classList.remove("hidden");
    els.cfgIntervalSec.focus();
  }

  function closeAgentConfig() {
    els.agentConfigModal.classList.add("hidden");
    state.agentConfigNick = null;
    setButtonState(els.applyAgentConfigBtn, "idle");
  }

  async function applyAgentConfig() {
    const nickname = state.agentConfigNick;
    if (!nickname) return;
    const interval = Number(els.cfgIntervalSec.value);
    const fail = Number(els.cfgExternalReachFail.value);
    if (!Number.isInteger(interval) || interval < 10 || interval > 86400) {
      els.agentConfigError.textContent = "Report interval должен быть 10..86400 секунд";
      return;
    }
    if (!Number.isInteger(fail) || fail < 1 || fail > 20) {
      els.agentConfigError.textContent = "external_reach fail threshold должен быть 1..20";
      return;
    }
    els.agentConfigError.textContent = "";
    setButtonState(els.applyAgentConfigBtn, "waiting");
    const args = {
      interval_sec: interval,
      external_reach_fail_threshold: fail,
      external_reach_enabled: els.cfgExternalReachEnabled.value === "true",
      awgm_base_url: els.cfgAwgmBaseURL.value.trim(),
      awgm_login: els.cfgAwgmLogin.value.trim(),
      allow_router_reboot: els.cfgAllowReboot.value === "true",
      allow_firmware_install: els.cfgAllowFirmware.value === "true"
    };
    try {
      setActionState(nickname, "update_agent_config", "queued");
      const res = await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}/commands`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action: "update_agent_config", args })
      });
      closeAgentConfig();
      toast("config queued — агент перезапустится");
      setActionState(nickname, "update_agent_config", "waiting");
      pollResult(nickname, res.cmd_id, "update_agent_config");
    } catch (err) {
      els.agentConfigError.textContent = err.message;
      setButtonState(els.applyAgentConfigBtn, "error");
    }
  }

  // cronScheduleToTime converts a stored "M H * * *" cron line to an "HH:MM"
  // value for the <input type=time> so the drawer reflects the live schedule.
  function cronScheduleToTime(schedule, fallback) {
    const fields = String(schedule || "").trim().split(/\s+/);
    if (fields.length === 5) {
      const min = Number(fields[0]);
      const hour = Number(fields[1]);
      if (Number.isInteger(min) && Number.isInteger(hour) && hour >= 0 && hour <= 23 && min >= 0 && min <= 59) {
        return String(hour).padStart(2, "0") + ":" + String(min).padStart(2, "0");
      }
    }
    return fallback;
  }

  function renderGroupOptions() {
    const telegram = state.summary?.telegram || {};
    const current = els.newAgentGroup.value;
    const options = [`<option value="0">Primary group (${escapeHTML(telegram.primary_chat_id || "default")})</option>`];
    (telegram.extra_chat_ids || []).forEach((chatID) => {
      options.push(`<option value="${escapeAttr(chatID)}">Extra group (${escapeHTML(chatID)})</option>`);
    });
    options.push('<option value="custom">Custom group...</option>');
    els.newAgentGroup.innerHTML = options.join("");
    if ([...els.newAgentGroup.options].some((o) => o.value === current)) {
      els.newAgentGroup.value = current;
    }
    toggleCustomGroup();
  }

  function toggleCustomGroup() {
    els.customGroupField.classList.toggle("hidden", els.newAgentGroup.value !== "custom");
  }

  function setResultHTML(html) {
    els.resultOutput.innerHTML = html;
  }

  function formatEnrollmentResult(res) {
    return [
      `<div class="result-section"><strong>${escapeHTML(res.message || "Agent enrollment created. Save the token now.")}</strong></div>`,
      resultGrid({
        nickname: res.nickname,
        backend_url: res.backend_url,
        telegram_chat_id: res.telegram_chat_id,
        telegram_thread_id: res.telegram_thread_id || "no topic"
      }),
      `<div class="result-section"><strong>Raw token</strong><pre class="raw-output">${escapeHTML(res.raw_token || "")}</pre></div>`,
      rawDetails(res)
    ].join("");
  }

  function formatCommandResult(res) {
    const sections = [
      `<div class="result-section"><strong>Status: ${escapeHTML(res.status || "unknown")}</strong><p>${escapeHTML(res.id || "no command id")}${res.duration_ms ? " / " + escapeHTML(res.duration_ms) + "ms" : ""}</p></div>`
    ];
    if (res.output) {
      sections.push(formatOutput(res.output));
    }
    if (res.error) {
      sections.push(`<div class="result-section result-error"><strong>Error</strong><pre class="raw-output">${escapeHTML(res.error)}</pre></div>`);
    }
    sections.push(rawDetails(res));
    return sections.join("");
  }

  function rememberCommandResult(nickname, action, res) {
    if (!res || res.status !== "ok" || typeof res.output !== "string") return;
    let parsed;
    try {
      parsed = JSON.parse(res.output);
    } catch (_) {
      return;
    }
    if (isOpkgCronStatus(parsed)) {
      state.opkgCron.set(nickname, parsed);
      renderSelectedDrawer();
    } else if (isEntwareCleanStatus(parsed)) {
      state.entwareClean.set(nickname, parsed);
      renderSelectedDrawer();
    } else if (isVersionAudit(parsed)) {
      state.versions.set(nickname, parsed);
      renderSelectedDrawer();
    } else if (isAgentConfig(parsed)) {
      state.agentConfig.set(nickname, parsed);
      renderSelectedDrawer();
    }
  }

  function opkgCronBadge(value) {
    if (value.installed) return '<span class="badge badge-success">installed</span>';
    return '<span class="badge badge-warning">not installed</span>';
  }

  function entwareCleanBadge(value) {
    if (value.installed) return '<span class="badge badge-success">installed</span>';
    return '<span class="badge badge-warning">not installed</span>';
  }

  function formatOutput(output) {
    if (typeof output !== "string") return resultValue("Output", output);
    const trimmed = output.trim();
    if (!trimmed) return `<div class="result-section"><strong>Output</strong><p>empty</p></div>`;
    try {
      return formatStructuredOutput(JSON.parse(trimmed));
    } catch (_) {
      return `<div class="result-section"><strong>Output</strong><pre class="raw-output">${escapeHTML(output)}</pre></div>`;
    }
  }

  function formatStructuredOutput(value) {
    if (Array.isArray(value)) return resultValue("Items", value);
    if (!value || typeof value !== "object") return resultValue("Output", value);
    if (isOpkgCronStatus(value)) return formatOpkgCronStatus(value);
    if (isEntwareCleanStatus(value)) return formatEntwareCleanStatus(value);
    if (isVersionAudit(value)) return formatVersionAudit(value);
    if (isAgentConfig(value)) return formatAgentConfig(value);
    const sections = [];
    const compact = {};
    Object.keys(value).forEach((key) => {
      const item = value[key];
      if (Array.isArray(item)) {
        sections.push(resultArray(key, item));
      } else if (item && typeof item === "object") {
        sections.push(resultValue(key, item));
      } else {
        compact[key] = item;
      }
    });
    if (Object.keys(compact).length) sections.unshift(resultGrid(compact));
    return sections.join("");
  }

  function isOpkgCronStatus(value) {
    return Object.prototype.hasOwnProperty.call(value, "installed") &&
      Object.prototype.hasOwnProperty.call(value, "script_path") &&
      Object.prototype.hasOwnProperty.call(value, "log_path") &&
      !Object.prototype.hasOwnProperty.call(value, "mem_available_kb");
  }

  function formatOpkgCronStatus(value) {
    const installed = value.installed ? "installed" : "not installed";
    const sections = [
      `<div class="result-section"><strong>OPKG cron: ${escapeHTML(installed)}</strong><p>${escapeHTML(value.schedule || "no schedule")}</p></div>`,
      resultGrid({
        free_opt: value.free_kb ? formatKB(value.free_kb) : "-",
        required_free: value.min_free_kb ? formatKB(value.min_free_kb) : "-",
        cron: value.cron_service || "-",
        last_run: value.last_run || "-",
        last_status: value.last_status || "-"
      }),
      resultGrid({
        script: value.script_path || "-",
        log: value.log_path || "-"
      })
    ];
    if (value.log_tail) {
      sections.push(`<div class="result-section"><strong>Log tail</strong><pre class="raw-output">${escapeHTML(value.log_tail)}</pre></div>`);
    }
    return sections.join("");
  }

  function isEntwareCleanStatus(value) {
    return Object.prototype.hasOwnProperty.call(value, "installed") &&
      Object.prototype.hasOwnProperty.call(value, "script_path") &&
      Object.prototype.hasOwnProperty.call(value, "log_path") &&
      Object.prototype.hasOwnProperty.call(value, "mem_available_kb");
  }

  function formatEntwareCleanStatus(value) {
    const installed = value.installed ? "installed" : "not installed";
    const sections = [
      `<div class="result-section"><strong>Entware cleanup: ${escapeHTML(installed)}</strong><p>${escapeHTML(value.schedule || "no schedule")}</p></div>`,
      resultGrid({
        free_opt: value.free_kb ? formatKB(value.free_kb) : "-",
        required_free: value.min_free_kb ? formatKB(value.min_free_kb) : "-",
        mem_available: value.mem_available_kb ? formatKB(value.mem_available_kb) : "-",
        mem_total: value.mem_total_kb ? formatKB(value.mem_total_kb) : "-",
        drop_cache_below: value.min_mem_available_kb ? formatKB(value.min_mem_available_kb) : "-",
        last_freed: value.last_freed_kb ? formatKB(value.last_freed_kb) : "-",
        cron: value.cron_service || "-",
        last_run: value.last_run || "-",
        last_status: value.last_status || "-"
      }),
      resultGrid({
        script: value.script_path || "-",
        log: value.log_path || "-"
      })
    ];
    if (value.log_tail) {
      sections.push(`<div class="result-section"><strong>Log tail</strong><pre class="raw-output">${escapeHTML(value.log_tail)}</pre></div>`);
    }
    return sections.join("");
  }

  function isVersionAudit(value) {
    return Object.prototype.hasOwnProperty.call(value, "awgmgr_version") ||
      Object.prototype.hasOwnProperty.call(value, "firmware_current") ||
      Object.prototype.hasOwnProperty.call(value, "hrneo_installed");
  }

  function formatVersionAudit(value) {
    return [
      `<div class="result-section"><strong>Router versions</strong><p>AWG Manager, HR-Neo and Keenetic firmware from the selected agent.</p></div>`,
      resultGrid({
        awg_manager: value.awgmgr_version || "-",
        awg_backend: value.awgmgr_backend || "-",
        awg_uptime: value.awgmgr_uptime || "-",
        hrneo: value.hrneo_installed ? (value.hrneo_version || "installed") : "not installed",
        hrneo_running: value.hrneo_installed ? displayValue(value.hrneo_running) : "-",
        hrneo_uptime: value.hrneo_uptime || "-",
        firmware: value.firmware_current || "-",
        firmware_available: value.firmware_avail || "-"
      })
    ].join("");
  }

  function isAgentConfig(value) {
    return Boolean(value) && value.config_kind === "agent";
  }

  function formatAgentConfig(value) {
    return [
      `<div class="result-section"><strong>On-router config</strong><p>${escapeHTML(value.config_path || "config.yaml")}</p></div>`,
      resultGrid({
        interval_sec: value.interval_sec,
        awgm_base_url: value.awgm_base_url,
        awgm_login: value.awgm_login,
        external_reach_enabled: value.external_reach_enabled,
        external_reach_fail_threshold: value.external_reach_fail_threshold,
        allow_router_reboot: value.allow_router_reboot,
        allow_firmware_install: value.allow_firmware_install
      })
    ].join("");
  }

  function resultGrid(values) {
    return `<div class="result-section result-grid">${Object.keys(values).map((key) => `
      <div><span>${escapeHTML(humanLabel(key))}</span><strong>${escapeHTML(displayValue(values[key]))}</strong></div>
    `).join("")}</div>`;
  }

  function resultArray(label, rows) {
    if (!rows.length) return `<div class="result-section"><strong>${escapeHTML(humanLabel(label))}</strong><p>empty</p></div>`;
    if (rows.every((row) => row && typeof row === "object" && !Array.isArray(row))) {
      const keys = Array.from(new Set(rows.flatMap((row) => Object.keys(row)))).slice(0, 8);
      return `<div class="result-section"><strong>${escapeHTML(humanLabel(label))}</strong><div class="result-table-wrap"><table class="result-table"><thead><tr>${keys.map((key) => `<th>${escapeHTML(humanLabel(key))}</th>`).join("")}</tr></thead><tbody>${rows.map((row) => `<tr>${keys.map((key) => `<td>${escapeHTML(displayValue(row[key]))}</td>`).join("")}</tr>`).join("")}</tbody></table></div></div>`;
    }
    return resultValue(label, rows);
  }

  function resultValue(label, value) {
    return `<div class="result-section"><strong>${escapeHTML(humanLabel(label))}</strong><pre class="raw-output">${escapeHTML(JSON.stringify(value, null, 2))}</pre></div>`;
  }

  function rawDetails(value) {
    return `<details class="raw-output"><summary>Raw JSON</summary><pre>${escapeHTML(JSON.stringify(value, null, 2))}</pre></details>`;
  }

  function humanLabel(value) {
    return String(value).replaceAll("_", " ");
  }

  function displayValue(value) {
    if (value == null || value === "") return "-";
    if (typeof value === "object") return JSON.stringify(value);
    return String(value);
  }

  function formatKB(value) {
    const kb = Number(value || 0);
    if (!Number.isFinite(kb) || kb <= 0) return "-";
    if (kb >= 1024 * 1024) return (kb / 1024 / 1024).toFixed(1) + " GB";
    if (kb >= 1024) return Math.round(kb / 1024) + " MB";
    return Math.round(kb) + " KB";
  }

  function commandArgs(button) {
    const action = button.dataset.command;
    if (action === "opkg_cron_install") {
      return { schedule: document.getElementById("opkgCronSchedule")?.value || "04:30" };
    }
    if (action === "entware_clean_install") {
      return { schedule: document.getElementById("entwareCleanSchedule")?.value || "05:15" };
    }
    if (action === "opkg_cron_status") return { lines: 40 };
    if (action === "opkg_cron_logs") return { lines: 160 };
    if (action === "entware_clean_status") return { lines: 40 };
    if (action === "entware_clean_logs") return { lines: 160 };
    return {};
  }

  function sleep(ms) {
    return new Promise((resolve) => window.setTimeout(resolve, ms));
  }

  function cssEscape(value) {
    if (window.CSS && typeof window.CSS.escape === "function") return window.CSS.escape(value);
    return String(value).replaceAll('"', '\\"');
  }

  function escapeHTML(value) {
    return String(value == null ? "" : value)
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#39;");
  }

  function escapeAttr(value) {
    return escapeHTML(value);
  }

  els.refreshBtn.addEventListener("click", refresh);
  els.addAgentBtn.addEventListener("click", openAddAgent);
  els.logoutBtn.addEventListener("click", logout);
  document.querySelector('[data-action="refresh"]').addEventListener("click", refresh);
  els.searchInput.addEventListener("input", (event) => {
    state.query = event.target.value;
    render();
  });
  document.querySelectorAll(".segment").forEach((button) => {
    button.addEventListener("click", () => {
      document.querySelectorAll(".segment").forEach((item) => item.classList.remove("active"));
      button.classList.add("active");
      state.filter = button.dataset.filter;
      render();
    });
  });
  document.body.addEventListener("click", (event) => {
    const button = event.target.closest("button");
    if (button) {
      if (isButtonBusy(button)) return;
      const nickname = button.dataset.agent || button.dataset.deploy;
      if (button.dataset.editAgent) openEditAgent(button.dataset.editAgent);
      if (button.dataset.agentConfig) openAgentConfig(button.dataset.agentConfig);
      if (button.dataset.deploy) openDeploy(nickname);
      if (button.dataset.updateLatest) deployLatest(nickname).catch((err) => {
        setActionState(nickname, "self_update", "error");
        toast(err.message);
      });
      if (button.dataset.command) enqueueCommand(nickname, button.dataset.command, commandArgs(button)).catch((err) => {
        setActionState(nickname, button.dataset.command, "error");
        toast(err.message);
      });
      if (button.dataset.maint) restartAWGM(nickname).catch((err) => {
        setActionState(nickname, "awgmgr", "error");
        toast(err.message);
      });
      return;
    }
    const row = event.target.closest("[data-row-agent]");
    if (row) {
      state.selected = row.dataset.rowAgent;
      renderSelectedDrawer();
    }
  });
  document.querySelectorAll("[data-close-modal]").forEach((button) => button.addEventListener("click", closeModal));
  document.querySelectorAll("[data-close-add-agent]").forEach((button) => button.addEventListener("click", closeAddAgent));
  document.querySelectorAll("[data-close-edit-agent]").forEach((button) => button.addEventListener("click", closeEditAgent));
  document.querySelectorAll("[data-close-agent-config]").forEach((button) => button.addEventListener("click", closeAgentConfig));
  els.confirmDeployBtn.addEventListener("click", () => deployAgent().catch((err) => {
    setButtonState(els.confirmDeployBtn, "error");
    toast(err.message);
  }));
  els.createAgentBtn.addEventListener("click", createEnrollment);
  els.saveAgentBtn.addEventListener("click", () => saveAgentEdit().catch((err) => {
    setButtonState(els.saveAgentBtn, "error");
    els.editAgentError.textContent = err.message;
  }));
  els.applyAgentConfigBtn.addEventListener("click", () => applyAgentConfig().catch((err) => {
    setButtonState(els.applyAgentConfigBtn, "error");
    els.agentConfigError.textContent = err.message;
  }));
  els.newAgentGroup.addEventListener("change", toggleCustomGroup);
  els.closeDrawerBtn.addEventListener("click", () => els.resultDrawer.classList.add("hidden"));
  els.autoRefreshBtn.addEventListener("click", () => setAutoRefresh(!state.autoRefresh));
  els.backendUpdateBtn.addEventListener("click", () => deployBackend().catch((err) => {
    setButtonState(els.backendUpdateBtn, "error");
    toast(err.message);
    window.setTimeout(() => setButtonState(els.backendUpdateBtn, "idle"), 3000);
  }));

  // Keyboard: Esc closes the top-most overlay; "/" focuses search.
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      if (!els.agentConfigModal.classList.contains("hidden")) { closeAgentConfig(); return; }
      if (!els.editAgentModal.classList.contains("hidden")) { closeEditAgent(); return; }
      if (!els.addAgentModal.classList.contains("hidden")) { closeAddAgent(); return; }
      if (!els.deployModal.classList.contains("hidden")) { closeModal(); return; }
      if (!els.resultDrawer.classList.contains("hidden")) { els.resultDrawer.classList.add("hidden"); return; }
      return;
    }
    if (event.key === "/" && !/^(input|select|textarea)$/i.test(event.target.tagName) && !overlayOpen()) {
      event.preventDefault();
      els.searchInput.focus();
    }
  });

  setAutoRefresh(true);
  updateFreshness();
  window.setInterval(updateFreshness, 1000);
  window.setInterval(autoRefreshTick, AUTO_REFRESH_MS);

  refresh();
})();
