const $ = (id) => document.getElementById(id);
let config = {},
  devices = [],
  incidents = [],
  selected = "",
  activeIncident = null,
  polling = false,
  loggedIn = false;
const percent = (n) => `${Number(n).toFixed(1)}%`;
const time = (value) =>
  new Date(value).toLocaleTimeString([], {
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
const sourceName = (s) =>
  ({
    simulated: "Simulated",
    linux: "Linux · live",
    windows: "Windows · live",
    snmp: "SNMP · live",
    external: "External collector",
    unknown: "Unspecified source",
  })[s] || "Unspecified source";
function node(tag, text, className) {
  const el = document.createElement(tag);
  if (text !== undefined) el.textContent = text;
  if (className) el.className = className;
  return el;
}
async function api(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...options.headers },
  });
  let data;
  try {
    data = await response.json();
  } catch {
    throw new Error("Server returned an invalid response");
  }
  if (response.status === 401) {
    showLogin();
    throw new Error("Session expired. Sign in again.");
  }
  if (!response.ok)
    throw new Error(data.error || `Request failed (${response.status})`);
  return data;
}
function notify(message) {
  $("notice").textContent = message;
  $("notice").hidden = !message;
}
function showLogin() {
  loggedIn = false;
  $("login").hidden = false;
  $("app").hidden = true;
  $("detail").close();
}
async function start() {
  config = await api("/api/config");
  loggedIn = true;
  $("login").hidden = true;
  $("app").hidden = false;
  $("ai-mode").textContent = config.ai_enabled ? "Local AI" : "Disabled";
  $("ai-model").textContent = config.ai_enabled
    ? config.model
    : "Enable in environment";
  await refresh();
}
$("login-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  $("login-error").textContent = "";
  try {
    await api("/api/login", {
      method: "POST",
      body: JSON.stringify({ token: $("token").value }),
    });
    $("token").value = "";
    await start();
  } catch (err) {
    $("login-error").textContent = err.message;
  }
});
$("logout").onclick = async () => {
  try {
    await api("/api/logout", { method: "POST" });
    showLogin();
  } catch (err) {
    notify(err.message);
  }
};
$("refresh").onclick = () => refresh();
$("status-filter").onchange = () => refresh();
async function refresh() {
  if (!loggedIn || polling) return;
  polling = true;
  try {
    [devices, incidents] = await Promise.all([
      api("/api/devices"),
      api("/api/incidents?limit=100"),
    ]);
    if (!devices.some((d) => d.device_id === selected))
      selected = devices[0]?.device_id || "";
    $("device-count").textContent = devices.length;
    $("device-note").textContent =
      `${devices.filter((d) => !d.stale).length} sending recent readings`;
    $("avg-cpu").textContent = devices.length
      ? percent(
          devices.reduce((sum, d) => sum + d.cpu_percent, 0) / devices.length,
        )
      : "—";
    $("open-count").textContent = incidents.filter(
      (i) => i.status === "open",
    ).length;
    $("connection").textContent = "● Connected";
    $("connection").className = "badge";
    $("updated").textContent = `Updated ${time(Date.now())}`;
    $("last-poll").textContent = "Refreshes every 5 seconds";
    notify("");
    renderDevices();
    renderIncidents();
    await history();
    if (activeIncident) {
      const fresh = incidents.find((i) => i.id === activeIncident.id);
      if (fresh) renderDetail(fresh);
    }
  } catch (err) {
    if (loggedIn) {
      notify(err.message);
      $("connection").textContent = "Reconnecting";
      $("connection").className = "badge warning";
    }
  } finally {
    polling = false;
  }
}
function renderDevices() {
  $("devices").replaceChildren();
  if (!devices.length) {
    $("devices").append(
      node(
        "p",
        "No readings yet. Start the simulator or a real collector.",
        "muted",
      ),
    );
    return;
  }
  for (const d of devices) {
    const card = node(
      "button",
      undefined,
      `device-card${d.device_id === selected ? " selected" : ""}`,
    );
    card.type = "button";
    card.setAttribute("aria-pressed", String(d.device_id === selected));
    const top = node("div", undefined, "device-top");
    top.append(
      node("span", d.device_id, "device-name"),
      node(
        "span",
        d.stale ? "Stale" : d.cpu_percent > 90 ? "High CPU" : "Reporting",
        `badge ${d.stale ? "neutral" : d.cpu_percent > 90 ? "warning" : ""}`,
      ),
    );
    card.append(top);
    for (const [label, value] of [
      ["CPU", d.cpu_percent],
      ["Memory", d.memory_percent],
      ["Probe / simulated loss", d.packet_loss_pct],
    ]) {
      const row = node("div", undefined, "metric-row");
      row.append(node("span", label), node("b", percent(value)));
      card.append(row);
      if (label === "CPU") {
        const p = node("progress");
        p.max = 100;
        p.value = value;
        p.setAttribute("aria-label", "CPU usage");
        card.append(p);
      }
    }
    card.append(
      node(
        "div",
        `${sourceName(d.source)} · ${time(d.timestamp)}`,
        "device-source",
      ),
    );
    card.onclick = () => {
      selected = d.device_id;
      renderDevices();
      history().catch((err) => notify(err.message));
    };
    $("devices").append(card);
  }
}
function svg(tag, attrs) {
  const el = document.createElementNS("http://www.w3.org/2000/svg", tag);
  for (const [k, v] of Object.entries(attrs)) el.setAttribute(k, String(v));
  return el;
}
async function history() {
  const requested = selected;
  const readings = requested
    ? (
        await api(
          `/api/devices/${encodeURIComponent(requested)}/history?limit=120`,
        )
      ).reverse()
    : [];
  if (requested !== selected) return;
  $("chart").replaceChildren();
  $("chart-empty").hidden = readings.length > 0;
  $("chart-title").textContent = requested
    ? `${requested} · telemetry`
    : "Telemetry timeline";
  for (const y of [0, 25, 50, 75, 100]) {
    const py = 190 - y * 1.7;
    $("chart").append(
      svg("line", {
        x1: 35,
        y1: py,
        x2: 880,
        y2: py,
        stroke: "#263342",
        "stroke-width": 1,
      }),
    );
    const label = svg("text", {
      x: 0,
      y: py + 4,
      fill: "#91a4b5",
      "font-size": 10,
    });
    label.textContent = y;
    $("chart").append(label);
  }
  $("chart").append(
    svg("line", {
      x1: 35,
      y1: 37,
      x2: 880,
      y2: 37,
      stroke: "#a57643",
      "stroke-dasharray": "5 5",
    }),
  );
  for (const [field, color] of [
    ["cpu_percent", "#5ce0ba"],
    ["memory_percent", "#7fafff"],
  ]) {
    const points = readings
      .map(
        (m, i) =>
          `${35 + (i * 845) / Math.max(1, readings.length - 1)},${190 - m[field] * 1.7}`,
      )
      .join(" ");
    if (points)
      $("chart").append(
        svg("polyline", {
          points,
          fill: "none",
          stroke: color,
          "stroke-width": 2.5,
          "stroke-linejoin": "round",
        }),
      );
    if (readings.length === 1)
      $("chart").append(
        svg("circle", {
          cx: 35,
          cy: 190 - readings[0][field] * 1.7,
          r: 4,
          fill: color,
        }),
      );
  }
  $("chart-start").textContent = readings.length
    ? time(readings[0].timestamp)
    : "—";
  $("chart-end").textContent = readings.length
    ? time(readings.at(-1).timestamp)
    : "—";
}
function renderIncidents() {
  $("incident-list").replaceChildren();
  const filter = $("status-filter").value;
  const items = incidents.filter((i) => !filter || i.status === filter);
  if (!items.length) {
    $("incident-list").append(
      node("div", "No incidents in this view.", "empty-state"),
    );
    return;
  }
  for (const incident of items) {
    const row = node("button", undefined, "incident-row");
    row.type = "button";
    const main = node("div", undefined, "incident-main");
    main.append(node("span", "!", "incident-icon"));
    const text = node("div");
    text.append(
      node("h3", `${incident.device_id} · High CPU`),
      node(
        "p",
        `${percent(incident.cpu_percent)} observed · ${time(incident.observed_at)} · ${sourceName(incident.source)}`,
      ),
    );
    main.append(text);
    const badges = node("div", undefined, "incident-badges");
    badges.append(
      node(
        "span",
        incident.status,
        `badge ${incident.status === "open" ? "warning" : "neutral"}`,
      ),
      node(
        "span",
        incident.analysis_status === "complete"
          ? "AI reviewed"
          : `AI ${incident.analysis_status}`,
        "badge neutral",
      ),
    );
    row.append(main, badges);
    row.onclick = () => {
      renderDetail(incident);
      $("detail").showModal();
    };
    $("incident-list").append(row);
  }
}
function section(title, content) {
  const heading = node("h3", title);
  $("detail-body").append(heading);
  if (Array.isArray(content)) {
    const list = node("ul");
    content.forEach((text) => list.append(node("li", text)));
    $("detail-body").append(list);
  } else $("detail-body").append(node("p", content));
}
function renderDetail(incident) {
  activeIncident = incident;
  $("detail-title").textContent = `${incident.device_id} · High CPU`;
  $("detail-meta").textContent =
    `${percent(incident.cpu_percent)} / ${percent(incident.threshold)} threshold · ${sourceName(incident.source)} · ${new Date(incident.observed_at).toLocaleString()} · ${incident.status}`;
  $("detail-body").replaceChildren();
  if (incident.analysis_status === "complete" && incident.analysis) {
    const a = incident.analysis;
    section("Analysis", a.summary);
    section("Evidence", a.evidence);
    section("Possible causes · unconfirmed", a.possible_causes);
    section("Suggested checks", a.suggested_checks);
    section("Missing information", a.missing_information);
    section(
      "Operator review",
      `Generated by ${a.model}. Suggestions are advisory; no changes were executed.`,
    );
  } else {
    section(
      "Observed evidence",
      `CPU reached ${percent(incident.cpu_percent)}, exceeding the configured ${percent(incident.threshold)} threshold. A single reading does not establish a root cause.`,
    );
    section(
      "Analysis status",
      incident.analysis_status === "disabled"
        ? "Local AI is disabled. Enable AI in your environment to request an analysis."
        : incident.analysis_status === "error"
          ? "Analysis could not be completed. Check the model service and request another attempt."
          : "Analysis is queued or running. This panel updates automatically.",
    );
    if (incident.analysis_error)
      section("Last analysis error", incident.analysis_error);
  }
  $("resolve").textContent =
    incident.status === "open" ? "Resolve incident" : "Reopen incident";
  $("reanalyze").disabled =
    !config.ai_enabled ||
    ["pending", "running"].includes(incident.analysis_status);
}
$("close-detail").onclick = () => {
  $("detail").close();
  activeIncident = null;
};
$("detail").addEventListener("close", () => (activeIncident = null));
$("resolve").onclick = async () => {
  if (!activeIncident) return;
  try {
    await api(`/api/incidents/${encodeURIComponent(activeIncident.id)}`, {
      method: "PATCH",
      body: JSON.stringify({
        status: activeIncident.status === "open" ? "resolved" : "open",
      }),
    });
    await refresh();
  } catch (err) {
    notify(err.message);
  }
};
$("reanalyze").onclick = async () => {
  if (!activeIncident) return;
  try {
    await api(
      `/api/incidents/${encodeURIComponent(activeIncident.id)}/analyze`,
      { method: "POST" },
    );
    await refresh();
  } catch (err) {
    notify(err.message);
  }
};
setInterval(() => {
  if (loggedIn) refresh();
}, 5000);
start().catch(() => showLogin());
