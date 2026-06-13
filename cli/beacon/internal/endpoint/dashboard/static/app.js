const state = {
  status: null,
  summary: null,
  events: [],
  eventResult: null,
  loading: false,
  error: null,
  currentQuery: "",
  newEventCount: 0,
};

const $ = (selector) => document.querySelector(selector);
const $$ = (selector) => Array.from(document.querySelectorAll(selector));
const isOverviewPage = document.body.dataset.page === "overview";
const isTokensPage = document.body.dataset.page === "tokens";
const isDetectionsPage = document.body.dataset.page === "detections";
const isFindingsPage = document.body.dataset.page === "findings";
const formFields = [
  "q",
  "harness",
  "model",
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
  const form = $("#filters");
  if (!form) return "";
  const data = new FormData(form);
  const params = new URLSearchParams();
  for (const [key, value] of data.entries()) {
    const trimmed = String(value).trim();
    if (trimmed) params.set(key, trimmed);
  }
  return params.toString();
}

function hydrateFiltersFromURL() {
  if (!$("#filters")) return;
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

async function load({ updateLocation = false, mode = "replace" } = {}) {
  const query = queryFromFilters();
  if (updateLocation) updateURL(query);
  const suffix = query ? `?${query}` : "";
  const quiet = mode === "poll";
  state.loading = !quiet;
  state.error = null;
  if (!quiet) renderLoading();
  let rendered = false;
  try {
    const [status, summary, events] = await Promise.all([
      getJSON("/api/status"),
      getJSON(`/api/summary${suffix}`),
      getJSON(`/api/events${suffix}`),
    ]);
    const previousEvents = state.events;
    const previousQuery = state.currentQuery;
    state.status = status;
    state.summary = summary;
    state.eventResult = events;
    state.currentQuery = query;
    state.events = events.events || [];
    if (quiet && previousQuery === query && canPatchEvents(previousEvents, state.events)) {
      patchEvents(previousEvents, state.events);
      renderSummaryOnly();
      rendered = true;
    }
  } catch (err) {
    state.error = err;
  } finally {
    state.loading = false;
    if (!rendered) render();
  }
}

function render() {
  setText("#log-path", state.status?.log_path || "Runtime log unavailable");
  setText("#last-updated", state.summary?.last_event_time ? `Last event ${formatTime(state.summary.last_event_time)}` : "");
  renderNewEventsIndicator();
  renderFilterOptions();
  renderCards();
  renderInsights();
  renderSearchState();
  renderHarnesses();
  renderEvents();
}

function renderCards() {
  if (!$("#cards")) return;
  const topHarness = firstCount(state.summary?.top_harnesses);
  const topModel = firstCount(state.summary?.top_models);
  const cards = [
    { label: "Events", value: state.summary?.total_events || 0, hint: "matching current view" },
    { label: "Needs Review", value: state.summary?.needs_review_events || 0, tone: "danger", filters: { review: "true" } },
    { label: "High/Critical", value: (state.summary?.high_severity_events || 0) + (state.summary?.critical_severity_events || 0), tone: "danger", filters: { review: "true" } },
    { label: "Denied/Blocked", value: (state.summary?.denied_approval_events || 0) + (state.summary?.policy_blocked_events || 0), tone: "danger", filters: { category: "approval", review: "true" } },
    { label: "Failed Tools", value: state.summary?.failed_tool_events || 0, tone: "warn", filters: { action: "tool.failed" } },
    { label: "Sessions", value: state.summary?.active_sessions || 0, hint: "active sessions" },
    { label: "Top Harness", value: topHarness?.name || "None", hint: topHarness ? `${topHarness.count} events` : "no harness events", filters: topHarness ? { harness: topHarness.name } : null },
    { label: "Top Model", value: topModel?.name || "None", hint: topModel ? `${topModel.count} events` : "no model events", filters: topModel ? { model: topModel.name } : null },
  ];
  $("#cards").innerHTML = cards
    .map((card, index) => `
      <button type="button" class="card ${card.tone ? `card-${card.tone}` : ""}" data-card="${index}">
        <span class="muted">${escapeHTML(card.label)}</span>
        <span class="value">${escapeHTML(card.value)}</span>
        ${card.hint ? `<span class="muted">${escapeHTML(card.hint)}</span>` : ""}
      </button>
    `)
    .join("");
  $$("#cards [data-card]").forEach((button) => {
    button.addEventListener("click", () => {
      const card = cards[Number(button.dataset.card)];
      if (card?.filters) applyFilters(card.filters, { reset: true });
    });
  });
}

function renderInsights() {
  if (!$("#insights")) return;
  const attention = attentionItems();
  const sections = [
    ["What Needs Attention", attention, "attention"],
    ["Agent Harnesses", state.summary?.top_harnesses || []],
    ["Models", state.summary?.top_models || []],
    ["Top Actions", state.summary?.top_actions || []],
    ["Top Repositories", state.summary?.top_repositories || []],
    ["MCP Servers", state.summary?.top_mcp_servers || []],
  ];
  $("#insights").innerHTML = sections
    .map(([title, values, tone]) => `
      <div class="panel insight">
        <h2>${escapeHTML(title)}</h2>
        <div class="stack">
          ${values.length ? values.map((item, index) => `<button type="button" class="${tone === "attention" ? "attention-item" : ""}" data-insight="${escapeHTML(title)}" data-index="${index}" data-value="${escapeHTML(item.name)}"><span>${escapeHTML(item.name)}</span><strong>${escapeHTML(item.count)}</strong></button>`).join("") : `<span class="muted">No data yet</span>`}
        </div>
      </div>
    `)
    .join("");
  $$("[data-insight]").forEach((button) => {
    button.addEventListener("click", () => {
      if (button.dataset.insight === "What Needs Attention") {
        const item = attention[Number(button.dataset.index)];
        if (item?.filters) applyFilters(item.filters, { reset: true });
        return;
      }
      applyInsight(button.dataset.insight, button.dataset.value);
    });
  });
  renderFacets();
}

function renderSearchState() {
  if (!$("#result-meta")) return;
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
    chips.push(`<button type="button" class="chip" data-clear-filter="${escapeHTML(key)}"><strong>${escapeHTML(labelForParam(key))}</strong>${escapeHTML(value)}<span aria-hidden="true">x</span></button>`);
  }
  $("#active-filters").innerHTML = chips.join("");
  $$("[data-clear-filter]").forEach((button) => {
    button.addEventListener("click", () => clearFilter(button.dataset.clearFilter));
  });
}

function renderSummaryOnly() {
  setText("#log-path", state.status?.log_path || "Runtime log unavailable");
  setText("#last-updated", state.summary?.last_event_time ? `Last event ${formatTime(state.summary.last_event_time)}` : "");
  renderNewEventsIndicator();
  renderFilterOptions();
  renderCards();
  renderInsights();
  renderSearchState();
  renderHarnesses();
}

function renderHarnesses() {
  if (!$("#harnesses")) return;
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
  if (!$("#events")) return;
  if (state.loading) {
    renderLoading();
    return;
  }
  if (state.error) {
    $("#events").innerHTML = `<tr><td colspan="10">Failed to load dashboard: ${escapeHTML(state.error.message)}</td></tr>`;
    return;
  }
  if (!state.events.length) {
    $("#events").innerHTML = `<tr><td colspan="10">No events match this search. Clear filters or broaden the query.</td></tr>`;
    return;
  }
  $("#events").innerHTML = state.events
    .map((record) => eventRowHTML(record))
    .join("");
  bindEventRows();
}

function eventRowHTML(record) {
  const event = record.event || {};
  const session = event.session || {};
  return `
    <tr data-id="${escapeHTML(record.id)}">
      <td class="nowrap col-timestamp">${escapeHTML(formatTime(event.timestamp))}</td>
      <td class="mono">${escapeHTML(session.id || "")}</td>
      <td>${escapeHTML(repositoryShortLabel(event))}</td>
      <td>${tagCell(record)}</td>
      <td>${badge(event.severity || "unknown", `severity-${event.severity || "unknown"}`)}</td>
      <td>${retentionCell(record)}</td>
      <td>${signalCell(record)}</td>
      <td>${harnessCell(event)}</td>
      <td class="col-artifact">${artifactCell(event)}</td>
      <td class="col-message">${escapeHTML(event.message || "")}</td>
    </tr>
  `;
}

function bindEventRows(scope = document) {
  const rows = scope.matches?.("tr[data-id]") ? [scope] : Array.from(scope.querySelectorAll("#events tr, tr[data-id]"));
  rows.forEach((row) => {
    row.addEventListener("click", (event) => {
      if (event.target.closest("button")) return;
      showEvent(row.dataset.id);
    });
  });
  Array.from(scope.querySelectorAll("[data-apply-filter]")).forEach((button) => {
    button.addEventListener("click", () => applyFilters({ [button.dataset.applyFilter]: button.dataset.value }));
  });
}

function canPatchEvents(previousEvents, nextEvents) {
  return $("#events") && previousEvents.length > 0 && nextEvents.length > 0;
}

function patchEvents(previousEvents, nextEvents) {
  const tbody = $("#events");
  if (!tbody) return;
  const existing = new Set(previousEvents.map((record) => record.id));
  const newest = [];
  for (const record of nextEvents) {
    if (existing.has(record.id)) break;
    newest.push(record);
  }
  if (!newest.length) return;
  prependEventRows(newest);
}

function prependEventRows(records) {
  const tbody = $("#events");
  if (!tbody) return;
  const nearTop = window.scrollY < 160;
  const beforeTop = tbody.getBoundingClientRect().top;
  const template = document.createElement("template");
  template.innerHTML = records.map((record) => eventRowHTML(record)).join("");
  const rows = Array.from(template.content.children);
  const fragment = document.createDocumentFragment();
  rows.forEach((row) => fragment.appendChild(row));
  tbody.prepend(fragment);
  rows.forEach((row) => bindEventRows(row));
  if (!nearTop) {
    const afterTop = tbody.getBoundingClientRect().top;
    window.scrollBy(0, afterTop - beforeTop);
    state.newEventCount += records.length;
  } else {
    state.newEventCount = 0;
  }
  renderNewEventsIndicator();
}

function renderNewEventsIndicator() {
  const button = $("#new-events");
  if (!button) return;
  if (state.newEventCount > 0) {
    button.hidden = false;
    button.textContent = `${state.newEventCount} new event${state.newEventCount === 1 ? "" : "s"} available`;
  } else {
    button.hidden = true;
    button.textContent = "";
  }
}

function showNewEvents() {
  state.newEventCount = 0;
  renderNewEventsIndicator();
  window.scrollTo({ top: 0, behavior: "smooth" });
}

async function showEvent(id) {
  if (!$("#drawer")) return;
  const record = await getJSON(`/api/event?id=${encodeURIComponent(id)}`);
  $("#event-summary").innerHTML = detailSummary(record);
  $("#event-json").textContent = JSON.stringify(record.event, null, 2);
  $$("#event-summary [data-apply-filter]").forEach((button) => {
    button.addEventListener("click", () => applyFilters({ [button.dataset.applyFilter]: button.dataset.value }));
  });
  $("#drawer").classList.add("open");
  $("#drawer").setAttribute("aria-hidden", "false");
}

function closeDrawer() {
  if (!$("#drawer")) return;
  $("#drawer").classList.remove("open");
  $("#drawer").setAttribute("aria-hidden", "true");
}

function renderLoading() {
  setText("#result-meta", "Loading...");
  if ($("#events")) $("#events").innerHTML = `<tr><td colspan="10">Loading events...</td></tr>`;
}

function signalCell(record) {
  const event = record.event || {};
  const info = event.event || {};
  const parts = [
    `<strong>${escapeHTML(signalAction(record))}</strong>`,
    `<span class="muted">${escapeHTML(info.category || "uncategorized")}${event.model ? ` · ${escapeHTML(event.model)}` : ""}</span>`,
  ].filter(Boolean);
  return parts.join("<br />");
}

function tagCell(record) {
  const event = record.event || {};
  return filterButtons([
    ["action", signalAction(record)],
    ["model", event.model],
  ]);
}

function harnessCell(event) {
  const harness = event.harness || {};
  if (!harness.name) return "";
  return escapeHTML(harnessLabel(harness.name));
}

function signalAction(record) {
  const event = record.event || {};
  const info = event.event || {};
  if (info.category === "metric") return metricName(event) || info.action || "metric.observed";
  return info.action || "unknown";
}

function metricName(event) {
  return event.raw?.metric_name || event.message || "";
}

function primaryArtifact(event) {
  if (event.prompt?.text) return event.prompt.text;
  if (event.command?.command) return event.command.command;
  if (event.file?.path) return `${event.file.operation || "file"} ${event.file.path}`;
  if (event.mcp?.server || event.mcp?.tool) return [event.mcp.server, event.mcp.tool].filter(Boolean).join(" / ");
  if (event.tool?.name || event.tool?.command) return [event.tool.name, event.tool.command].filter(Boolean).join(" ");
  if (event.approval?.decision || event.approval?.reason) return [event.approval.decision, event.approval.reason].filter(Boolean).join(": ");
  if (event.policy?.decision || event.policy?.name) return [event.policy.decision, event.policy.name].filter(Boolean).join(": ");
  if (event.event?.category === "metric") return metricName(event);
  return event.model || event.branch || "";
}

function artifactCell(event) {
  const artifact = primaryArtifact(event);
  const meta = [
    event.command?.exit_code !== undefined ? `exit ${event.command.exit_code}` : "",
    event.command?.duration_ms ? `${event.command.duration_ms}ms` : "",
    event.file?.language,
    event.mcp?.server,
  ].filter(Boolean);
  return `
    <div class="artifact">${escapeHTML(truncateMiddle(artifact || event.message || "", 140))}</div>
    ${meta.length ? `<div class="muted">${escapeHTML(meta.join(" · "))}</div>` : ""}
  `;
}

function retentionCell(record) {
  const event = record.event || {};
  const labels = [];
  if (event.content?.retention) labels.push(badge(event.content.retention, "badge-muted"));
  if (event.field_truncated || event.content?.truncated) labels.push(badge("truncated", "badge-warn"));
  if (event.content?.redacted) labels.push(badge("redacted", "badge-warn"));
  return labels.join(" ") || badge("default", "badge-muted");
}

function detailSummary(record) {
  const event = record.event || {};
  const info = event.event || {};
  const rows = [
    ["Action", signalAction(record)],
    ["Category", info.category],
    ["Severity", event.severity],
    ["Wazuh level", record.wazuh_level || ""],
    ["Harness", event.harness?.name],
    ["Model", event.model],
    ["Session", event.session?.id],
    ["Repository", repositoryLabel(event)],
    ["Prompt", event.prompt?.text],
    ["Artifact", event.prompt?.text ? "" : primaryArtifact(event)],
    ["Approval", event.approval ? [event.approval.decision, event.approval.reason].filter(Boolean).join(": ") : ""],
    ["Policy", event.policy ? [event.policy.decision, event.policy.name, event.policy.reason].filter(Boolean).join(": ") : ""],
    ["Content", event.content ? `${event.content.retention}${event.content.redacted ? ", redacted" : ""}${event.content.truncated ? ", truncated" : ""}` : ""],
  ].filter(([, value]) => value);
  return rows
    .map(([label, value]) => {
      const key = detailFilterKey(label);
      return `
        <div>
          <span class="muted">${escapeHTML(label)}</span>
          <strong>${escapeHTML(value)}</strong>
          ${key ? `<button type="button" class="text-button" data-apply-filter="${escapeHTML(key)}" data-value="${escapeHTML(value)}">Filter by this</button>` : ""}
        </div>
      `;
    })
    .join("");
}

function repositoryLabel(event) {
  return [event.repository, event.branch].filter(Boolean).join(" @ ");
}

function repositoryShortLabel(event) {
  if (!event.repository) return event.branch || "";
  const parts = event.repository.split("/").filter(Boolean);
  const repo = parts[parts.length - 1] || event.repository;
  const label = parts.length > 1 ? `../${repo}` : repo;
  return [label, event.branch].filter(Boolean).join(" @ ");
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
  const labels = {
    q: "search",
    mcp: "MCP",
    wazuh_level: "Wazuh",
  };
  return labels[key] || key.replaceAll("_", " ");
}

function renderFilterOptions() {
  if (!$("#filters")) return;
  renderHarnessSelect(state.summary?.top_harnesses || []);
  renderModelSelect(state.summary?.top_models || []);
  renderDatalist("action-options", state.summary?.top_actions || []);
}

function renderHarnessSelect(values) {
  const select = $("#harness-filter");
  if (!select) return;
  const current = select.value;
  const options = [
    `<option value="">All agent harnesses</option>`,
    ...values.map((item) => `<option value="${escapeHTML(item.name)}">${escapeHTML(harnessLabel(item.name))} (${escapeHTML(item.count)})</option>`),
  ];
  select.innerHTML = options.join("");
  select.value = values.some((item) => item.name === current) ? current : "";
}

function renderModelSelect(values) {
  const select = $("#model-filter");
  if (!select) return;
  const current = select.value;
  const options = [
    `<option value="">All models</option>`,
    ...values.map((item) => `<option value="${escapeHTML(item.name)}">${escapeHTML(item.name)} (${escapeHTML(item.count)})</option>`),
  ];
  select.innerHTML = options.join("");
  select.value = values.some((item) => item.name === current) ? current : "";
}

function renderDatalist(id, values) {
  const list = $(`#${id}`);
  if (!list) return;
  list.innerHTML = values.map((item) => `<option value="${escapeHTML(item.name)}">${escapeHTML(item.count)} events</option>`).join("");
}

function harnessLabel(value) {
  const labels = {
    cursor: "Cursor",
    claude_code: "Claude Code",
    codex_cli: "Codex CLI",
    copilot_cli: "GitHub Copilot CLI",
    grok: "Grok Build",
    cli: "Beacon CLI",
  };
  return labels[value] || value;
}

function renderFacets() {
  if (!$("#facets")) return;
  const groups = [
    ["Harness", "harness", state.summary?.top_harnesses || []],
    ["Model", "model", state.summary?.top_models || []],
  ];
  $("#facets").innerHTML = groups
    .filter(([, , values]) => values.length)
    .map(([label, key, values]) => `
      <div class="facet-group">
        <span class="muted">${escapeHTML(label)}</span>
        ${values.slice(0, 5).map((item) => `<button type="button" data-apply-filter="${escapeHTML(key)}" data-value="${escapeHTML(item.name)}">${escapeHTML(item.name)} <strong>${escapeHTML(item.count)}</strong></button>`).join("")}
      </div>
    `)
    .join("");
  $$("#facets [data-apply-filter]").forEach((button) => {
    button.addEventListener("click", () => applyFilters({ [button.dataset.applyFilter]: button.dataset.value }));
  });
}

function attentionItems() {
  const items = [
    { name: "Needs review", count: state.summary?.needs_review_events || 0, filters: { review: "true" } },
    { name: "Denied approvals", count: state.summary?.denied_approval_events || 0, filters: { action: "approval.denied" } },
    { name: "Blocked policies", count: state.summary?.policy_blocked_events || 0, filters: { action: "policy.blocked" } },
    { name: "Failed tools", count: state.summary?.failed_tool_events || 0, filters: { action: "tool.failed" } },
    { name: "High or critical severity", count: (state.summary?.high_severity_events || 0) + (state.summary?.critical_severity_events || 0), filters: { review: "true" } },
  ];
  return items.filter((item) => item.count > 0);
}

function firstCount(values) {
  return values && values.length ? values[0] : null;
}

function filterButtons(values) {
  return values
    .filter(([, value]) => value)
    .map(([key, value]) => `<button type="button" class="mini-filter" data-apply-filter="${escapeHTML(key)}" data-value="${escapeHTML(value)}">${escapeHTML(labelForParam(key))}</button>`)
    .join(" ");
}

function detailFilterKey(label) {
  switch (label) {
    case "Action":
      return "action";
    case "Category":
      return "category";
    case "Severity":
      return "severity";
    case "Harness":
      return "harness";
    case "Model":
      return "model";
    case "Session":
      return "session";
    case "Repository":
      return "repository";
    default:
      return "";
  }
}

function setFilters(filters, { reset = false } = {}) {
  if (reset) clearFields(false);
  state.newEventCount = 0;
  for (const [key, value] of Object.entries(filters)) {
    const input = $(`[name="${key}"]`);
    if (input) input.value = value;
  }
  load({ updateLocation: true }).catch(console.error);
}

function applyFilters(filters, options = {}) {
  if (isOverviewPage) {
    const query = queryStringFromObject(filters);
    window.location.href = query ? `/?${query}` : "/";
    return;
  }
  setFilters(filters, options);
}

function queryStringFromObject(values) {
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(values)) {
    if (value) params.set(key, value);
  }
  return params.toString();
}

function clearFilter(key) {
  if (!$("#filters")) return;
  state.newEventCount = 0;
  const input = $(`[name="${key}"]`);
  if (input) input.value = "";
  load({ updateLocation: true }).catch(console.error);
}

function clearFields(loadAfter = true) {
  if (!$("#filters")) return;
  state.newEventCount = 0;
  for (const field of formFields) {
    const input = $(`[name="${field}"]`);
    if (input) input.value = field === "limit" ? "500" : "";
  }
  if (loadAfter) load({ updateLocation: true }).catch(console.error);
}

function truncateMiddle(value, maxLength) {
  value = String(value || "");
  if (value.length <= maxLength) return value;
  const keep = Math.floor((maxLength - 3) / 2);
  return `${value.slice(0, keep)}...${value.slice(value.length - keep)}`;
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
  applyFilters(presets[name] || {}, { reset: true });
}

function applyInsight(kind, value) {
  const key =
    kind === "Top Actions" ? "action" :
    kind === "Top Repositories" ? "repository" :
    kind === "Agent Harnesses" ? "harness" :
    kind === "Models" ? "model" :
    "mcp";
  applyFilters({ [key]: value });
}

function clearSearch() {
  clearFields();
}

async function loadTokens() {
  const params = new URLSearchParams(window.location.search);
  const tokenParams = new URLSearchParams({ bucket: "1h" });
  const session = (params.get("session") || "").trim();
  if (session) tokenParams.set("session", session);
  try {
    const [status, report] = await Promise.all([
      getJSON("/api/status"),
      getJSON(`/api/tokens?${tokenParams.toString()}`),
    ]);
    state.status = status;
    state.error = null;
    renderTokensPage(report, session);
  } catch (err) {
    state.error = err;
    setText("#token-meta", `Failed to load token usage: ${err.message}`);
  }
}

function renderTokensPage(report, session) {
  setText("#log-path", state.status?.log_path || "Runtime log unavailable");
  setText("#token-meta", `${report.events_with_usage} of ${report.total_events} events carry usage`);
  renderTokenCards(report.totals || {});
  renderUtilizationRows(report.utilization || []);
  renderUsageGroupRows("#token-models", report.by_model || [], "model");
  renderUsageGroupRows("#token-sessions", report.by_session || [], "session");
  renderUsageGroupRows("#token-runs", report.by_run || []);
  renderUsageGroupRows("#token-harnesses", report.by_harness || []);
  renderSessionDetail(report.session_detail, session);
}

function renderTokenCards(totals) {
  if (!$("#token-cards")) return;
  const cards = [
    { label: "Input Tokens", value: formatTokens(totals.input_tokens) },
    { label: "Output Tokens", value: formatTokens(totals.output_tokens) },
    { label: "Cache Read", value: formatTokens(totals.cache_read_input_tokens) },
    { label: "Cache Creation", value: formatTokens(totals.cache_creation_input_tokens) },
    { label: "Reasoning", value: formatTokens(totals.reasoning_output_tokens) },
    { label: "Cost (USD)", value: totals.cost_usd ? totals.cost_usd.toFixed(4) : "-", hint: "runtime-reported only" },
  ];
  $("#token-cards").innerHTML = cards
    .map((card) => `
      <div class="card">
        <span class="muted">${escapeHTML(card.label)}</span>
        <span class="value">${escapeHTML(card.value)}</span>
        ${card.hint ? `<span class="muted">${escapeHTML(card.hint)}</span>` : ""}
      </div>
    `)
    .join("");
}

function renderUtilizationRows(utilization) {
  const tbody = $("#token-utilization");
  if (!tbody) return;
  if (!utilization.length) {
    tbody.innerHTML = `<tr><td colspan="7" class="muted">No usage with input tokens captured yet.</td></tr>`;
    return;
  }
  tbody.innerHTML = utilization
    .map((item) => `
      <tr>
        <td class="mono">${escapeHTML(item.model)}</td>
        <td>${item.context_window ? formatTokens(item.context_window) : `<span class="muted">unknown</span>`}</td>
        <td>${escapeHTML(item.calls)}</td>
        <td>${formatTokens(item.max_input_tokens)}</td>
        <td>${formatRatio(item.max_ratio, item.context_window)}</td>
        <td>${formatRatio(item.p95_ratio, item.context_window)}</td>
        <td>${item.near_limit_calls ? badge(`${item.near_limit_calls} near limit`, "badge-warn") : ""}</td>
      </tr>
    `)
    .join("");
}

function renderUsageGroupRows(selector, groups, filterKey) {
  const tbody = $(selector);
  if (!tbody) return;
  if (!groups.length) {
    tbody.innerHTML = `<tr><td colspan="8" class="muted">No usage captured yet.</td></tr>`;
    return;
  }
  tbody.innerHTML = groups
    .map((group) => `
      <tr ${filterKey === "session" ? `class="row-link" data-token-session="${escapeHTML(group.key)}"` : ""}>
        <td class="mono">${escapeHTML(group.key)}</td>
        ${usageCells(group.usage)}
      </tr>
    `)
    .join("");
  $$(`${selector} [data-token-session]`).forEach((row) => {
    row.addEventListener("click", () => {
      const params = new URLSearchParams(window.location.search);
      params.set("session", row.dataset.tokenSession);
      window.location.search = params.toString();
    });
  });
}

function renderSessionDetail(detail, session) {
  const panel = $("#token-session-panel");
  if (!panel) return;
  if (!detail || !session) {
    panel.hidden = true;
    return;
  }
  panel.hidden = false;
  setText("#token-session-title", `Session ${detail.session_id}`);
  const rows = [];
  const walk = (steps, depth) => {
    for (const step of steps || []) {
      const label = step.name || step.action || step.span_id || "step";
      rows.push(`
        <tr>
          <td><span class="mono">${"&nbsp;".repeat(depth * 4)}${escapeHTML(label)}</span></td>
          <td class="mono">${escapeHTML(step.model || "")}</td>
          ${usageCells(step.usage, { events: false })}
        </tr>
      `);
      walk(step.children, depth + 1);
    }
  };
  walk(detail.steps, 0);
  $("#token-session-detail").innerHTML = rows.length
    ? rows.join("")
    : `<tr><td colspan="8" class="muted">No usage-bearing steps in this session.</td></tr>`;
}

function usageCells(usage = {}, { events = true } = {}) {
  const cells = [
    formatTokens(usage.input_tokens),
    formatTokens(usage.output_tokens),
    formatTokens(usage.cache_read_input_tokens),
    formatTokens(usage.cache_creation_input_tokens),
    formatTokens(usage.reasoning_output_tokens),
    usage.cost_usd ? usage.cost_usd.toFixed(4) : "-",
  ];
  if (events) cells.push(String(usage.events ?? 0));
  return cells.map((value) => `<td>${escapeHTML(value)}</td>`).join("");
}

function formatTokens(value) {
  if (!value) return "0";
  return Number(value).toLocaleString();
}

function formatRatio(ratio, contextWindow) {
  if (!contextWindow) return "-";
  return `${(ratio * 100).toFixed(1)}%`;
}

async function loadDetections() {
  try {
    const resp = await getJSON("/api/detections");
    state.error = null;
    renderDetections(resp);
  } catch (err) {
    state.error = err;
    setText("#detections-meta", `Failed to load detections: ${err.message}`);
  }
}

function renderDetections(resp) {
  const rules = resp?.rules || [];
  setText("#detections-meta", `${rules.length} active rule${rules.length === 1 ? "" : "s"}`);
  const tbody = $("#detections-rows");
  if (!tbody) return;
  if (!rules.length) {
    tbody.innerHTML = `<tr><td colspan="7" class="muted">No active rules.</td></tr>`;
    return;
  }
  tbody.innerHTML = rules
    .map((rule) => `
      <tr id="rule-${escapeHTML(rule.id)}">
        <td>${badge(rule.severity || "unknown", `severity-${rule.severity || "unknown"}`)}</td>
        <td><span class="mono">${escapeHTML(rule.id)}</span><br /><span class="muted">${escapeHTML(rule.title || "")}</span></td>
        <td>${escapeHTML(rule.status || "")}</td>
        <td>${escapeHTML(rule.posture || "")}</td>
        <td>${escapeHTML(rule.kind || "")}</td>
        <td>${badge(rule.source || "unknown", "badge-muted")}</td>
        <td>${escapeHTML(rule.reason || "")}</td>
      </tr>
    `)
    .join("");
}

async function loadFindings() {
  const params = new URLSearchParams(window.location.search);
  const minSeverity = (params.get("min_severity") || "").trim();
  const select = $("#min-severity");
  if (select) select.value = minSeverity;
  const query = new URLSearchParams();
  if (minSeverity) query.set("min_severity", minSeverity);
  const session = (params.get("session") || "").trim();
  if (session) query.set("session", session);
  const suffix = query.toString() ? `?${query.toString()}` : "";
  try {
    const resp = await getJSON(`/api/findings${suffix}`);
    state.error = null;
    renderFindings(resp);
  } catch (err) {
    state.error = err;
    setText("#findings-meta", `Failed to load findings: ${err.message}`);
  }
}

function renderFindings(resp) {
  const findings = resp?.findings || [];
  setText("#findings-meta", `${findings.length} finding${findings.length === 1 ? "" : "s"} across ${resp?.scanned || 0} events`);
  const tbody = $("#findings-rows");
  if (!tbody) return;
  if (!findings.length) {
    tbody.innerHTML = `<tr><td colspan="5" class="muted">No findings.</td></tr>`;
    return;
  }
  tbody.innerHTML = findings
    .map((f) => `
      <tr>
        <td>${badge(f.severity || "unknown", `severity-${f.severity || "unknown"}`)}</td>
        <td><a class="mono" href="/detections.html#rule-${escapeHTML(f.rule_id)}">${escapeHTML(f.rule_id)}</a><br /><span class="muted">${escapeHTML(f.title || "")}</span></td>
        <td class="mono">${escapeHTML(f.session_id || "-")}</td>
        <td>${escapeHTML(f.reason || "")}</td>
        <td>${summarizeFindingEvents(f.events)}</td>
      </tr>
    `)
    .join("");
}

function summarizeFindingEvents(events) {
  if (!events || !events.length) return `<span class="muted">-</span>`;
  return events
    .map((e) => {
      const action = e.event?.action || "event";
      let detail = "";
      if (e.command?.command) detail = e.command.command;
      else if (e.file?.path) detail = e.file.path;
      else if (e.prompt?.text) detail = e.prompt.text;
      const when = e.timestamp ? `${escapeHTML(formatTime(e.timestamp))} ` : "";
      const text = detail ? `${action}: ${detail}` : action;
      return `<div>${when}<span class="mono">${escapeHTML(text)}</span></div>`;
    })
    .join("");
}

$("#min-severity")?.addEventListener("change", (event) => {
  const params = new URLSearchParams(window.location.search);
  if (event.target.value) params.set("min_severity", event.target.value);
  else params.delete("min_severity");
  window.location.search = params.toString();
});

$("#refresh")?.addEventListener("click", () => {
  const loader = isFindingsPage ? loadFindings : isDetectionsPage ? loadDetections : isTokensPage ? loadTokens : load;
  loader().catch(console.error);
});
$("#filters")?.addEventListener("submit", (event) => {
  event.preventDefault();
  load({ updateLocation: true }).catch(console.error);
});
$("#clear-search")?.addEventListener("click", clearSearch);
$("#close-drawer")?.addEventListener("click", closeDrawer);
$("#new-events")?.addEventListener("click", showNewEvents);
$$("[data-preset]").forEach((button) => {
  button.addEventListener("click", () => applyPreset(button.dataset.preset));
});

if (isDetectionsPage) {
  loadDetections().catch(console.error);
  setInterval(() => loadDetections().catch(console.error), 15000);
} else if (isFindingsPage) {
  loadFindings().catch(console.error);
  setInterval(() => loadFindings().catch(console.error), 15000);
} else if (isTokensPage) {
  loadTokens().catch(console.error);
  setInterval(() => loadTokens().catch(console.error), 15000);
} else {
  hydrateFiltersFromURL();
  load().catch(console.error);
  setInterval(() => load({ mode: "poll" }).catch(console.error), 10000);
}

function setText(selector, value) {
  const element = $(selector);
  if (element) element.textContent = value;
}
