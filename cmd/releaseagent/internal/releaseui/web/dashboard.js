"use strict";

const ongoingReleases = document.querySelector("#ongoing-releases");
const recentSection = document.querySelector("#recent-section");
const recentReleases = document.querySelector("#recent-releases");
const processGrid = document.querySelector("#process-grid");
const trackingError = document.querySelector("#tracking-error");
const jsonDialog = document.querySelector("#json-dialog");
const jsonDialogTitle = document.querySelector("#json-dialog-title");
const jsonEditor = document.querySelector("#json-editor");
const jsonError = document.querySelector("#json-error");
const jsonClose = document.querySelector("#json-close");
const jsonCopy = document.querySelector("#json-copy");
const jsonSave = document.querySelector("#json-save");

let editingWorkItem = 0;

loadDashboard();
jsonClose.addEventListener("click", () => jsonDialog.close());
jsonCopy.addEventListener("click", copyJSON);
jsonSave.addEventListener("click", saveJSON);

async function loadDashboard() {
  const dashboardState = await requestJSON("/api/dashboard");
  renderReleases(ongoingReleases, dashboardState.ongoing, "No active release work items.");
  processGrid.replaceChildren(...dashboardState.processes.map(createProcessCard));
  recentSection.hidden = dashboardState.recent.length === 0;
  renderReleases(recentReleases, dashboardState.recent, "");
  trackingError.hidden = !dashboardState.trackingError;
  trackingError.textContent = dashboardState.trackingError || "";
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

  const actions = document.createElement("div");
  actions.className = "release-card-actions";
  if (release.workItemId) {
    const workItemLink = document.createElement("a");
    workItemLink.className = "button button-secondary compact-button";
    workItemLink.href = release.workItemUrl;
    workItemLink.target = "_blank";
    workItemLink.rel = "noreferrer";
    workItemLink.textContent = `Work item ${release.workItemId}`;

    const edit = document.createElement("button");
    edit.className = "button button-secondary compact-button";
    edit.type = "button";
    edit.textContent = "View JSON";
    edit.addEventListener("click", () => openJSON(release));

    const resume = document.createElement("button");
    resume.className = "button button-secondary compact-button";
    resume.type = "button";
    const selectLabel = release.status === "succeeded" ? "Inspect" : "Resume";
    resume.textContent = selectLabel;
    resume.addEventListener("click", () => selectWorkItem(release, resume, selectLabel));
    actions.append(workItemLink, edit, resume);
  } else {
    const link = document.createElement("a");
    link.className = "button button-secondary compact-button";
    link.href = release.href;
    link.textContent = "Track release";
    actions.append(link);
  }
  card.append(icon, body, actions);
  return card;
}

async function selectWorkItem(release, button, label) {
  setButtonBusy(button, true, "Opening…");
  try {
    const selected = await requestJSON(`/api/release-work-items/${release.workItemId}/select`, {
      method: "POST",
      body: "{}",
    });
    location.assign(selected.href);
  } catch (error) {
    showTrackingError(error.message);
    setButtonBusy(button, false, label);
  }
}

async function openJSON(release) {
  try {
    const exported = await requestJSON(`/api/release-work-items/${release.workItemId}/export`);
    editingWorkItem = release.workItemId;
    jsonDialogTitle.textContent = `Work item ${release.workItemId} state`;
    jsonEditor.value = JSON.stringify(exported, null, 2);
    jsonError.hidden = true;
    jsonDialog.showModal();
  } catch (error) {
    showTrackingError(error.message);
  }
}

async function copyJSON() {
  try {
    await navigator.clipboard.writeText(jsonEditor.value);
    setButtonBusy(jsonCopy, true, "Copied");
    setTimeout(() => setButtonBusy(jsonCopy, false, "Copy JSON"), 900);
  } catch (error) {
    showJSONError(error.message);
  }
}

async function saveJSON() {
  let value;
  try {
    value = JSON.parse(jsonEditor.value);
  } catch (error) {
    showJSONError(`Invalid JSON: ${error.message}`);
    return;
  }
  if (!confirm(`Replace the stored state for work item ${editingWorkItem}?`)) return;

  setButtonBusy(jsonSave, true, "Saving…");
  try {
    const updated = await requestJSON(`/api/release-work-items/${editingWorkItem}/import`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(value),
    });
    jsonEditor.value = JSON.stringify(updated, null, 2);
    jsonDialog.close();
    await loadDashboard();
  } catch (error) {
    showJSONError(error.message);
  } finally {
    setButtonBusy(jsonSave, false, "Save repaired state");
  }
}

function setButtonBusy(button, busy, text) {
  button.disabled = busy;
  button.textContent = text;
}

function showTrackingError(message) {
  trackingError.textContent = message;
  trackingError.hidden = false;
}

function showJSONError(message) {
  jsonError.textContent = message;
  jsonError.hidden = false;
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

async function requestJSON(path, options = {}) {
  const response = await fetch(path, options);
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `${response.status} ${response.statusText}`);
  return data;
}
