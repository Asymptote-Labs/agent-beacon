const state = {
  status: null,
  summary: null,
  events: [],
};

const $ = (selector) => document.querySelector(selector);

async function getJSON(path) {
  const response = await fetch(path);
  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText}`);
  }
  return response.json();
}

function queryFromFilters() {
  const data = new FormData($("#filters"));
  const params = new URLSearchParams();
  for (const [key, value] of data.entries()) {
    if (value) params.set(key, value);
  }
  return params.toString();
}

async function load() {
  const query = queryFromFilters();
  const suffix = query ? `?${query}` : "";
  const [status, summary, events] = await Promise.all([
    getJSON("/api/status"),
    getJSON(`/api/summary${suffix}`),
    getJSON(`/api/events${suffix}`),
  ]);
  state.status = status;
  state.summary = summary;
  state.events = events.events || [];
  render();
}

function render() {
  $("#log-path").textContent = state.status?.log_path || "Runtime log unavailable";
  $("#retention").textContent = `retention: ${state.status?.content_retention || "metadata"}`;
  renderCards();
  renderHarnesses();
  renderEvents();
}

function renderCards() {
  const cards = [
    ["Events", state.summary?.total_events || 0],
    ["Sessions", state.summary?.active_sessions || 0],
    ["Commands", state.summary?.command_events || 0],
    ["Files", state.summary?.file_events || 0],
    ["MCP", state.summary?.mcp_events || 0],
    ["Approvals", state.summary?.approval_events || 0],
    ["Malformed", state.summary?.malformed_lines || 0],
  ];
  $("#cards").innerHTML = cards
    .map(([label, value]) => `<div class="card"><span class="muted">${escapeHTML(label)}</span><span class="value">${value}</span></div>`)
    .join("");
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
  $("#events").innerHTML = state.events
    .map((record) => {
      const event = record.event || {};
      const info = event.event || {};
      const session = event.session || {};
      const harness = event.harness || {};
      return `
        <tr data-id="${escapeHTML(record.id)}">
          <td>${escapeHTML(event.timestamp || "")}</td>
          <td>${escapeHTML(harness.name || "")}</td>
          <td>${escapeHTML(info.action || "")}</td>
          <td class="severity-${escapeHTML(event.severity || "")}">${escapeHTML(event.severity || "")}</td>
          <td>${escapeHTML(session.id || "")}</td>
          <td>${escapeHTML(event.repository || "")}</td>
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
  $("#event-json").textContent = JSON.stringify(record.event, null, 2);
  $("#drawer").classList.add("open");
  $("#drawer").setAttribute("aria-hidden", "false");
}

function closeDrawer() {
  $("#drawer").classList.remove("open");
  $("#drawer").setAttribute("aria-hidden", "true");
}

function escapeHTML(value) {
  return String(value)
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

$("#refresh").addEventListener("click", () => load().catch(console.error));
$("#filters").addEventListener("submit", (event) => {
  event.preventDefault();
  load().catch(console.error);
});
$("#close-drawer").addEventListener("click", closeDrawer);

load().catch((err) => {
  $("#events").innerHTML = `<tr><td colspan="7">Failed to load dashboard: ${escapeHTML(err.message)}</td></tr>`;
});
setInterval(() => load().catch(console.error), 10000);
