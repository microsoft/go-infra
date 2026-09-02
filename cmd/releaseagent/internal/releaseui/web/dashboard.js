"use strict";

const ongoingReleases = document.querySelector("#ongoing-releases");
const recentSection = document.querySelector("#recent-section");
const recentReleases = document.querySelector("#recent-releases");
const processGrid = document.querySelector("#process-grid");

loadDashboard();

async function loadDashboard() {
  const dashboardState = await requestJSON("/api/dashboard");
  renderReleases(ongoingReleases, dashboardState.ongoing, "No release is currently being tracked in this local session.");
  processGrid.replaceChildren(...dashboardState.processes.map(createProcessCard));
  recentSection.hidden = dashboardState.recent.length === 0;
  renderReleases(recentReleases, dashboardState.recent, "");
}

function renderReleases(container, releases, emptyText) {
  if (!releases.length) {
    const empty = document.createElement("article");
    empty.className = "release-empty";
    empty.textContent = emptyText;
    container.replaceChildren(empty);
    return;
  }
  container.replaceChildren(...releases.map(createReleaseCard));
}

function createReleaseCard(release) {
  const card = document.createElement("article");
  card.className = "release-card";

  const icon = document.createElement("span");
  icon.className = "release-icon";
  icon.textContent = release.mark;

  const body = document.createElement("div");
  body.className = "release-card-body";
  const top = document.createElement("div");
  top.className = "release-card-title";
  const title = document.createElement("strong");
  title.textContent = `${release.name} · ${formatMode(release.mode)}`;
  const status = document.createElement("span");
  status.className = "status-badge";
  status.dataset.status = release.status;
  status.textContent = statusText(release.status);
  top.append(title, status);

  const details = document.createElement("p");
  const run = release.runId ? ` · ${release.runLabel || "Run"} ${release.runId}` : "";
  details.textContent = `Updated ${new Date(release.updatedAt).toLocaleString()}${run}`;
  body.append(top, details);

  const link = document.createElement("a");
  link.className = "button button-secondary compact-button";
  link.href = release.href;
  if (/^https?:/.test(release.href)) {
    link.target = "_blank";
    link.rel = "noreferrer";
    link.textContent = "Open Azure run";
  } else {
    link.textContent = "Track release";
  }
  card.append(icon, body, link);
  return card;
}

function createProcessCard(process) {
  const card = document.createElement("a");
  card.className = "process-card";
  card.href = process.href;

  const mark = document.createElement("span");
  mark.className = "process-mark";
  mark.textContent = process.mark;

  const title = document.createElement("h3");
  title.textContent = process.name;
  const description = document.createElement("p");
  description.textContent = process.description;
  const action = document.createElement("span");
  action.className = "process-action";
  action.textContent = "Open release process →";

  card.append(mark, title, description, action);
  return card;
}

function formatMode(mode) {
  return mode ? `${mode[0].toUpperCase()}${mode.slice(1)}` : "Release";
}

function statusText(status) {
  return status === "running" ? "in progress" : status;
}

async function requestJSON(path) {
  const response = await fetch(path);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `${response.status} ${response.statusText}`);
  return data;
}
