"use strict";

const form = document.querySelector("#plan-form");
const planButton = document.querySelector("#plan-button");
const demoButton = document.querySelector("#demo-button");
const emptyState = document.querySelector("#empty-state");
const planContent = document.querySelector("#plan-content");
const planSubtitle = document.querySelector("#plan-subtitle");
const stepList = document.querySelector("#step-list");
const progressLabel = document.querySelector("#progress-label");
const progressCount = document.querySelector("#progress-count");
const progressBar = document.querySelector("#progress-bar");
const statusCounts = document.querySelector("#status-counts");
const toast = document.querySelector("#toast");
const connectionDot = document.querySelector("#connection-dot");
const connectionLabel = document.querySelector("#connection-label");
const preflightList = document.querySelector("#preflight-list");
const pipelineID = document.querySelector("#pipeline-id");
const pipelineTarget = document.querySelector("#pipeline-target");
const pipelineParameters = document.querySelector("#pipeline-parameters");

let plan = null;
let eventSource = null;
let toastTimer = null;

loadExistingPlan();
loadPreflight();

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  setBusy(planButton, true, "Building plan…");
  try {
    const versions = document.querySelector("#versions").value
      .split(/[\n,]+/)
      .map((version) => version.trim())
      .filter(Boolean);
    plan = await requestJSON("/api/plan", {
      method: "POST",
      body: JSON.stringify({
        versions,
        runner: document.querySelector("#runner").value.trim(),
        security: document.querySelector("#security").checked,
        variableGroup: document.querySelector("#variable-group").value.trim(),
        releaseIssue: Number(document.querySelector("#release-issue").value) || 0,
      }),
    });
    renderPlan(plan);
    connectEvents();
  } catch (error) {
    showError(error.message);
  } finally {
    setBusy(planButton, false, "Build go-images plan");
  }
});

demoButton.addEventListener("click", async () => {
  setBusy(demoButton, true, "Starting safe simulation…");
  try {
    await requestJSON("/api/demo/start", { method: "POST", body: "{}" });
  } catch (error) {
    showError(error.message);
    demoButton.disabled = false;
  }
});

function renderPlan(nextPlan) {
  document.querySelector("#versions").value = nextPlan.input.versions.join("\n");
  document.querySelector("#runner").value = nextPlan.input.runner;
  document.querySelector("#security").checked = nextPlan.input.security;
  document.querySelector("#variable-group").value = nextPlan.input.variableGroup;
  document.querySelector("#release-issue").value = nextPlan.input.releaseIssue || "";
  emptyState.hidden = true;
  planContent.hidden = false;
  const restored = nextPlan.restored ? " · restored from disk" : "";
  planSubtitle.textContent = `${nextPlan.input.versions.join(", ")} · pipeline ${nextPlan.pipeline.definitionId} · ${nextPlan.steps.length} steps · execution disabled${restored}`;
  renderPipeline(nextPlan.pipeline);
  stepList.replaceChildren(...nextPlan.steps.map(createStepCard));
  demoButton.disabled = false;
  updateProgress({ active: false, steps: nextPlan.steps.map((step) => ({ ...step, status: "waiting" })) });
}

function renderPipeline(pipeline) {
  pipelineID.textContent = `${pipeline.definitionId} · ${pipeline.name}`;
  pipelineTarget.textContent = `${pipeline.organization}/${pipeline.project}`;
  const entries = Object.entries(pipeline.parameters).sort(([left], [right]) => left.localeCompare(right));
  pipelineParameters.replaceChildren(...entries.flatMap(([name, value]) => {
    const term = document.createElement("dt");
    term.textContent = name;
    const description = document.createElement("dd");
    description.textContent = value;
    return [term, description];
  }));
}

async function loadExistingPlan() {
  try {
    const response = await fetch("/api/plan");
    if (response.status === 204) return;
    const data = await response.json().catch(() => ({}));
    if (!response.ok) {
      throw new Error(data.error || `${response.status} ${response.statusText}`);
    }
    plan = data;
    renderPlan(plan);
    connectEvents();
  } catch (error) {
    showError(`Unable to restore release plan: ${error.message}`);
  }
}

async function loadPreflight() {
  try {
    const report = await requestJSON("/api/preflight");
    preflightList.replaceChildren(...report.checks.map((check) => {
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

function createStepCard(step) {
  const item = document.createElement("li");
  item.className = "step-card";
  item.dataset.stepId = step.id;
  item.dataset.status = "waiting";

  const head = document.createElement("div");
  head.className = "step-card-head";
  const title = document.createElement("h3");
  title.className = "step-title";
  title.textContent = step.name;
  const status = document.createElement("span");
  status.className = "status-badge";
  status.textContent = "waiting";
  head.append(title, status);

  const id = document.createElement("code");
  id.className = "step-id";
  id.textContent = step.id;
  item.append(head, id);

  if (step.dependsOn?.length) {
    const dependencies = document.createElement("div");
    dependencies.className = "dependencies";
    dependencies.title = "Dependencies";
    for (const dependency of step.dependsOn) {
      const tag = document.createElement("span");
      tag.textContent = dependency;
      dependencies.append(tag);
    }
    item.append(dependencies);
  }
  return item;
}

function connectEvents() {
  if (eventSource) {
    eventSource.close();
  }
  eventSource = new EventSource("/api/events");
  eventSource.addEventListener("open", () => setConnection(true));
  eventSource.addEventListener("error", () => setConnection(false));
  eventSource.addEventListener("state", (event) => {
    const snapshot = JSON.parse(event.data);
    if (snapshot.steps.length) {
      updateSteps(snapshot);
      updateProgress(snapshot);
    }
    demoButton.disabled = snapshot.active;
    demoButton.textContent = snapshot.active ? "Simulation running…" : "Simulate queue and monitor";
    if (!snapshot.active && snapshot.error) {
      showError(snapshot.error);
    }
  });
}

function updateSteps(snapshot) {
  for (const step of snapshot.steps) {
    const card = Array.from(stepList.children).find((candidate) => candidate.dataset.stepId === step.id);
    if (!card) continue;
    card.dataset.status = step.status;
    card.querySelector(".status-badge").textContent = step.status;
    card.querySelector(".step-error")?.remove();
    if (step.error) {
      const error = document.createElement("p");
      error.className = "step-error";
      error.textContent = step.error;
      card.append(error);
    }
    if (step.status === "running") {
      stepList.scrollTo({
        top: Math.max(0, card.offsetTop - stepList.offsetTop - 12),
        behavior: "smooth",
      });
    }
  }
}

function updateProgress(snapshot) {
  const steps = snapshot.steps || [];
  const terminal = new Set(["succeeded", "failed", "blocked", "canceled"]);
  const complete = steps.filter((step) => terminal.has(step.status)).length;
  const succeeded = steps.filter((step) => step.status === "succeeded").length;
  const percent = steps.length ? Math.round((complete / steps.length) * 100) : 0;

  progressCount.textContent = `${complete} / ${steps.length} complete`;
  progressBar.style.width = `${percent}%`;
  progressLabel.textContent = snapshot.active ? "Simulation in progress" : complete === steps.length && steps.length ? "Simulation complete" : "Ready to simulate";

  const counts = new Map();
  for (const step of steps) counts.set(step.status, (counts.get(step.status) || 0) + 1);
  statusCounts.replaceChildren(...Array.from(counts.entries()).map(([status, count]) => {
    const label = document.createElement("span");
    label.textContent = `${status} ${count}`;
    return label;
  }));
  if (!snapshot.active && complete === steps.length && succeeded === steps.length && steps.length) {
    progressLabel.textContent = "Simulation completed successfully";
  }
}

async function requestJSON(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(data.error || `${response.status} ${response.statusText}`);
  }
  return data;
}

function setBusy(button, busy, label) {
  button.disabled = busy;
  const text = button.querySelector("span:first-child");
  if (text) text.textContent = label;
  else button.textContent = label;
}

function setConnection(connected) {
  connectionDot.classList.toggle("disconnected", !connected);
  connectionLabel.textContent = connected ? "Live local session" : "Reconnecting…";
}

function showError(message) {
  toast.textContent = message;
  toast.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toast.hidden = true; }, 8000);
}
