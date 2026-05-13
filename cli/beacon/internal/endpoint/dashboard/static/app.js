const state = {
  status: null,
  summary: null,
  events: [],
  eventResult: null,
  loading: false,
  error: null,
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => Array.from(document.querySelectorAll(selector));
const formFields = [
  "q",
  "harness",
  "action",
  "severity",
  "category",
  "repository",
  "session",
  "file",
  "command",
  "mcp",
  "approval",
  "decision",
  "policy",
  "wazuh_level",
  "since",
  "limit",
  "review",
];

const presets = {
  review: { review: "true", limit: "500" },
  commands: { action: "command.executed", limit: "500" },
  files: { action: "file.modified", limit: "500" },
  mcp: { action: "mcp.tool_invoked", limit: "500" },
  approvals: { category: "approval", limit: "500" },
  failures: { action: "tool.failed", limit: "500" },
  high: { severity: "high", limit: "500" },
};

async function getJSON(path) {
  const response = await fetch(path);
  if (!response.ok) {
    let message = `${response.status} ${response.statusText}`;
    try {
      const body = await response.json();
      if (body.error) message = body.error;
    } catch (_) {
      // Keep the HTTP status when the body is not JSON.
    }
    throw new Error(message);
  }
  return response.json();
}

function queryFromFilters() {
  const data = new FormData($("#filters"));
  const params = new URLSearchParams();
  for (const [key, value] of data.entries()) {
    const trimmed = String(value).trim();
    if (trimmed) params.set(key, trimmed);
  }
  return params.toString();
}

function hydrateFiltersFromURL() {
  const params = new URLSearchParams(window.location.search);
  for (const field of formFields) {
    const input = $(`[name="${field}"]`);
    if (!input) continue;
    if (params.has(field)) input.value = params.get(field);
  }
}

function updateURL(query) {
  const next = query ? `${window.location.pathname}?${query}` : window.location.pathname;
  window.history.replaceState(null, "", next);
}

async function load({ updateLocation = false } = {}) {
  const query = queryFromFilters();
  if (updateLocation) updateURL(query);
  const suffix = query ? `?${query}` : "";
  state.loading = true;
  state.error = null;
  renderLoading();
  try {
    const [status, summary, events] = await Promise.all([
      getJSON("/api/status"),
      getJSON(`/api/summary${suffix}`),
      getJSON(`/api/events${suffix}`),
    ]);
    state.status = status;
    state.summary = summary;
    state.eventResult = events;
    state.events = events.events || [];
  } catch (err) {
    state.error = err;
  } finally {
    state.loading = false;
    render();
  }
}

function render() {
  $("#log-path").textContent = state.status?.log_path || "Runtime log unavailable";
  $("#retention").textContent = `retention: ${state.status?.content_retention || "metadata"}`;
  renderCards();
  renderInsights();
  renderSearchState();
  renderHarnesses();
  renderEvents();
}

function renderCards() {
  const cards = [
    ["Events", state.summary?.total_events || 0],
    ["Needs Review", state.summary?.needs_review_events || 0, "danger"],
    ["High/Critical", (state.summary?.high_severity_events || 0) + (state.summary?.critical_severity_events || 0), "danger"],
    ["Denied/Blocked", (state.summary?.denied_approval_events || 0) + (state.summary?.policy_blocked_events || 0), "danger"],
    ["Failed Tools", state.summary?.failed_tool_events || 0, "warn"],
    ["Sessions", state.summary?.active_sessions || 0],
    ["Commands", state.summary?.command_events || 0],
    ["Files", state.summary?.file_events || 0],
    ["MCP", state.summary?.mcp_events || 0],
    ["Approvals", state.summary?.approval_events || 0],
    ["Malformed", state.summary?.malformed_lines || 0],
  ];
  $("#cards").innerHTML = cards
    .map(([label, value, tone]) => `<div class="card ${tone ? `card-${tone}` : ""}"><span class="muted">${escapeHTML(label)}</span><span class="value">${value}</span></div>`)
    .join("");
}

function renderInsights() {
  const sections = [
    ["Top Actions", state.summary?.top_actions || []],
    ["Top Repositories", state.summary?.top_repositories || []],
    ["MCP Servers", state.summary?.top_mcp_servers || []],
  ];
  $("#insights").innerHTML = sections
    .map(([title, values]) => `
      <div class="panel insight">
        <h2>${escapeHTML(title)}</h2>
        <div class="stack">
          ${values.length ? values.map((item) => `<button type="button" data-insight="${escapeHTML(title)}" data-value="${escapeHTML(item.name)}"><span>${escapeHTML(item.name)}</span><strong>${item.count}</strong></button>`).join("") : `<span class="muted">No data yet</span>`}
        </div>
      </div>
    `)
    .join("");
  $$("[data-insight]").forEach((button) => {
    button.addEventListener("click", () => applyInsight(button.dataset.insight, button.dataset.value));
  });
}

function renderSearchState() {
  const result = state.eventResult || {};
  const returned = result.returned ?? state.events.length;
  const total = result.total_matched ?? 0;
  $("#result-meta").textContent = state.loading ? "Loading..." : `${returned} shown of ${total} matched`;

  const notice = $("#notice");
  if (state.error) {
    notice.hidden = false;
    notice.textContent = `Failed to load dashboard: ${state.error.message}`;
  } else if (result.truncated) {
    notice.hidden = false;
    notice.textContent = `Showing the latest ${returned} matching events. Narrow the search or raise the limit to see more.`;
  } else if (result.malformed_lines) {
    notice.hidden = false;
    notice.textContent = `${result.malformed_lines} malformed log line${result.malformed_lines === 1 ? "" : "s"} skipped.`;
  } else {
    notice.hidden = true;
    notice.textContent = "";
  }

  const params = new URLSearchParams(queryFromFilters());
  const chips = [];
  for (const [key, value] of params.entries()) {
    if (key === "limit") continue;
    chips.push(`<span class="chip"><strong>${escapeHTML(labelForParam(key))}</strong>${escapeHTML(value)}</span>`);
  }
  $("#active-filters").innerHTML = chips.join("");
}

function renderHarnesses() {
  const harnesses = state.status?.harnesses || [];
  $("#harnesses").innerHTML = harnesses
    .map((harness) => `
      <div class="harness">
        <strong>${escapeHTML(harness.display_name || harness.name || "unknown")}</strong>
        <p class="muted">detected: ${Boolean(harness.detected)}</p>
        <p class="muted">telemetry: ${escapeHTML(harness.telemetry_status || "unknown")}</p>
        <p class="muted">${escapeHTML(harness.message || "")}</p>
      </div>
    `)
    .join("");
}

function renderEvents() {
  if (state.loading) {
    renderLoading();
    return;
  }
  if (state.error) {
    $("#events").innerHTML = `<tr><td colspan="8">Failed to load dashboard: ${escapeHTML(state.error.message)}</td></tr>`;
    return;
  }
  if (!state.events.length) {
    $("#events").innerHTML = `<tr><td colspan="8">No events match this search. Clear filters or broaden the query.</td></tr>`;
    return;
  }
  $("#events").innerHTML = state.events
    .map((record) => {
      const event = record.event || {};
      const info = event.event || {};
      const session = event.session || {};
      return `
        <tr data-id="${escapeHTML(record.id)}">
          <td class="nowrap">${escapeHTML(formatTime(event.timestamp))}</td>
          <td>${signalCell(record)}</td>
          <td>${escapeHTML(primaryArtifact(event))}</td>
          <td>${badge(event.severity || "unknown", `severity-${event.severity || "unknown"}`)}</td>
          <td>${reviewCell(record)}</td>
          <td class="mono">${escapeHTML(session.id || "")}</td>
          <td>${escapeHTML(repositoryLabel(event))}</td>
          <td>${escapeHTML(event.message || "")}</td>
        </tr>
      `;
    })
    .join("");
  document.querySelectorAll("#events tr").forEach((row) => {
    row.addEventListener("click", () => showEvent(row.dataset.id));
  });
}

async function showEvent(id) {
  const record = await getJSON(`/api/event?id=${encodeURIComponent(id)}`);
  $("#event-summary").innerHTML = detailSummary(record);
  $("#event-json").textContent = JSON.stringify(record.event, null, 2);
  $("#drawer").classList.add("open");
  $("#drawer").setAttribute("aria-hidden", "false");
}

function closeDrawer() {
  $("#drawer").classList.remove("open");
  $("#drawer").setAttribute("aria-hidden", "true");
}

function renderLoading() {
  $("#result-meta").textContent = "Loading...";
  $("#events").innerHTML = `<tr><td colspan="8">Loading events...</td></tr>`;
}

function signalCell(record) {
  const event = record.event || {};
  const info = event.event || {};
  const harness = event.harness || {};
  const parts = [
    `<strong>${escapeHTML(info.action || "unknown")}</strong>`,
    `<span class="muted">${escapeHTML(info.category || "uncategorized")} · ${escapeHTML(harness.name || "unknown")}</span>`,
    record.wazuh_level ? `<span class="muted">Wazuh level ${escapeHTML(record.wazuh_level)}</span>` : "",
  ].filter(Boolean);
  return parts.join("<br />");
}

function primaryArtifact(event) {
  if (event.command?.command) return event.command.command;
  if (event.file?.path) return `${event.file.operation || "file"} ${event.file.path}`;
  if (event.mcp?.server || event.mcp?.tool) return [event.mcp.server, event.mcp.tool].filter(Boolean).join(" / ");
  if (event.tool?.name || event.tool?.command) return [event.tool.name, event.tool.command].filter(Boolean).join(" ");
  if (event.approval?.decision || event.approval?.reason) return [event.approval.decision, event.approval.reason].filter(Boolean).join(": ");
  if (event.policy?.decision || event.policy?.name) return [event.policy.decision, event.policy.name].filter(Boolean).join(": ");
  return event.model || event.branch || "";
}

function reviewCell(record) {
  const event = record.event || {};
  const labels = [];
  if (event.severity === "critical" || event.severity === "high") labels.push(badge(event.severity, "badge-danger"));
  if (record.wazuh_level >= 9) labels.push(badge(`L${record.wazuh_level}`, "badge-danger"));
  if (event.event?.action === "tool.failed") labels.push(badge("failed", "badge-warn"));
  if (event.event?.action === "policy.blocked") labels.push(badge("blocked", "badge-danger"));
  if (event.event?.action === "approval.denied" || event.approval?.decision === "denied") labels.push(badge("denied", "badge-danger"));
  if (event.content?.truncated || event.field_truncated) labels.push(badge("truncated", "badge-warn"));
  if (event.content?.retention) labels.push(badge(event.content.retention, "badge-muted"));
  return labels.join(" ") || badge("normal", "badge-muted");
}

function detailSummary(record) {
  const event = record.event || {};
  const info = event.event || {};
  const rows = [
    ["Action", info.action],
    ["Category", info.category],
    ["Severity", event.severity],
    ["Wazuh level", record.wazuh_level || ""],
    ["Session", event.session?.id],
    ["Repository", repositoryLabel(event)],
    ["Artifact", primaryArtifact(event)],
    ["Approval", event.approval ? [event.approval.decision, event.approval.reason].filter(Boolean).join(": ") : ""],
    ["Policy", event.policy ? [event.policy.decision, event.policy.name, event.policy.reason].filter(Boolean).join(": ") : ""],
    ["Content", event.content ? `${event.content.retention}${event.content.redacted ? ", redacted" : ""}${event.content.truncated ? ", truncated" : ""}` : ""],
  ].filter(([, value]) => value);
  return rows
    .map(([label, value]) => `<div><span class="muted">${escapeHTML(label)}</span><strong>${escapeHTML(value)}</strong></div>`)
    .join("");
}

function repositoryLabel(event) {
  return [event.repository, event.branch].filter(Boolean).join(" @ ");
}

function badge(value, className) {
  return `<span class="badge ${escapeHTML(className)}">${escapeHTML(value)}</span>`;
}

function formatTime(timestamp) {
  if (!timestamp) return "";
  const parsed = new Date(timestamp);
  if (Number.isNaN(parsed.getTime())) return timestamp;
  return parsed.toLocaleString();
}

function labelForParam(key) {
  return key.replaceAll("_", " ");
}

function escapeHTML(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function applyPreset(name) {
  for (const field of formFields) {
    const input = $(`[name="${field}"]`);
    if (input) input.value = field === "limit" ? "500" : "";
  }
  const preset = presets[name] || {};
  for (const [key, value] of Object.entries(preset)) {
    const input = $(`[name="${key}"]`);
    if (input) input.value = value;
  }
  load({ updateLocation: true }).catch(console.error);
}

function applyInsight(kind, value) {
  for (const field of formFields) {
    const input = $(`[name="${field}"]`);
    if (input) input.value = field === "limit" ? "500" : "";
  }
  const key = kind === "Top Actions" ? "action" : kind === "Top Repositories" ? "repository" : "mcp";
  const input = $(`[name="${key}"]`);
  if (input) input.value = value;
  load({ updateLocation: true }).catch(console.error);
}

function clearSearch() {
  for (const field of formFields) {
    const input = $(`[name="${field}"]`);
    if (input) input.value = field === "limit" ? "500" : "";
  }
  load({ updateLocation: true }).catch(console.error);
}

$("#refresh").addEventListener("click", () => load().catch(console.error));
$("#filters").addEventListener("submit", (event) => {
  event.preventDefault();
  load({ updateLocation: true }).catch(console.error);
});
$("#clear-search").addEventListener("click", clearSearch);
$("#close-drawer").addEventListener("click", closeDrawer);
$$("[data-preset]").forEach((button) => {
  button.addEventListener("click", () => applyPreset(button.dataset.preset));
});

hydrateFiltersFromURL();
load().catch(console.error);
setInterval(() => load().catch(console.error), 10000);
