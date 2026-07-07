(function () {
  const state = {
    summary: null,
    filter: "all",
    query: "",
    selected: null,
    drawerTab: "overview",
    deployAgent: null,
    editAgent: null,
    repairNick: null,
    autoRefresh: true,
    lastUpdatedAt: 0,
    buttonStates: new Map(),
    opkgCron: new Map(),
    entwareClean: new Map(),
    versions: new Map(),
    agentConfig: new Map(),
    cronAutoChecked: new Set(),
    provisionMode: null,
    provisionStep: "mode",
    repairMode: null,
    repairStep: "mode",
    // jobPoll is non-null while pollJob has a live setInterval ticking against
    // a provision/repair job (see pollJob/stopJobPoll) — overlayOpen() also
    // inspects this so auto-refresh pauses for the whole life of a job, not
    // just incidentally because the result drawer happens to be open.
    jobPoll: null,
    // modalReturnFocus holds document.activeElement from just before a modal
    // opened (see rememberFocus/restoreFocus below), so closing the modal can
    // return focus to whatever triggered it instead of leaving it wherever
    // the browser's focus-fixup rule drops it once the focused element's
    // subtree is hidden (usually <body>).
    modalReturnFocus: null
  };

  const els = {
    provisionBtn: document.getElementById("provisionBtn"),
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
    provisionModal: document.getElementById("provisionModal"),
    provisionNickname: document.getElementById("provisionNickname"),
    provisionKind: document.getElementById("provisionKind"),
    provisionGroup: document.getElementById("provisionGroup"),
    provisionCustomGroupField: document.getElementById("provisionCustomGroupField"),
    provisionCustomGroup: document.getElementById("provisionCustomGroup"),
    provisionThread: document.getElementById("provisionThread"),
    provisionIdentityError: document.getElementById("provisionIdentityError"),
    provisionIdentityNextBtn: document.getElementById("provisionIdentityNextBtn"),
    provisionAWGMURL: document.getElementById("provisionAWGMURL"),
    provisionAWGMAuth: document.getElementById("provisionAWGMAuth"),
    provisionRootPassword: document.getElementById("provisionRootPassword"),
    provisionVersion: document.getElementById("provisionVersion"),
    provisionAwgmLogin: document.getElementById("provisionAwgmLogin"),
    provisionAwgmPassword: document.getElementById("provisionAwgmPassword"),
    provisionAwgmApiKey: document.getElementById("provisionAwgmApiKey"),
    provisionAccessError: document.getElementById("provisionAccessError"),
    provisionStartBtn: document.getElementById("provisionStartBtn"),
    resultDrawer: document.getElementById("resultDrawer"),
    drawerTitle: document.getElementById("drawerTitle"),
    resultOutput: document.getElementById("resultOutput"),
    jobPollStatus: document.getElementById("jobPollStatus"),
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
    applyAgentConfigBtn: document.getElementById("applyAgentConfigBtn"),
    repairModal: document.getElementById("repairModal"),
    repairTitle: document.getElementById("repairTitle"),
    repairRootPassword: document.getElementById("repairRootPassword"),
    repairNewURLField: document.getElementById("repairNewURLField"),
    repairNewURL: document.getElementById("repairNewURL"),
    repairVersionField: document.getElementById("repairVersionField"),
    repairVersion: document.getElementById("repairVersion"),
    repairAwgmLogin: document.getElementById("repairAwgmLogin"),
    repairAwgmPassword: document.getElementById("repairAwgmPassword"),
    repairAwgmApiKey: document.getElementById("repairAwgmApiKey"),
    repairAccessError: document.getElementById("repairAccessError"),
    repairStartBtn: document.getElementById("repairStartBtn")
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
      const message = body && (body.message || body.error) ? (body.message || body.error) : String(body || res.statusText);
      if (res.status === 401) {
        window.location.href = "/dashboard/login";
        return null;
      }
      const error = new Error(message);
      error.status = res.status;
      error.code = body && body.code;
      error.body = body;
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

  // overlayOpen is true while a modal, the result drawer, or a live job poll
  // is active — used to pause auto-refresh so it never yanks the UI out from
  // under an open form or a running provision/repair job. The job-poll check
  // is belt-and-suspenders alongside the resultDrawer check: today
  // renderJobProgress always renders into the (visible) result drawer, so
  // the two conditions overlap for the whole life of a job, but gating on
  // state.jobPoll directly means the pause holds by construction — tied to
  // "is a job actively being polled", not incidentally to "is this one DOM
  // node's hidden class toggled" — and survives a future refactor that
  // renders progress somewhere else. Closes the audit's drawer-clobber
  // finding for this surface (flagged forward from Task 11's self-review).
  function overlayOpen() {
    return Boolean(document.querySelector(".modal:not(.hidden)")) ||
      !els.resultDrawer.classList.contains("hidden") ||
      Boolean(state.jobPoll);
  }

  // FOCUSABLE_SELECTOR / isVisible / focusableIn / rememberFocus / restoreFocus
  // / trapModalTab implement a small, dependency-free focus trap shared by
  // every .modal (deploy / provision / edit-agent / agent-config / repair —
  // including the provision and repair wizards' multi-step panels).
  // trapModalTab always re-queries the *currently* open ".modal:not(.hidden)"
  // and its visible focusable descendants on every Tab press rather than
  // caching a list at open time, so it stays correct as a wizard advances
  // through steps (each step toggles "hidden" on its own panel, changing
  // which fields are actually focusable) without any per-modal bookkeeping.
  const FOCUSABLE_SELECTOR = 'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])';

  function isVisible(el) {
    return Boolean(el.offsetWidth || el.offsetHeight || el.getClientRects().length);
  }

  function focusableIn(panel) {
    return Array.from(panel.querySelectorAll(FOCUSABLE_SELECTOR)).filter(isVisible);
  }

  // rememberFocus/restoreFocus bracket every open*/close* modal function pair:
  // rememberFocus runs just before a modal's "hidden" class is removed (so it
  // captures whatever triggered the open — a row action button, a drawer
  // link, ...); restoreFocus runs after the "hidden" class is re-added on
  // close, returning focus there instead of leaving it wherever the browser
  // dropped it once the previously-focused element's subtree got hidden.
  function rememberFocus() {
    state.modalReturnFocus = document.activeElement;
  }

  function restoreFocus() {
    const target = state.modalReturnFocus;
    state.modalReturnFocus = null;
    if (target && typeof target.focus === "function" && document.contains(target)) {
      target.focus();
    }
  }

  // trapModalTab keeps Tab/Shift+Tab cycling within the open modal's
  // .modal-panel instead of escaping to the page behind the backdrop. A
  // no-op when no modal is open, so it is safe to call unconditionally from
  // the global keydown handler below.
  function trapModalTab(event) {
    const modal = document.querySelector(".modal:not(.hidden)");
    if (!modal) return;
    const panel = modal.querySelector(".modal-panel");
    if (!panel) return;
    const focusable = focusableIn(panel);
    if (!focusable.length) return;
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    const active = document.activeElement;
    if (event.shiftKey) {
      if (active === first || !panel.contains(active)) {
        event.preventDefault();
        last.focus();
      }
    } else if (active === last || !panel.contains(active)) {
      event.preventDefault();
      first.focus();
    }
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
    renderHealthStrip(summary);
    const counts = filterCounts(summary);
    [["cntAll", "all"], ["cntAlert", "alert"], ["cntOnline", "online"], ["cntSleeping", "sleeping"], ["cntOffline", "offline"]]
      .forEach(([id, key]) => { const el = document.getElementById(id); if (el) el.textContent = counts[key]; });
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

  function filterCounts(summary) {
    const agents = summary.agents || [];
    const by = (s) => agents.filter((a) => a.status === s).length;
    return { all: agents.length, alert: by("alert"), online: by("online"), sleeping: by("sleeping"), offline: by("offline") };
  }

  function renderHealthStrip(summary) {
    const t = summary.totals || {};
    const segs = [
      { n: t.online || 0, cls: "seg-online", label: "online" },
      { n: t.sleeping || 0, cls: "seg-sleeping", label: "sleeping" },
      { n: t.alerts || 0, cls: "seg-alert", label: "alert" },
      { n: t.offline || 0, cls: "seg-offline", label: "offline" }
    ].filter((s) => s.n > 0);
    const total = segs.reduce((sum, s) => sum + s.n, 0) || 1;
    const bar = document.getElementById("healthBar");
    if (!bar) return;
    bar.innerHTML = segs.map((s) =>
      `<span class="health-seg ${s.cls}" style="width:${(s.n / total) * 100}%" title="${escapeAttr(s.n + " " + s.label)}"></span>`
    ).join("") || `<span class="health-seg seg-offline" style="width:100%"></span>`;
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
      ? `<span class="badge badge-warning">${escapeHTML(agent.pending_version)}</span><button class="mini-btn danger cancel-pending" type="button" title="Cancel pending deploy" data-cancel-deploy="${escapeAttr(agent.nickname)}">✕</button>`
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
          <div class="row-actions">
            <button class="mini-btn" type="button" title="Run diagnostics" data-state="${buttonState(agent.nickname, "diag_now")}" data-command="diag_now" data-agent="${escapeAttr(agent.nickname)}">Diag</button>
            <button class="mini-btn" type="button" title="${escapeAttr(recheckTitle(agent))}" data-state="${buttonState(agent.nickname, "force_recheck")}" data-command="force_recheck" data-agent="${escapeAttr(agent.nickname)}">${escapeHTML(recheckShortLabel(agent))}</button>
            ${latestDeployButton(agent, "mini-btn primary", "Update")}
            <div class="row-overflow">
              <button class="mini-btn" type="button" title="More actions" data-overflow="${escapeAttr(agent.nickname)}">⋯</button>
              <div class="row-overflow-menu">
                <button class="mini-btn" type="button" title="Show route status" data-state="${buttonState(agent.nickname, "route_status")}" data-command="route_status" data-agent="${escapeAttr(agent.nickname)}">Routes</button>
                <button class="mini-btn" type="button" title="Show tunnel status" data-state="${buttonState(agent.nickname, "tunnels_status")}" data-command="tunnels_status" data-agent="${escapeAttr(agent.nickname)}">Tunnels</button>
                <button class="mini-btn danger" type="button" title="Restart AWG Manager" data-state="${buttonState(agent.nickname, "awgmgr")}" data-maint="awgmgr" data-agent="${escapeAttr(agent.nickname)}">Restart AWG</button>
                <button class="mini-btn" type="button" title="Deploy a specific version" data-deploy="${escapeAttr(agent.nickname)}">Version</button>
              </div>
            </div>
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

  const DRAWER_TABS = [
    ["overview", "Overview"],
    ["maintenance", "Maintenance"],
    ["config", "Config"],
    ["recovery", "Recovery"]
  ];

  // Reference DNS-over-TLS upstreams for Keenetic/NetCraze (Entware ndmc).
  // Shown read-only in the Maintenance tab with a copy button — these run on the
  // router via SSH/terminal, not dispatched through the agent.
  const DNS_REFERENCE_COMMANDS = [
    "ndmc -c dns-proxy tls upstream 8.8.8.8 sni dns.google",
    "ndmc -c dns-proxy tls upstream 9.9.9.9 sni dns.quad9.net",
    "ndmc -c dns-proxy tls upstream 1.1.1.1 sni cloudflare-dns.com",
    "ndmc -c dns-proxy tls upstream common.dot.dns.yandex.net domain ru",
    "ndmc -c dns-proxy tls upstream common.dot.dns.yandex.net domain su",
    "ndmc -c dns-proxy tls upstream common.dot.dns.yandex.net domain xn--p1ai",
    "ndmc -c system configuration save"
  ].join("\n");

  // copyText copies via the async Clipboard API when available (secure context),
  // else falls back to a hidden textarea + execCommand("copy") so it still works
  // when the dashboard is served over plain http.
  async function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      try {
        await navigator.clipboard.writeText(text);
        return true;
      } catch (err) {
        // fall through to the legacy path
      }
    }
    try {
      const ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      const ok = document.execCommand("copy");
      document.body.removeChild(ta);
      return ok;
    } catch (err) {
      return false;
    }
  }

  function copyDNSReference() {
    const node = document.getElementById("dnsReferenceScript");
    const text = node ? node.textContent : DNS_REFERENCE_COMMANDS;
    copyText(text).then((ok) => toast(ok ? "DNS-команды скопированы" : "Не удалось скопировать"));
  }

  function renderSelectedDrawer() {
    const selected = state.selected && (state.summary?.agents || []).find((a) => a.nickname === state.selected);
    if (!selected) {
      els.agentDrawer.innerHTML = `<div class="drawer-empty"><span class="ti ti-router"></span><strong>Выбери агента</strong><p>Клик по строке откроет понятную карточку с проверками, Telegram topic и AWG Manager.</p></div>`;
      return;
    }
    const tab = state.drawerTab || "overview";
    const nav = DRAWER_TABS.map(([id, label]) =>
      `<button type="button" data-drawer-tab="${id}" class="${tab === id ? "active" : ""}">${label}</button>`
    ).join("");
    let body;
    if (tab === "maintenance") body = drawerTabMaintenance(selected);
    else if (tab === "config") body = drawerTabConfig(selected);
    else if (tab === "recovery") body = drawerTabRecovery(selected);
    else body = drawerTabOverview(selected);
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
        <nav class="drawer-tabs" aria-label="Agent detail tabs">${nav}</nav>
        ${body}
      </div>
    `;
  }

  function drawerTabOverview(selected) {
    const incidents = (selected.active_incidents || []).map((i) => `<span class="badge badge-danger">${escapeHTML(i.check_name)} ${i.fail_count || ""}</span>`).join("") || '<span class="badge badge-success">clear</span>';
    const versionStatus = state.versions.get(selected.nickname);
    const cronText = opkgCronOverviewText(selected);
    const awgBlock = selected.awgm_url
      ? `<a class="action-btn primary" href="${escapeAttr(selected.awgm_url)}" target="_blank" rel="noreferrer noopener"><span class="ti ti-external-link"></span>Open AWG Manager</a><p class="drawer-note">Откроется веб-интерфейс AWG Manager. Логин остается на стороне AWG Manager.</p>`
      : `<button class="action-btn disabled" type="button" disabled><span class="ti ti-external-link"></span>Open AWG</button><p class="drawer-note">AWG Manager URL не сохранен. Добавь его через deploy sync или awgm-url-patch.</p>`;
    return `
        <section class="drawer-section">
          <h3>Состояние</h3>
          <div class="incident-list">${incidents}</div>
          <div class="drawer-list">
            ${drawerKV("Last seen", formatLastSeen(selected))}
            ${drawerKV("Version", selected.agent_version || "-")}
            ${drawerKV("Pending", selected.pending_version || "idle")}
            ${drawerKV("OPKG cron", cronText)}
          </div>
          ${selected.pending_version ? `<div class="drawer-actions drawer-actions-top"><button class="action-btn warn" type="button" title="Снять зависший pending-деплой (дропает очередь self_update, чтобы он не сработал при пробуждении)" data-cancel-deploy="${escapeAttr(selected.nickname)}"><span class="ti ti-x"></span>Cancel pending ${escapeHTML(selected.pending_version)}</button></div>` : ""}
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
    `;
  }

  function drawerTabMaintenance(selected) {
    const cronStatus = state.opkgCron.get(selected.nickname);
    const cleanStatus = state.entwareClean.get(selected.nickname);
    return `
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
        <section class="drawer-section">
          <h3>DNS reset (reference DoT)</h3>
          <p class="drawer-note">«Reset DNS» прогонит на роутере через агента эталонную настройку DNS-over-TLS: снимет все текущие dns-proxy upstream'ы (DoT/DoH) и применит набор ниже, затем сохранит конфиг. Агент должен быть online. Per-interface ip name-server от туннелей не трогаются — удали их из конфигов туннелей, иначе пересоздадутся. Не допускай дублирований: иначе маршрутизация ведёт себя непредсказуемо.</p>
          <div class="drawer-actions">
            ${drawerCommandButton(selected, "dns_reset", "Reset DNS → reference")}
          </div>
          <p class="drawer-note">Эталонные команды (для ручного прогона в SSH/терминале роутера):</p>
          <pre id="dnsReferenceScript" class="raw-output">${escapeHTML(DNS_REFERENCE_COMMANDS)}</pre>
          <div class="drawer-actions">
            <button class="action-btn" type="button" data-copy-dns><span class="ti ti-clipboard"></span>Copy commands</button>
          </div>
        </section>
    `;
  }

  function drawerTabConfig(selected) {
    const agentConfigView = state.agentConfig.get(selected.nickname);
    return `
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
    `;
  }

  function drawerTabRecovery(selected) {
    return `
        <section class="drawer-section drawer-section-recovery">
          <h3>Resurrect / recovery</h3>
          <p class="drawer-note">Сменился домен или настройки роутера, или агент тёмный? Edit меняет метаданные на бэкенде; Repair чинит сам роутер через awg-manager terminal (re-point URL или полная переустановка) — работает даже если агент офлайн.</p>
          <div class="drawer-actions">
            <button class="action-btn" type="button" data-edit-agent="${escapeAttr(selected.nickname)}"><span class="ti ti-edit"></span>Edit settings</button>
            <button class="action-btn warn" type="button" title="Почини роутер через awg-manager terminal: быстрый re-point URL или полная переустановка (работает даже если агент офлайн)" data-repair="${escapeAttr(selected.nickname)}"><span class="ti ti-terminal"></span>Repair</button>
            ${drawerCommandButton(selected, "force_recheck", recheckLongLabel(selected))}
            <button class="action-btn" type="button" data-state="${buttonState(selected.nickname, "awgmgr")}" data-maint="awgmgr" data-agent="${escapeAttr(selected.nickname)}"><span class="ti ti-server"></span>Restart AWG</button>
          </div>
        </section>
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

  async function cancelDeploy(nickname) {
    if (!nickname) return;
    const res = await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}/deploy/cancel`, { method: "POST" });
    toast(res && res.cleared ? "pending deploy отменён" : "нечего отменять");
    await refresh();
  }

  // opkgCronOverviewText surfaces the OPKG update-cron install state at a glance
  // in the agent drawer, so the operator doesn't have to dig into Maintenance and
  // click Check. Reflects the cached opkg_cron_status (auto-checked on select).
  function opkgCronOverviewText(selected) {
    const cron = state.opkgCron.get(selected.nickname);
    if (cron) return cron.installed ? "installed" + (cron.schedule ? " · " + cron.schedule : "") : "not installed";
    if (state.cronAutoChecked.has(selected.nickname)) return "проверяю…";
    if (selected.status !== "online" && selected.status !== "alert") return "— (агент офлайн)";
    return "—";
  }

  // maybeAutoCheckCron fires a one-shot, silent opkg_cron_status when a reachable
  // agent is opened and its cron state is not yet known — so "OPKG cron" in the
  // drawer fills in by itself. Skips sleeping/offline agents (can't reach them)
  // and never re-fires for the same agent in a session.
  function maybeAutoCheckCron(nickname) {
    if (!nickname || state.cronAutoChecked.has(nickname) || state.opkgCron.has(nickname)) return;
    const agent = (state.summary?.agents || []).find((a) => a.nickname === nickname);
    if (!agent || (agent.status !== "online" && agent.status !== "alert")) return;
    state.cronAutoChecked.add(nickname);
    renderSelectedDrawer();
    silentCommand(nickname, "opkg_cron_status", { lines: 40 });
  }

  // silentCommand queues a read-only status command and polls its result without
  // opening the result drawer, feeding the cached state via rememberCommandResult.
  // Best-effort: errors and timeouts are swallowed (the badge falls back to "—").
  async function silentCommand(nickname, action, args) {
    try {
      const res = await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}/commands`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ action, args: args || {} })
      });
      if (!res || !res.cmd_id) return;
      for (let i = 0; i < 8; i++) {
        try {
          const r = await api(`/v1/dashboard/commands/${encodeURIComponent(res.cmd_id)}?nickname=${encodeURIComponent(nickname)}&wait_sec=5`);
          rememberCommandResult(nickname, action, r);
          return;
        } catch (err) {
          if (!(err.status === 404 && err.code === "result_not_ready")) return;
          await sleep(1200);
        }
      }
    } catch (_) {
      // silent — auto-check is best-effort
    }
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

  // selectedGroup reads the provision-wizard identity step's Telegram group
  // picker (a preset primary/extra chat id, or the "Custom group..." option
  // backed by provisionCustomGroup). Only ever used by the provision wizard now.
  function selectedGroup() {
    const value = els.provisionGroup.value;
    if (value === "custom") {
      const raw = els.provisionCustomGroup.value.trim();
      if (!raw) return { ok: false, error: "Укажи свой chat id" };
      const chatID = Number(raw);
      if (!Number.isFinite(chatID) || chatID === 0) return { ok: false, error: "Chat id должен быть не равен 0" };
      return { ok: true, chatID, custom: true };
    }
    return { ok: true, chatID: Number(value || 0), custom: false };
  }

  function showEnrollmentResult(res) {
    els.drawerTitle.textContent = "Enrollment / " + res.nickname;
    setResultHTML(formatEnrollmentResult(res));
    els.resultDrawer.classList.remove("hidden");
  }

  // provisionShowStep switches the visible panel inside #provisionModal —
  // vanilla show/hide over the four data-step panels (mode/identity/access/run),
  // no router/bundler involved.
  function provisionShowStep(step) {
    state.provisionStep = step;
    document.querySelectorAll("#provisionModal [data-step]").forEach((panel) => {
      panel.classList.toggle("hidden", panel.dataset.step !== step);
    });
  }

  // PROVISION_BACK_MAP is the static predecessor for each panel's Back button —
  // the wizard only ever moves forward one step at a time, so a plain lookup
  // is enough (no history stack needed).
  const PROVISION_BACK_MAP = { identity: "mode", access: "identity" };

  function provisionGoBack() {
    const prev = PROVISION_BACK_MAP[state.provisionStep];
    if (prev) provisionShowStep(prev);
  }

  // updateProvisionIdentityButton swaps the identity step's advance button
  // between "Дальше" (provision mode — goes on to the access step) and
  // "Зарегистрировать" (register mode — submits right away, no router access
  // needed). The strings are fixed literals, not user/server data, so no
  // escaping is needed for this innerHTML assignment.
  function updateProvisionIdentityButton() {
    els.provisionIdentityNextBtn.innerHTML = state.provisionMode === "register"
      ? '<span class="ti ti-rocket"></span>Зарегистрировать'
      : "Дальше";
  }

  function chooseProvisionMode(mode) {
    state.provisionMode = mode;
    els.provisionIdentityError.textContent = "";
    els.provisionAccessError.textContent = "";
    setButtonState(els.provisionIdentityNextBtn, "idle");
    setButtonState(els.provisionStartBtn, "idle");
    updateProvisionIdentityButton();
    provisionShowStep("identity");
    els.provisionNickname.focus();
  }

  // provisionValidateIdentity is shared by the register-direct-submit path and
  // the provision-mode "advance to access" path — both need nickname + a valid
  // Telegram group before going further.
  function provisionValidateIdentity() {
    const nickname = els.provisionNickname.value.trim();
    if (!nickname) return { ok: false, error: "Никнейм обязателен" };
    const group = selectedGroup();
    if (!group.ok) return { ok: false, error: group.error };
    return { ok: true, nickname, group };
  }

  function provisionIdentityNext() {
    if (state.provisionMode === "register") {
      submitProvision("register").catch((err) => toast(err.message));
      return;
    }
    const v = provisionValidateIdentity();
    if (!v.ok) {
      els.provisionIdentityError.textContent = v.error;
      return;
    }
    els.provisionIdentityError.textContent = "";
    provisionShowStep("access");
    els.provisionAWGMURL.focus();
  }

  function openProvision() {
    state.provisionMode = null;
    els.provisionNickname.value = "";
    els.provisionKind.value = "static";
    els.provisionThread.value = "";
    els.provisionCustomGroup.value = "";
    els.provisionAWGMURL.value = "";
    els.provisionAWGMAuth.value = "";
    els.provisionRootPassword.value = "";
    els.provisionVersion.value = latestVersion();
    els.provisionAwgmLogin.value = "";
    els.provisionAwgmPassword.value = "";
    els.provisionAwgmApiKey.value = "";
    els.provisionIdentityError.textContent = "";
    els.provisionAccessError.textContent = "";
    setButtonState(els.provisionStartBtn, "idle");
    setButtonState(els.provisionIdentityNextBtn, "idle");
    renderGroupOptions();
    provisionShowStep("mode");
    rememberFocus();
    els.provisionModal.classList.remove("hidden");
  }

  function closeProvision() {
    els.provisionModal.classList.add("hidden");
    state.provisionMode = null;
    setButtonState(els.provisionStartBtn, "idle");
    setButtonState(els.provisionIdentityNextBtn, "idle");
    restoreFocus();
  }

  // submitProvision posts to the one provisioning endpoint for both wizard
  // outcomes: kind "register" mints a token only (no relay call, no router
  // access needed — the access step is skipped entirely); kind "provision"
  // validates + sends the router-access fields too and gets back an async
  // job to hand off to startJobProgress. Errors bounce the wizard back to
  // whichever step owns the offending fields, mirroring the old
  // createEnrollment/submitDeployRouter per-panel error convention.
  async function submitProvision(kind) {
    const v = provisionValidateIdentity();
    if (!v.ok) {
      els.provisionIdentityError.textContent = v.error;
      provisionShowStep("identity");
      return;
    }
    els.provisionIdentityError.textContent = "";

    const payload = {
      kind,
      nickname: v.nickname,
      agent_kind: els.provisionKind.value,
      telegram_group: v.group.chatID,
      thread_id: Number(els.provisionThread.value || 0)
    };

    if (kind === "provision") {
      const awgmURL = els.provisionAWGMURL.value.trim();
      if (!awgmURL) {
        els.provisionAccessError.textContent = "AWG Manager URL обязателен";
        provisionShowStep("access");
        return;
      }
      if (!els.provisionRootPassword.value) {
        els.provisionAccessError.textContent = "Нужен root-пароль роутера";
        provisionShowStep("access");
        return;
      }
      els.provisionAccessError.textContent = "";
      payload.awgm_url = awgmURL;
      payload.awgm_auth = els.provisionAWGMAuth.value;
      payload.root_password = els.provisionRootPassword.value;
      payload.awgm_login = els.provisionAwgmLogin.value.trim();
      payload.awgm_password = els.provisionAwgmPassword.value;
      payload.awgm_api_key = els.provisionAwgmApiKey.value.trim();
      payload.version = els.provisionVersion.value.trim();
    }

    const busyBtn = kind === "provision" ? els.provisionStartBtn : els.provisionIdentityNextBtn;
    setButtonState(busyBtn, "waiting");
    provisionShowStep("run");
    try {
      const res = await api("/v1/dashboard/provision", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      });
      closeProvision();
      if (kind === "register") {
        toast("токен создан — сохрани сейчас");
        showEnrollmentResult(Object.assign({}, res, {
          telegram_chat_id: v.group.chatID,
          telegram_thread_id: payload.thread_id
        }));
      } else {
        toast("установка запущена — job " + ((res && res.job_id) || ""));
        startJobProgress(v.nickname, res, "Provision");
      }
      await refresh();
    } catch (err) {
      setButtonState(busyBtn, "error");
      if (kind === "register") {
        provisionShowStep("identity");
        els.provisionIdentityError.textContent = err.message;
      } else {
        provisionShowStep("access");
        els.provisionAccessError.textContent = err.message;
      }
    }
  }

  // STEP_LABELS is the shared declarative registry the provisioning-rework
  // design spec calls for ("a small declarative registry [{name,label,icon}]"):
  // name -> {label, icon} for every step-catalog constant provision/steps.go
  // defines (installSequence + repointSequence's full 10-name set). A step
  // whose name isn't in this map (e.g. a future step the backend grew before
  // this bundle was redeployed) still renders safely — stepRow falls back to
  // the raw, escaped step name — rather than throwing.
  //
  // Order here is irrelevant: renderJobProgress always walks job.steps in the
  // array order the backend's per-kind template defines (steps.go's own
  // Template), never re-sorted client-side, so a later task reordering
  // installSequence cannot break this map.
  //
  // icon is a decorative tabler glyph shown next to the label. The icons
  // picked here deliberately stick to a small subset (ti-dashboard/refresh/
  // lock/search/x/rocket/terminal/activity/upload/player-play) even though
  // vendor/tabler-icons-lite.css now defines more glyphs (added for other
  // dashboard buttons), so a few of these are the closest available
  // stand-in rather than a perfect semantic match (e.g. ti-upload for
  // "downloading" — there's no dedicated download glyph in the subset).
  // Purely cosmetic either way; the actual done/failed/active signal is
  // stepStatusGlyph, not this icon.
  const STEP_LABELS = {
    terminal_connected: { icon: "ti-terminal", label: "Подключение к терминалу" },
    arch_detected: { icon: "ti-activity", label: "Определение архитектуры" },
    downloading: { icon: "ti-upload", label: "Скачивание бинаря" },
    checksum_ok: { icon: "ti-lock", label: "Проверка подписи/суммы" },
    config_written: { icon: "ti-activity", label: "Запись конфигурации" },
    init_installed: { icon: "ti-terminal", label: "Установка init-скрипта" },
    service_started: { icon: "ti-player-play", label: "Запуск сервиса" },
    backend_url_rewritten: { icon: "ti-refresh", label: "Обновление backend URL" },
    service_restarted: { icon: "ti-player-play", label: "Перезапуск сервиса" },
    verify_online: { icon: "ti-rocket", label: "Агент выходит на связь" }
  };

  // JOB_STATE_HEADERS renders renderJobProgress's header from job.state. Any
  // value outside this map (defensive only — the backend Job.State is always
  // one of these three) falls back to the escaped raw state string instead of
  // rendering "undefined" or throwing.
  const JOB_STATE_HEADERS = {
    running: "Установка идёт…",
    success: "Готово",
    failed: "Ошибка"
  };

  // STEP_STATUS_GLYPH is the left-hand status marker for one checklist row —
  // independent of STEP_LABELS's per-step topic icon. Every value is a fixed
  // literal (the spinner reuses the existing wg-spin keyframe via the bare
  // .spin rule in app.css, shared with .wizard-run-status and
  // #jobPollStatus's reconnect badge), so none of this ever needs escaping.
  // Any status outside pending/active/done/failed (defensive only) falls
  // back to the pending glyph.
  const STEP_STATUS_GLYPH = {
    pending: "⏳",
    active: '<span class="ti ti-refresh spin" aria-hidden="true"></span>',
    done: "✓",
    failed: "✗"
  };

  function stepStatusGlyph(status) {
    return STEP_STATUS_GLYPH[status] || STEP_STATUS_GLYPH.pending;
  }

  // stepRow renders one job.steps[] entry. name/status/detail are all
  // server-controlled, so every one is escaped before insertion — status only
  // ever feeds a `data-status` attribute here, but it is escaped anyway per
  // the "escape every dynamic value" rule rather than trusting the backend's
  // enum shape never to change.
  function stepRow(step) {
    const meta = STEP_LABELS[step.name];
    const label = escapeHTML((meta && meta.label) || step.name);
    const icon = (meta && meta.icon) || "ti-activity";
    const statusAttr = escapeAttr(step.status || "pending");
    const detail = step.detail
      ? `<div class="job-step-detail">${escapeHTML(step.detail)}</div>`
      : "";
    return `
      <div class="job-step" data-status="${statusAttr}">
        <span class="job-step-glyph" aria-hidden="true">${stepStatusGlyph(step.status)}</span>
        <span class="job-step-icon ti ${icon}" aria-hidden="true"></span>
        <div class="job-step-body">
          <div class="job-step-label">${label}</div>
          ${detail}
        </div>
      </div>`;
  }

  function findStep(steps, name) {
    return (steps || []).find((step) => step.name === name);
  }

  // failedStepLabel finds the step the engine's fail() (runner.go) marked
  // StepFailed and returns its escaped human label for the "Упало на «label»"
  // card. Falls back to a generic phrase if no step is marked failed
  // (defensive only — the backend always marks exactly one step failed
  // alongside StateFailed).
  function failedStepLabel(steps) {
    const failed = (steps || []).find((step) => step.status === "failed");
    if (!failed) return "неизвестный шаг";
    const meta = STEP_LABELS[failed.name];
    return escapeHTML((meta && meta.label) || failed.name);
  }

  // jobSuccessCard: "✓ Агент онлайн — <version>, <verify_online detail>" per
  // the design spec's Progress view section. Both pieces degrade gracefully
  // (no dangling "—"/"," ) if either is missing — reachable in practice only
  // for a job kind that never sets Job.Version (e.g. a future repair_repoint
  // job reusing this same shared renderer, see steps.go's repointSequence).
  function jobSuccessCard(job) {
    const verify = findStep(job.steps, "verify_online");
    const parts = [];
    if (job.version) parts.push(escapeHTML(job.version));
    if (verify && verify.detail) parts.push(escapeHTML(verify.detail));
    const suffix = parts.length ? " — " + parts.join(", ") : "";
    return `<div class="result-section"><strong>✓ Агент онлайн${suffix}</strong></div>`;
  }

  // jobFailureCard: "✗ Упало на «label»" + the backend's already-friendly,
  // already-Russian job.hint, plus a collapsible, already-redacted job.tail
  // (still escaped here regardless — never trust a raw token wasn't left in
  // by some future backend change).
  function jobFailureCard(job) {
    const label = failedStepLabel(job.steps);
    const hint = job.hint ? `<p>${escapeHTML(job.hint)}</p>` : "";
    const card = `<div class="result-section result-error"><strong>✗ Упало на «${label}»</strong>${hint}</div>`;
    if (!job.tail) return card;
    return card + `<details class="raw-output"><summary>Журнал терминала</summary><pre>${escapeHTML(job.tail)}</pre></details>`;
  }

  // renderJobProgress is the shared progress-render component the design spec
  // calls for: header from job.state, checklist from job.steps walked in
  // array order (never reordered/sorted here — see STEP_LABELS's doc), plus
  // the terminal success/failure card once job.state leaves "running". Called
  // both for the initial seeded {job_id,steps} response (startJobProgress)
  // and on every pollJob tick, so it always fully replaces resultOutput
  // rather than incrementally patching it.
  function renderJobProgress(job) {
    const steps = Array.isArray(job.steps) ? job.steps : [];
    const header = JOB_STATE_HEADERS[job.state] || escapeHTML(String(job.state || "unknown"));
    const rows = steps.length
      ? steps.map(stepRow).join("")
      : '<p class="job-steps-empty">Шаги ещё не пришли.</p>';
    const sections = [
      `<div class="result-section job-progress-header"><strong>${header}</strong></div>`,
      `<div class="result-section"><div class="job-steps">${rows}</div></div>`
    ];
    if (job.state === "success") {
      sections.push(jobSuccessCard(job));
    } else if (job.state === "failed") {
      sections.push(jobFailureCard(job));
    }
    setResultHTML(sections.join(""));
  }

  // jobPollToken guards a poll's in-flight fetch against acting on stale
  // data: stopJobPoll bumps it, so a request that resolves after the user
  // closed the drawer (or a newer job superseded this one) checks its
  // captured token against the live one before touching state/DOM. Kept as a
  // plain module variable (not on `state`) — it's pure internal bookkeeping
  // that nothing else ever reads or renders, unlike state.jobPoll below,
  // which overlayOpen/stopJobPoll do inspect.
  let jobPollToken = 0;

  // stopJobPoll clears the live poll's setInterval (if any) and marks no job
  // as polling. The one function that MUST run on every exit path: terminal
  // job state, explicit drawer close (closeResultDrawer), and the top of
  // pollJob itself (superseding whatever ran before it). Safe to call when
  // nothing is polling. Also hides the transient "reconnecting" badge (see
  // showJobPollTransient/hideJobPollTransient below) so it can never survive
  // past the job it belonged to — one choke point for all poll-related UI
  // teardown, same reasoning as the interval/token bookkeeping it already did.
  function stopJobPoll() {
    jobPollToken++;
    if (state.jobPoll && state.jobPoll.timer != null) {
      window.clearInterval(state.jobPoll.timer);
    }
    state.jobPoll = null;
    hideJobPollTransient();
  }

  // renderJobPollError is the genuinely-terminal card for a 404: job.go's
  // Sweep only evicts terminal (success/failed) jobs after a 30-minute TTL —
  // running jobs are never evicted — so a 404 here means the job really is
  // gone (store-evicted or, defensively, never existed), not merely that one
  // request failed.
  function renderJobPollError() {
    setResultHTML(
      '<div class="result-section result-error"><strong>Задача не найдена или истекла</strong>' +
      "<p>Не удалось получить статус установки — возможно, задача устарела (TTL 30 минут) или сервер временно недоступен. Обнови страницу или запусти заново.</p></div>"
    );
  }

  // renderJobPollLost is the terminal card shown when pollJob gives up after
  // JOB_POLL_FAILURE_LIMIT consecutive *transient* failures. Deliberately not
  // phrased as "failed"/"expired" like renderJobPollError above: this path
  // only means the client gave up polling, not that the job itself ended —
  // per job.go, a running job is never evicted, so it may well still be
  // going on the server. Wording steers the operator to re-open and check
  // rather than implying the install died.
  function renderJobPollLost() {
    setResultHTML(
      '<div class="result-section"><strong>Соединение потеряно</strong>' +
      "<p>Задача могла продолжиться на сервере — откройте заново, чтобы проверить.</p></div>"
    );
  }

  // showJobPollTransient/hideJobPollTransient toggle the small badge above
  // the checklist that pollJob shows while it's retrying through non-404
  // errors. Deliberately does not touch #resultOutput — the last successfully
  // rendered checklist stays exactly as it was, since nothing about the job
  // itself is known to have changed, only that the last poll didn't land.
  function showJobPollTransient() {
    if (els.jobPollStatus) els.jobPollStatus.classList.remove("hidden");
  }

  function hideJobPollTransient() {
    if (els.jobPollStatus) els.jobPollStatus.classList.add("hidden");
  }

  // JOB_POLL_FAILURE_LIMIT bounds how many consecutive non-404 poll failures
  // (network blips, 5xx, timeouts) pollJob tolerates before it gives up and
  // shows renderJobPollLost. At the ~1500ms tick cadence that's roughly 60s
  // of unbroken connectivity loss — long enough to ride out a flaky link
  // without polling forever against a link that may never recover. Reset to
  // 0 on every successful tick (see pollJob).
  const JOB_POLL_FAILURE_LIMIT = 40;

  // pollJob polls GET /v1/dashboard/provision/{jobId} every ~1500ms, re-
  // rendering the checklist on every response, until job.state leaves
  // "running". Errors split two ways:
  //   - `err.status === 404` (job store-evicted — see renderJobPollError's
  //     comment) is genuinely terminal: show the not-found/expired card and
  //     stop.
  //   - Everything else (a plain network failure has no `.status` at all —
  //     see api()'s `fetch` call — plus any non-404 HTTP error) is treated as
  //     a transient blip: the job is still running server-side, so polling
  //     continues on the same interval and only a subtle "reconnecting"
  //     badge is shown, without disturbing the last-rendered checklist. If
  //     failures keep piling up past JOB_POLL_FAILURE_LIMIT, pollJob finally
  //     gives up via renderJobPollLost (non-alarming wording — the job may
  //     still have finished server-side, the client just stopped watching).
  // `busy` skips a tick if the previous request is still in flight, so a
  // slow response (longer than the 1500ms cadence) can never pile up
  // overlapping requests.
  function pollJob(jobId) {
    stopJobPoll();
    const token = jobPollToken;
    const poll = { timer: null, busy: false, failures: 0 };
    state.jobPoll = poll;

    const tick = async () => {
      if (jobPollToken !== token || poll.busy) return;
      poll.busy = true;
      try {
        const job = await api(`/v1/dashboard/provision/${encodeURIComponent(jobId)}`);
        if (jobPollToken !== token) return; // superseded/stopped while awaiting
        poll.busy = false;
        if (!job) {
          // api() hands back null on a 401 (it redirects to /dashboard/login
          // itself rather than throwing) — nothing to render, and no point
          // continuing to poll a session that's going away.
          stopJobPoll();
          return;
        }
        poll.failures = 0;
        hideJobPollTransient();
        renderJobProgress(job);
        if (job.state !== "running") {
          stopJobPoll();
        }
      } catch (err) {
        if (jobPollToken !== token) return;
        poll.busy = false; // must reset even on error — see busy-flag note above
        if (err && err.status === 404) {
          stopJobPoll();
          renderJobPollError();
          return;
        }
        poll.failures++;
        if (poll.failures >= JOB_POLL_FAILURE_LIMIT) {
          stopJobPoll();
          renderJobPollLost();
          return;
        }
        showJobPollTransient();
      }
    };

    poll.timer = window.setInterval(tick, 1500);
    tick();
  }

  // startJobProgress hands the seeded {job_id, steps} response from either
  // submitProvision("provision") or submitRepair() off to the SAME live
  // checklist — the design spec's Repair section is explicit that the run
  // step is "same progress view", not a second implementation. Render once
  // immediately (so the drawer never shows a blank flash before the first
  // poll resolves), then start pollJob — if a job id actually came back
  // (defensive; should always be present on a 202, but polling with an empty
  // id is nonsensical). `label` is the fixed, caller-supplied drawer-title
  // prefix ("Provision"/"Repair") — a compile-time literal, not user/server
  // data, so it needs no escaping; defaults to "Provision" only as a
  // defensive fallback for a hypothetical future caller that forgets it.
  function startJobProgress(nickname, res, label) {
    const jobID = (res && res.job_id) || "";
    els.drawerTitle.textContent = (label || "Provision") + " / " + nickname;
    renderJobProgress({ state: "running", steps: (res && res.steps) || [] });
    els.resultDrawer.classList.remove("hidden");
    if (jobID) {
      pollJob(jobID);
    }
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
    rememberFocus();
    els.deployModal.classList.remove("hidden");
    els.deployVersionInput.focus();
  }

  function closeModal() {
    els.deployModal.classList.add("hidden");
    state.deployAgent = null;
    setButtonState(els.confirmDeployBtn, "idle");
    restoreFocus();
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
    rememberFocus();
    els.editAgentModal.classList.remove("hidden");
    els.editAgentAWGMURL.focus();
  }

  function closeEditAgent() {
    els.editAgentModal.classList.add("hidden");
    state.editAgent = null;
    setButtonState(els.saveAgentBtn, "idle");
    restoreFocus();
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
    rememberFocus();
    els.agentConfigModal.classList.remove("hidden");
    els.cfgIntervalSec.focus();
  }

  function closeAgentConfig() {
    els.agentConfigModal.classList.add("hidden");
    state.agentConfigNick = null;
    setButtonState(els.applyAgentConfigBtn, "idle");
    restoreFocus();
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

  // repairShowStep switches the visible panel inside #repairModal — the same
  // vanilla show/hide pattern as provisionShowStep, over the three
  // data-repair-step panels (mode/access/run). Repair's wizard is a flat
  // 2-step form (no identity step — an existing, already-enrolled nickname
  // has nothing to (re)identify), so unlike PROVISION_BACK_MAP there is only
  // ever one back transition (access -> mode); repairGoBack below is a
  // one-line direct call rather than a lookup table for that reason.
  function repairShowStep(step) {
    state.repairStep = step;
    document.querySelectorAll("#repairModal [data-repair-step]").forEach((panel) => {
      panel.classList.toggle("hidden", panel.dataset.repairStep !== step);
    });
  }

  function repairGoBack() {
    repairShowStep("mode");
  }

  // chooseRepairMode records which repair the operator picked and reveals
  // the access step's mode-specific field: repoint gets an optional new
  // backend URL, reinstall gets an optional target version (pre-filled with
  // the known latest, mirroring openProvision's install-now default so the
  // operator isn't forced to look it up). Root password and the awgm-auth
  // disclosure are shown for both modes.
  function chooseRepairMode(mode) {
    state.repairMode = mode;
    els.repairAccessError.textContent = "";
    setButtonState(els.repairStartBtn, "idle");
    els.repairNewURLField.classList.toggle("hidden", mode !== "repoint");
    els.repairVersionField.classList.toggle("hidden", mode !== "reinstall");
    if (mode === "reinstall" && !els.repairVersion.value) {
      els.repairVersion.value = latestVersion();
    }
    repairShowStep("access");
    els.repairRootPassword.focus();
  }

  function openRepair(nickname) {
    state.repairNick = nickname;
    state.repairMode = null;
    els.repairTitle.textContent = "Repair / " + nickname;
    els.repairRootPassword.value = "";
    els.repairNewURL.value = "";
    els.repairVersion.value = "";
    els.repairAwgmLogin.value = "";
    els.repairAwgmPassword.value = "";
    els.repairAwgmApiKey.value = "";
    els.repairAccessError.textContent = "";
    els.repairNewURLField.classList.add("hidden");
    els.repairVersionField.classList.add("hidden");
    setButtonState(els.repairStartBtn, "idle");
    repairShowStep("mode");
    rememberFocus();
    els.repairModal.classList.remove("hidden");
  }

  function closeRepair() {
    els.repairModal.classList.add("hidden");
    state.repairNick = null;
    state.repairMode = null;
    setButtonState(els.repairStartBtn, "idle");
    restoreFocus();
  }

  // submitRepair posts to POST /v1/dashboard/agents/{nick}/repair for
  // whichever mode chooseRepairMode recorded, then hands the returned
  // {job_id, steps} off to the SAME shared progress view submitProvision
  // uses (startJobProgress -> pollJob -> renderJobProgress) — per the design
  // spec's "Repair modal ... Step 2 — run: -> same progress view", this is
  // not a second checklist implementation. Unlike the old synchronous
  // /revive endpoint (which could return a 502 with an inline {output,error}
  // transcript), the engine-backed /repair endpoint only ever returns
  // {job_id, steps} on success — every relay-side failure happens inside the
  // async job and is discovered by polling, so there is no special-cased
  // error status to handle here beyond the plain reject-before-job-starts
  // validation errors (bad mode, no root password, no awgm_url on file,
  // downgrade rejected, already running, etc.), all shown inline in the
  // access step exactly like submitProvision's "provision" kind.
  async function submitRepair() {
    const nickname = state.repairNick;
    const mode = state.repairMode;
    if (!nickname || !mode) return;
    if (!els.repairRootPassword.value) {
      els.repairAccessError.textContent = "Нужен root-пароль роутера";
      return;
    }
    els.repairAccessError.textContent = "";
    const payload = {
      mode,
      root_password: els.repairRootPassword.value,
      awgm_login: els.repairAwgmLogin.value.trim(),
      awgm_password: els.repairAwgmPassword.value,
      awgm_api_key: els.repairAwgmApiKey.value.trim()
    };
    if (mode === "repoint") {
      payload.new_backend_url = els.repairNewURL.value.trim();
    } else {
      payload.version = els.repairVersion.value.trim();
    }
    setButtonState(els.repairStartBtn, "waiting");
    repairShowStep("run");
    try {
      const res = await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}/repair`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload)
      });
      closeRepair();
      toast("repair запущен — job " + ((res && res.job_id) || ""));
      startJobProgress(nickname, res, "Repair");
      await refresh();
    } catch (err) {
      setButtonState(els.repairStartBtn, "error");
      repairShowStep("access");
      els.repairAccessError.textContent = err.message;
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
    const current = els.provisionGroup.value;
    const options = [`<option value="0">Primary group (${escapeHTML(telegram.primary_chat_id || "default")})</option>`];
    (telegram.extra_chat_ids || []).forEach((chatID) => {
      options.push(`<option value="${escapeAttr(chatID)}">Extra group (${escapeHTML(chatID)})</option>`);
    });
    options.push('<option value="custom">Custom group...</option>');
    els.provisionGroup.innerHTML = options.join("");
    if ([...els.provisionGroup.options].some((o) => o.value === current)) {
      els.provisionGroup.value = current;
    }
    toggleCustomGroup();
  }

  function toggleCustomGroup() {
    els.provisionCustomGroupField.classList.toggle("hidden", els.provisionGroup.value !== "custom");
  }

  function setResultHTML(html) {
    els.resultOutput.innerHTML = html;
  }

  // closeResultDrawer hides the result/progress drawer and stops any live job
  // poll. The drawer is the only surface pollJob ever renders into, so
  // closing it must always stop the interval — otherwise it would keep
  // ticking (and re-fetching) indefinitely against a hidden drawer nobody is
  // looking at. Both drawer-close entry points (the X button and Escape) call
  // this instead of toggling the hidden class directly.
  function closeResultDrawer() {
    els.resultDrawer.classList.add("hidden");
    stopJobPoll();
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
  els.provisionBtn.addEventListener("click", openProvision);
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
    // Close any open row-overflow menu unless the click is inside it (or on its toggle).
    document.querySelectorAll(".row-overflow.open").forEach((node) => {
      if (!node.contains(event.target)) node.classList.remove("open");
    });
    const button = event.target.closest("button");
    if (button) {
      if (button.dataset.overflow) {
        const wrap = button.closest(".row-overflow");
        if (wrap) wrap.classList.toggle("open");
        return;
      }
      if (button.dataset.drawerTab) {
        state.drawerTab = button.dataset.drawerTab;
        renderSelectedDrawer();
        return;
      }
      if (button.hasAttribute("data-copy-dns")) {
        copyDNSReference();
        return;
      }
      if (isButtonBusy(button)) return;
      const nickname = button.dataset.agent || button.dataset.deploy;
      if (button.dataset.editAgent) openEditAgent(button.dataset.editAgent);
      if (button.dataset.agentConfig) openAgentConfig(button.dataset.agentConfig);
      if (button.dataset.repair) openRepair(button.dataset.repair);
      if (button.dataset.deploy) openDeploy(nickname);
      if (button.dataset.cancelDeploy) cancelDeploy(button.dataset.cancelDeploy).catch((err) => toast(err.message));
      if (button.dataset.updateLatest) deployLatest(nickname).catch((err) => {
        setActionState(nickname, "self_update", "error");
        toast(err.message);
      });
      if (button.dataset.command) {
        if (button.dataset.command === "dns_reset" && !window.confirm("Сбросить DNS к эталонным DoT?\n\nТекущие dns-proxy upstream'ы будут удалены и заменены эталонным набором, конфиг сохранён. Команды выполнит агент на роутере.")) return;
        enqueueCommand(nickname, button.dataset.command, commandArgs(button)).catch((err) => {
          setActionState(nickname, button.dataset.command, "error");
          toast(err.message);
        });
      }
      if (button.dataset.maint) restartAWGM(nickname).catch((err) => {
        setActionState(nickname, "awgmgr", "error");
        toast(err.message);
      });
      return;
    }
    const row = event.target.closest("[data-row-agent]");
    if (row) {
      if (state.selected !== row.dataset.rowAgent) state.drawerTab = "overview";
      state.selected = row.dataset.rowAgent;
      renderSelectedDrawer();
      maybeAutoCheckCron(state.selected);
    }
  });
  document.querySelectorAll("[data-close-modal]").forEach((button) => button.addEventListener("click", closeModal));
  document.querySelectorAll("[data-close-provision]").forEach((button) => button.addEventListener("click", closeProvision));
  document.querySelectorAll("[data-close-edit-agent]").forEach((button) => button.addEventListener("click", closeEditAgent));
  document.querySelectorAll("[data-close-agent-config]").forEach((button) => button.addEventListener("click", closeAgentConfig));
  document.querySelectorAll("[data-close-repair]").forEach((button) => button.addEventListener("click", closeRepair));
  els.confirmDeployBtn.addEventListener("click", () => deployAgent().catch((err) => {
    setButtonState(els.confirmDeployBtn, "error");
    toast(err.message);
  }));
  document.querySelectorAll("#provisionModal [data-provision-mode]").forEach((button) => {
    button.addEventListener("click", () => chooseProvisionMode(button.dataset.provisionMode));
  });
  document.querySelectorAll("#provisionModal [data-provision-back]").forEach((button) => {
    button.addEventListener("click", provisionGoBack);
  });
  els.provisionIdentityNextBtn.addEventListener("click", provisionIdentityNext);
  els.provisionStartBtn.addEventListener("click", () => submitProvision("provision").catch((err) => {
    setButtonState(els.provisionStartBtn, "error");
    els.provisionAccessError.textContent = err.message;
  }));
  els.saveAgentBtn.addEventListener("click", () => saveAgentEdit().catch((err) => {
    setButtonState(els.saveAgentBtn, "error");
    els.editAgentError.textContent = err.message;
  }));
  els.applyAgentConfigBtn.addEventListener("click", () => applyAgentConfig().catch((err) => {
    setButtonState(els.applyAgentConfigBtn, "error");
    els.agentConfigError.textContent = err.message;
  }));
  document.querySelectorAll("#repairModal [data-repair-mode]").forEach((button) => {
    button.addEventListener("click", () => chooseRepairMode(button.dataset.repairMode));
  });
  document.querySelectorAll("#repairModal [data-repair-back]").forEach((button) => {
    button.addEventListener("click", repairGoBack);
  });
  els.repairStartBtn.addEventListener("click", () => submitRepair().catch((err) => {
    setButtonState(els.repairStartBtn, "error");
    els.repairAccessError.textContent = err.message;
  }));
  els.provisionGroup.addEventListener("change", toggleCustomGroup);
  els.closeDrawerBtn.addEventListener("click", closeResultDrawer);
  els.autoRefreshBtn.addEventListener("click", () => setAutoRefresh(!state.autoRefresh));
  els.backendUpdateBtn.addEventListener("click", () => deployBackend().catch((err) => {
    setButtonState(els.backendUpdateBtn, "error");
    toast(err.message);
    window.setTimeout(() => setButtonState(els.backendUpdateBtn, "idle"), 3000);
  }));

  // Keyboard: Esc closes the top-most overlay; Tab traps focus inside an open
  // modal (see trapModalTab); "/" focuses search.
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      if (!els.repairModal.classList.contains("hidden")) { closeRepair(); return; }
      if (!els.agentConfigModal.classList.contains("hidden")) { closeAgentConfig(); return; }
      if (!els.editAgentModal.classList.contains("hidden")) { closeEditAgent(); return; }
      if (!els.provisionModal.classList.contains("hidden")) { closeProvision(); return; }
      if (!els.deployModal.classList.contains("hidden")) { closeModal(); return; }
      if (!els.resultDrawer.classList.contains("hidden")) { closeResultDrawer(); return; }
      return;
    }
    if (event.key === "Tab") {
      trapModalTab(event);
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
