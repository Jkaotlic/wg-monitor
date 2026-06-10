(function () {
  const state = {
    token: localStorage.getItem("wgDashboardToken") || "",
    summary: null,
    filter: "all",
    query: "",
    deployAgent: null
  };

  const els = {
    tokenInput: document.getElementById("tokenInput"),
    saveTokenBtn: document.getElementById("saveTokenBtn"),
    refreshBtn: document.getElementById("refreshBtn"),
    authDot: document.getElementById("authDot"),
    authState: document.getElementById("authState"),
    kpiAgents: document.getElementById("kpiAgents"),
    kpiOnline: document.getElementById("kpiOnline"),
    kpiAlerts: document.getElementById("kpiAlerts"),
    kpiDeploys: document.getElementById("kpiDeploys"),
    kpiVersion: document.getElementById("kpiVersion"),
    agentsBody: document.getElementById("agentsBody"),
    searchInput: document.getElementById("searchInput"),
    deployModal: document.getElementById("deployModal"),
    deployTitle: document.getElementById("deployTitle"),
    deployVersionInput: document.getElementById("deployVersionInput"),
    confirmDeployBtn: document.getElementById("confirmDeployBtn"),
    resultDrawer: document.getElementById("resultDrawer"),
    drawerTitle: document.getElementById("drawerTitle"),
    resultOutput: document.getElementById("resultOutput"),
    closeDrawerBtn: document.getElementById("closeDrawerBtn"),
    toast: document.getElementById("toast")
  };

  const params = new URLSearchParams(window.location.search);
  const urlToken = params.get("token");
  if (urlToken) {
    state.token = urlToken;
    localStorage.setItem("wgDashboardToken", urlToken);
    params.delete("token");
    const next = window.location.pathname + (params.toString() ? "?" + params.toString() : "");
    window.history.replaceState({}, "", next);
  }
  els.tokenInput.value = state.token;

  function setAuth(ok) {
    els.authDot.classList.toggle("ok", ok);
    els.authState.textContent = ok ? "unlocked" : "locked";
  }

  function toast(text) {
    els.toast.textContent = text;
    els.toast.classList.remove("hidden");
    window.clearTimeout(toast.timer);
    toast.timer = window.setTimeout(() => els.toast.classList.add("hidden"), 3000);
  }

  async function api(path, options) {
    if (!state.token) {
      throw new Error("token required");
    }
    const init = Object.assign({ headers: {} }, options || {});
    init.headers = Object.assign({}, init.headers, { Authorization: "Bearer " + state.token });
    const res = await fetch(path, init);
    const ct = res.headers.get("content-type") || "";
    const body = ct.includes("application/json") ? await res.json() : await res.text();
    if (!res.ok) {
      const message = body && body.message ? body.message : String(body || res.statusText);
      throw new Error(message);
    }
    return body;
  }

  async function refresh() {
    try {
      const summary = await api("/v1/dashboard/summary");
      state.summary = summary;
      setAuth(true);
      render();
    } catch (err) {
      setAuth(false);
      renderError(err.message);
    }
  }

  function render() {
    const summary = state.summary;
    if (!summary) {
      renderEmpty("No data");
      return;
    }
    els.kpiAgents.textContent = summary.totals.agents;
    els.kpiOnline.textContent = summary.totals.online;
    els.kpiAlerts.textContent = summary.totals.alerts;
    els.kpiDeploys.textContent = summary.totals.pending_deploys;
    els.kpiVersion.textContent = summary.version || "version unknown";

    const agents = (summary.agents || []).filter((agent) => {
      const query = state.query.toLowerCase();
      const matchesQuery = !query || [agent.nickname, agent.kind, agent.agent_version, agent.expected_exit_ip]
        .join(" ")
        .toLowerCase()
        .includes(query);
      const matchesFilter = state.filter === "all" || agent.status === state.filter;
      return matchesQuery && matchesFilter;
    });

    if (!agents.length) {
      renderEmpty("No matching agents");
      return;
    }
    els.agentsBody.innerHTML = agents.map(agentRow).join("");
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
    renderEmpty(text || "Request failed");
  }

  function agentRow(agent) {
    const incidents = (agent.active_incidents || []).map((incident) => {
      return `<span class="badge badge-danger">${escapeHTML(incident.check_name)} ${incident.fail_count || ""}</span>`;
    }).join("");
    const pending = agent.pending_version
      ? `<span class="badge badge-warning">${escapeHTML(agent.pending_version)}</span>`
      : `<span class="badge badge-muted">idle</span>`;
    return `
      <tr>
        <td>
          <div class="agent-main">
            <span class="agent-name">${escapeHTML(agent.nickname)}</span>
            <span class="agent-meta">${escapeHTML(agent.kind || "static")} / ${escapeHTML(agent.awg_iface || "-")} / ${escapeHTML(agent.expected_exit_ip || "-")}</span>
          </div>
        </td>
        <td>${statusBadge(agent.status)}</td>
        <td>${escapeHTML(formatLastSeen(agent))}</td>
        <td>${escapeHTML(agent.agent_version || "-")}</td>
        <td>${pending}</td>
        <td><div class="incident-list">${incidents || '<span class="badge badge-success">clear</span>'}</div></td>
        <td>
          <div class="action-strip">
            <button class="mini-btn" data-command="diag_now" data-agent="${escapeAttr(agent.nickname)}">Diag</button>
            <button class="mini-btn" data-command="force_recheck" data-agent="${escapeAttr(agent.nickname)}">Check</button>
            <button class="mini-btn" data-command="tunnels_status" data-agent="${escapeAttr(agent.nickname)}">Tunnels</button>
            <button class="mini-btn danger" data-maint="awgmgr" data-agent="${escapeAttr(agent.nickname)}">AWGM</button>
            <button class="mini-btn primary" data-deploy="${escapeAttr(agent.nickname)}">Deploy</button>
          </div>
        </td>
      </tr>
    `;
  }

  function statusBadge(status) {
    if (status === "alert") return '<span class="badge badge-danger">alert</span>';
    if (status === "online") return '<span class="badge badge-success">online</span>';
    return '<span class="badge badge-muted">offline</span>';
  }

  function formatLastSeen(agent) {
    if (!agent.last_seen_at) return "never";
    if (typeof agent.last_seen_age_sec === "number") {
      const sec = agent.last_seen_age_sec;
      if (sec < 90) return sec + "s ago";
      const min = Math.round(sec / 60);
      if (min < 90) return min + "m ago";
      const hour = Math.round(min / 60);
      return hour + "h ago";
    }
    return agent.last_seen_at;
  }

  async function enqueueCommand(nickname, action, args) {
    const body = JSON.stringify({ action, args: args || {} });
    const res = await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}/commands`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body
    });
    toast(action + " queued");
    pollResult(nickname, res.cmd_id, action);
  }

  async function restartAWGM(nickname) {
    const res = await api(`/v1/dashboard/agents/${encodeURIComponent(nickname)}/maintenance`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name: "awgmgr" })
    });
    toast("awgmgr restart queued");
    pollResult(nickname, res.cmd_id, "service_restart");
  }

  async function deployAgent() {
    const version = els.deployVersionInput.value.trim();
    if (!version || !state.deployAgent) return;
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

  async function pollResult(nickname, cmdID, title) {
    if (!cmdID) return;
    els.drawerTitle.textContent = title + " / " + nickname;
    els.resultOutput.textContent = "waiting for " + cmdID;
    els.resultDrawer.classList.remove("hidden");
    for (let i = 0; i < 12; i++) {
      try {
        const res = await api(`/v1/dashboard/commands/${encodeURIComponent(cmdID)}?nickname=${encodeURIComponent(nickname)}&wait_sec=5`);
        els.resultOutput.textContent = JSON.stringify(res, null, 2);
        refresh();
        return;
      } catch (err) {
        els.resultOutput.textContent = "waiting for " + cmdID + "\n" + err.message;
        await sleep(1200);
      }
    }
  }

  function openDeploy(nickname) {
    state.deployAgent = nickname;
    els.deployTitle.textContent = "Deploy " + nickname;
    els.deployVersionInput.value = "";
    els.deployModal.classList.remove("hidden");
    els.deployVersionInput.focus();
  }

  function closeModal() {
    els.deployModal.classList.add("hidden");
    state.deployAgent = null;
  }

  function sleep(ms) {
    return new Promise((resolve) => window.setTimeout(resolve, ms));
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

  els.saveTokenBtn.addEventListener("click", () => {
    state.token = els.tokenInput.value.trim();
    localStorage.setItem("wgDashboardToken", state.token);
    refresh();
  });

  els.refreshBtn.addEventListener("click", refresh);
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
  els.agentsBody.addEventListener("click", (event) => {
    const target = event.target.closest("button");
    if (!target) return;
    const nickname = target.dataset.agent || target.dataset.deploy;
    if (target.dataset.deploy) openDeploy(nickname);
    if (target.dataset.command) enqueueCommand(nickname, target.dataset.command).catch((err) => toast(err.message));
    if (target.dataset.maint) restartAWGM(nickname).catch((err) => toast(err.message));
  });
  document.querySelectorAll("[data-close-modal]").forEach((button) => button.addEventListener("click", closeModal));
  els.confirmDeployBtn.addEventListener("click", () => deployAgent().catch((err) => toast(err.message)));
  els.closeDrawerBtn.addEventListener("click", () => els.resultDrawer.classList.add("hidden"));

  if (state.token) {
    refresh();
  } else {
    setAuth(false);
    renderError("Token required");
  }
})();
