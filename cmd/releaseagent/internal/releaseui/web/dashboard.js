"use strict";

const ongoingReleases = document.querySelector("#ongoing-releases");
const recentSection = document.querySelector("#recent-section");
const recentReleases = document.querySelector("#recent-releases");
const processGrid = document.querySelector("#process-grid");
const preflightList = document.querySelector("#preflight-list");
const availableCount = document.querySelector("#available-count");
const toast = document.querySelector("#toast");
let toastTimer = null;
let dashboardState = null;

loadDashboard();
loadPreflight();

async function loadDashboard() {
  dashboardState = await requestJSON("/api/dashboard");
  availableCount.textContent = String(dashboardState.processes.filter((process) => process.available).length);
  renderReleases(ongoingReleases, dashboardState.ongoing, "No release is currently being tracked in this local session.");
  processGrid.replaceChildren(...dashboardState.processes.map(createProcessCard));
  recentSection.hidden = dashboardState.recent.length === 0;
  renderReleases(recentReleases, dashboardState.recent, "");
}

async function loadPreflight() {
  try {
    const preflight = await requestJSON("/api/processes/go-images/preflight");
    preflightList.replaceChildren(...preflight.checks.map((check) => {
      const item = document.createElement("li");
      item.dataset.status = check.status;
      item.title = check.details;
      item.textContent = check.name;
      return item;
    }));
  } catch (error) {
    showError(`Unable to check local readiness: ${error.message}`);
  }
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
  icon.textContent = "GI";

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
  const card = document.createElement(process.available ? "a" : "article");
  card.className = "process-card";
  card.dataset.available = String(process.available);
  if (process.available) card.href = process.href;

  const mark = document.createElement("span");
  mark.className = "process-mark";
  mark.textContent = process.mark;

  const status = document.createElement("span");
  status.className = "process-status";
  status.textContent = process.status;

  const title = document.createElement("h3");
  title.textContent = process.name;
  const description = document.createElement("p");
  description.textContent = process.description;
  const action = document.createElement("span");
  action.className = "process-action";
  action.textContent = process.available ? "Open release process →" : "Not available yet";

  card.append(mark, status, title, description, action);
  return card;
}

function formatMode(mode) {
  return mode ? `${mode[0].toUpperCase()}${mode.slice(1)}` : "Release";
}

function statusText(status) {
  return status === "running" ? "in progress" : status;
}

async function requestJSON(path, options = {}) {
  const response = await fetch(path, options);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `${response.status} ${response.statusText}`);
  return data;
}

function showError(message) {
  toast.textContent = message;
  toast.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toast.hidden = true; }, 8000);
}
