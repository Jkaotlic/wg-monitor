(function () {
  const state = {
    summary: null,
    filter: "all",
    query: "",
    selected: null,
    deployAgent: null,
    buttonStates: new Map()
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
    toast: document.getElementById("toast")
  };

  function setAuth(ok) {
    els.authDot.classList.toggle("ok", ok);
    els.authState.textContent = ok ? "unlocked" : "locked";
  }

  function setButtonState(button, value) {
    if (!button) return;
    button.dataset.state = value || "idle";
    button.disabled = value === "waiting";
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
      throw new Error(message);
    }
    return body;
  }

  async function refresh() {
    setButtonState(els.refreshBtn, "waiting");
    try {
      const summary = await api("/v1/dashboard/summary");
      state.summary = summary;
      setAuth(true);
      setButtonState(els.refreshBtn, "ok");
      render();
      window.setTimeout(() => setButtonState(els.refreshBtn, "idle"), 700);
    } catch (err) {
      setAuth(false);
      setButtonState(els.refreshBtn, "error");
      renderError(err.message);
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
    els.kpiOnlineText.textContent = (totals.online || 0) === 1 ? "1 agent reporting" : (totals.online || 0) + " agents reporting";
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
            <button class="mini-btn" type="button" data-state="${buttonState(agent.nickname, "diag_now")}" data-command="diag_now" data-agent="${escapeAttr(agent.nickname)}">Diag</button>
            <button class="mini-btn" type="button" data-state="${buttonState(agent.nickname, "force_recheck")}" data-command="force_recheck" data-agent="${escapeAttr(agent.nickname)}">Check</button>
            <button class="mini-btn" type="button" data-state="${buttonState(agent.nickname, "tunnels_status")}" data-command="tunnels_status" data-agent="${escapeAttr(agent.nickname)}">Tunnels</button>
            <button class="mini-btn danger" type="button" data-state="${buttonState(agent.nickname, "awgmgr")}" data-maint="awgmgr" data-agent="${escapeAttr(agent.nickname)}">AWGM</button>
            ${latestDeployButton(agent, "mini-btn primary", "Latest")}
            <button class="mini-btn" type="button" data-deploy="${escapeAttr(agent.nickname)}">Custom</button>
          </div>
        </td>
      </tr>
    `;
  }

  function latestDeployButton(agent, className, label) {
    const latest = latestVersion();
    const current = agent.agent_version || "";
    const pending = agent.pending_version || "";
    const disabled = !latest || current === latest || pending === latest;
    const stateValue = pending === latest ? "waiting" : buttonState(agent.nickname, "self_update");
    const suffix = latest ? ` ${latest}` : "";
    const text = pending === latest ? `Pending${suffix}` : (current === latest ? `Latest${suffix}` : label);
    return `<button class="${escapeAttr(className)}" title="Update to latest${escapeAttr(suffix)}" type="button" data-state="${escapeAttr(stateValue)}" data-update-latest="1" data-agent="${escapeAttr(agent.nickname)}" ${disabled ? "disabled" : ""}>${escapeHTML(text)}</button>`;
  }

  function statusBadge(agent) {
    if (agent.status === "alert") return '<span class="badge badge-danger">alert</span>';
    if (agent.status === "online") return '<span class="badge badge-success">online</span>';
    return '<span class="badge badge-muted">offline</span>';
  }

  function statusText(agent) {
    if (agent.status === "alert") {
      const first = (agent.active_incidents || [])[0];
      if (first) return `${first.check_name}: ${first.fail_count || 1} fail подряд`;
      return "есть hard-инцидент";
    }
    if (agent.status === "online") return "последний отчет " + formatLastSeen(agent);
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
    const awgBlock = selected.awgm_url
      ? `<a class="action-btn primary" href="${escapeAttr(selected.awgm_url)}" target="_blank" rel="noreferrer noopener"><span class="ti ti-external-link"></span>Open AWG Manager</a><p class="drawer-note">Откроется веб-интерфейс AWG Manager. Логин остается на стороне AWG Manager.</p>`
      : `<button class="action-btn disabled" type="button" disabled><span class="ti ti-external-link"></span>Open AWG</button><p class="drawer-note">AWG Manager URL не сохранен. Добавь его через deploy sync или awgm-url-patch.</p>`;
    els.agentDrawer.innerHTML = `
      <div class="drawer-card">
        <div>
          <div class="eyebrow">agent detail</div>
          <h2>${escapeHTML(selected.nickname)}</h2>
          <p class="drawer-note">${escapeHTML(statusLongText(selected))}</p>
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
          </div>
        </section>
        <section class="drawer-section">
          <h3>Checks</h3>
          <div class="drawer-actions">
            ${drawerCommandButton(selected, "diag_now", "Diagnostics")}
            ${drawerCommandButton(selected, "force_recheck", "Force recheck")}
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
      </div>
    `;
  }

  function statusLongText(agent) {
    if (agent.status === "alert") return "Есть hard-инцидент. Начни с Diagnostics или Force recheck, затем смотри результат команды.";
    if (agent.status === "online") return "Агент онлайн: последний отчет " + formatLastSeen(agent) + ". Активных hard-инцидентов нет.";
    if (!agent.last_seen_at) return "Агент создан, но еще ни разу не прислал отчет.";
    return "Агент сейчас offline или давно не отчитывался. Последний отчет " + formatLastSeen(agent) + ".";
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
        setActionState(nickname, title, res.status === "ok" ? "ok" : "error");
        refresh();
        return;
      } catch (err) {
        setResultHTML(`<div class="result-section"><strong>Waiting for agent</strong><p>Command id: ${escapeHTML(cmdID)}</p><p>${escapeHTML(err.message)}</p></div>`);
        await sleep(1200);
      }
    }
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
      const nickname = button.dataset.agent || button.dataset.deploy;
      if (button.dataset.deploy) openDeploy(nickname);
      if (button.dataset.updateLatest) deployLatest(nickname).catch((err) => {
        setActionState(nickname, "self_update", "error");
        toast(err.message);
      });
      if (button.dataset.command) enqueueCommand(nickname, button.dataset.command).catch((err) => {
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
  els.confirmDeployBtn.addEventListener("click", () => deployAgent().catch((err) => {
    setButtonState(els.confirmDeployBtn, "error");
    toast(err.message);
  }));
  els.createAgentBtn.addEventListener("click", createEnrollment);
  els.newAgentGroup.addEventListener("change", toggleCustomGroup);
  els.closeDrawerBtn.addEventListener("click", () => els.resultDrawer.classList.add("hidden"));

  refresh();
})();
