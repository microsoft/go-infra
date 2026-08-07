"use strict";

const form = document.querySelector("#plan-form");
const modeCards = Array.from(document.querySelectorAll(".mode-card"));
const rollbackInputs = document.querySelector("#rollback-inputs");
const sourceBuildID = document.querySelector("#source-build-id");
const lockSummary = document.querySelector("#mode-lock-summary");
const planButton = document.querySelector("#plan-button");
const demoButton = document.querySelector("#demo-button");
const emptyState = document.querySelector("#empty-state");
const planContent = document.querySelector("#plan-content");
const planSubtitle = document.querySelector("#plan-subtitle");
const intentBanner = document.querySelector("#intent-banner");
const intentTitle = document.querySelector("#intent-title");
const intentPrefix = document.querySelector("#intent-prefix");
const sourceBranch = document.querySelector("#source-branch");
const sourceCommit = document.querySelector("#source-commit");
const rollbackSourceCard = document.querySelector("#rollback-source-card");
const rollbackBuild = document.querySelector("#rollback-build");
const rollbackBuildLink = document.querySelector("#rollback-build-link");
const pipelineID = document.querySelector("#pipeline-id");
const pipelineTarget = document.querySelector("#pipeline-target");
const pipelineParameters = document.querySelector("#pipeline-parameters");
const stepGraph = document.querySelector("#step-graph");
const stepEdges = document.querySelector("#step-edges");
const stepList = document.querySelector("#step-list");
const progressLabel = document.querySelector("#progress-label");
const progressCount = document.querySelector("#progress-count");
const progressBar = document.querySelector("#progress-bar");
const statusCounts = document.querySelector("#status-counts");
const pipelineRunLink = document.querySelector("#pipeline-run-link");
const executionControls = document.querySelector("#execution-controls");
const executionUnavailable = document.querySelector("#execution-unavailable");
const executionTitle = document.querySelector("#execution-title");
const executionWarning = document.querySelector("#execution-warning");
const runConfirmation = document.querySelector("#run-confirmation");
const runConfirmationCopy = document.querySelector("#run-confirmation-copy");
const executionCancel = document.querySelector("#execution-cancel");
const executionButton = document.querySelector("#execution-button");
const safetyTitle = document.querySelector("#safety-title");
const safetyCopy = document.querySelector("#safety-copy");
const preflightList = document.querySelector("#preflight-list");
const toast = document.querySelector("#toast");

let plan = null;
let preflight = null;
let eventSource = null;
let executionActive = false;
let runConfirmationPending = false;
let toastTimer = null;

for (const card of modeCards) {
  card.querySelector("input").addEventListener("change", updateModeSelection);
}
form.addEventListener("submit", prepareRelease);
demoButton.addEventListener("click", startSimulation);
executionButton.addEventListener("click", handleExecutionAction);
executionCancel.addEventListener("click", cancelRunConfirmation);
if ("ResizeObserver" in globalThis) {
  new ResizeObserver(() => renderGraphEdges()).observe(stepList);
} else {
  globalThis.addEventListener("resize", renderGraphEdges);
}

updateModeSelection();
loadExistingPlan();
loadPreflight();

function selectedMode() {
  return form.elements["release-mode"].value;
}

function updateModeSelection() {
  const mode = selectedMode();
  for (const card of modeCards) card.classList.toggle("selected", card.dataset.mode === mode);
  rollbackInputs.hidden = mode !== "rollback";
  sourceBuildID.required = mode === "rollback";
  const descriptions = {
    normal: ["Normal release is locked.", "Current main, current-build artifacts, and public/ are selected server-side."],
    rollback: ["Only the source build is editable.", "The server locks current main and public/, then validates the selected build."],
    test: ["Test release is locked to dev/.", "Current main is built normally, but publication is isolated under the dev/ prefix."],
  };
  lockSummary.querySelector("strong").textContent = descriptions[mode][0];
  lockSummary.querySelector("p").lastChild.textContent = ` ${descriptions[mode][1]}`;
  planButton.querySelector("span:first-child").textContent = `Prepare ${formatMode(mode).toLowerCase()} release`;
}

async function prepareRelease(event) {
  event.preventDefault();
  const mode = selectedMode();
  const input = { mode };
  if (mode === "rollback") input.sourceBuildId = sourceBuildID.value.trim();
  setBusy(planButton, true, "Resolving and validating…");
  try {
    plan = await requestJSON("/api/plan", { method: "POST", body: JSON.stringify(input) });
    renderPlan(plan);
    connectEvents();
  } catch (error) {
    showError(error.message);
  } finally {
    setBusy(planButton, false, `Prepare ${formatMode(mode).toLowerCase()} release`);
  }
}

async function startSimulation() {
  setBusy(demoButton, true, "Starting simulation…");
  try {
    await requestJSON("/api/demo/start", { method: "POST", body: "{}" });
  } catch (error) {
    showError(error.message);
    updateActionButtons();
  }
}

async function startRelease() {
  setBusy(executionButton, true, plan?.execution?.run?.buildId ? "Resuming monitoring…" : "Queueing release…");
  try {
    await requestJSON("/api/go-images/release/start", {
      method: "POST",
      body: JSON.stringify({
        planDigest: plan.execution.planDigest,
        confirmed: true,
      }),
    });
    connectEvents();
    await refreshPlan();
  } catch (error) {
    showError(error.message);
    updateExecutionButton();
  }
}

function renderPlan(nextPlan) {
  plan = nextPlan;
  selectMode(nextPlan.input.mode);
  if (nextPlan.input.sourceBuildId) sourceBuildID.value = nextPlan.input.sourceBuildId;
  emptyState.hidden = true;
  planContent.hidden = false;

  const restored = nextPlan.restored ? " · restored from disk" : "";
  planSubtitle.textContent = `${formatMode(nextPlan.input.mode)} release · pipeline ${nextPlan.pipeline.definitionId} · ${nextPlan.steps.length} steps${restored}`;
  intentBanner.dataset.mode = nextPlan.input.mode;
  intentTitle.textContent = intentText(nextPlan.input.mode, nextPlan.rollbackSource?.buildId);
  intentPrefix.textContent = nextPlan.pipeline.parameters.publishRepoPrefix;

  sourceBranch.textContent = nextPlan.source.branch;
  sourceCommit.textContent = nextPlan.source.commit;

  rollbackSourceCard.hidden = !nextPlan.rollbackSource;
  if (nextPlan.rollbackSource) {
    rollbackBuild.textContent = `Pipeline 1023 build ${nextPlan.rollbackSource.buildId}`;
    rollbackBuildLink.href = nextPlan.rollbackSource.url || `https://dev.azure.com/dnceng/internal/_build/results?buildId=${nextPlan.rollbackSource.buildId}`;
  }

  renderPipeline(nextPlan.pipeline);
  renderStepGraph(nextPlan.steps);
  const snapshot = initialSnapshot(nextPlan);
  executionActive = snapshot.active;
  updateSteps(snapshot);
  updateProgress(snapshot);
  renderRun(nextPlan.execution.run);
  renderExecution(nextPlan.execution, nextPlan.input.mode);
  updateActionButtons();
}

function selectMode(mode) {
  const input = form.querySelector(`input[value="${mode}"]`);
  if (input) input.checked = true;
  updateModeSelection();
}

function intentText(mode, buildID) {
  if (mode === "normal") return "Build current main and publish production images";
  if (mode === "rollback") return `Republish artifacts from build ${buildID}`;
  return "Build current main and publish a dev/ test release";
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

function renderExecution(execution, mode) {
  const visible = Boolean(execution?.eligible && preflight?.goImagesExecutionEnabled);
  executionControls.hidden = !visible;
  executionUnavailable.hidden = visible || !execution?.unavailableReason;
  executionUnavailable.textContent = execution?.unavailableReason || "";
  if (!visible) return;

  if (executionControls.dataset.planDigest !== execution.planDigest) {
    runConfirmationPending = false;
    executionControls.dataset.planDigest = execution.planDigest || "";
  }
  executionWarning.textContent = mode === "test"
    ? "This queues a real build and may use production signing resources, but publication is fixed to dev/ rather than public/."
    : mode === "rollback"
      ? `This republishes artifacts from build ${plan.input.sourceBuildId} under public/. It does not rebuild those images.`
      : "This builds current main, performs production signing, and publishes production images under public/.";
  runConfirmationCopy.textContent = mode === "test"
    ? "Confirm run to queue pipeline 1023 with publication locked to dev/."
    : mode === "rollback"
      ? `Confirm run to republish artifacts from build ${plan.input.sourceBuildId} to public/.`
      : "Confirm run to build, sign, and publish current main to public/.";
  executionControls.dataset.mode = mode;
  updateExecutionButton();
}

function handleExecutionAction() {
  if (plan?.execution?.run?.buildId) {
    startRelease();
    return;
  }
  if (!runConfirmationPending) {
    runConfirmationPending = true;
    updateExecutionButton();
    return;
  }
  startRelease();
}

function cancelRunConfirmation() {
  runConfirmationPending = false;
  updateExecutionButton();
}

function initialSnapshot(nextPlan) {
  const steps = nextPlan.steps.map((step) => ({ ...step, status: "waiting" }));
  const monitoring = Boolean(nextPlan.execution?.enabled && nextPlan.execution?.run?.buildId && !nextPlan.execution.run.complete);
  if (nextPlan.execution?.run?.buildId) {
    for (const id of ["go-images.release.verify-internal-mirror", "go-images.release.queue"]) {
      const completedStep = steps.find((step) => step.id === id);
      if (completedStep) completedStep.status = "succeeded";
    }
    const monitor = steps.find((step) => step.id === "go-images.release.wait");
    if (monitor && monitoring) monitor.status = "running";
  }
  if (nextPlan.execution?.run?.complete) {
    for (const step of steps) step.status = "succeeded";
  }
  return { active: monitoring, steps };
}

function createStepCard(step) {
  const item = document.createElement("article");
  item.className = "step-card";
  item.dataset.stepId = step.id;
  item.dataset.status = "waiting";
  item.setAttribute("role", "listitem");
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
  const liveProgress = document.createElement("div");
  liveProgress.className = "step-live-progress";
  liveProgress.hidden = true;
  liveProgress.setAttribute("aria-live", "polite");
  const progressSummary = document.createElement("strong");
  progressSummary.className = "step-progress-summary";
  const progressDetail = document.createElement("p");
  progressDetail.className = "step-progress-detail";
  const progressTrack = document.createElement("div");
  progressTrack.className = "step-progress-track";
  progressTrack.hidden = true;
  const progressFill = document.createElement("span");
  progressTrack.append(progressFill);
  const progressItems = document.createElement("ul");
  progressItems.className = "step-progress-items";
  liveProgress.append(progressSummary, progressDetail, progressTrack, progressItems);
  item.append(head, id, liveProgress);
  return item;
}

function renderStepGraph(steps) {
  const levels = computeStepLevels(steps);
  const maxLevel = Math.max(0, ...levels.values());
  const columns = Array.from({ length: maxLevel + 1 }, () => []);
  for (const step of steps) columns[levels.get(step.id) || 0].push(step);

  stepList.replaceChildren(...columns.map((stepsAtLevel, level) => {
    const column = document.createElement("section");
    column.className = "step-graph-column";
    column.dataset.level = String(level);
    const heading = document.createElement("div");
    heading.className = "step-graph-level-heading";
    const label = document.createElement("span");
    label.textContent = level === 0 ? "Start" : level === maxLevel ? "Finish" : `Stage ${level + 1}`;
    const parallel = document.createElement("small");
    parallel.textContent = stepsAtLevel.length > 1 ? `${stepsAtLevel.length} steps in parallel` : "";
    heading.append(label, parallel);
    const nodes = document.createElement("div");
    nodes.className = "step-graph-nodes";
    nodes.replaceChildren(...stepsAtLevel.map(createStepCard));
    column.append(heading, nodes);
    return column;
  }));
  requestAnimationFrame(renderGraphEdges);
}

function computeStepLevels(steps) {
  const byID = new Map(steps.map((step) => [step.id, step]));
  const levels = new Map();
  const visiting = new Set();
  const visit = (step) => {
    if (levels.has(step.id)) return levels.get(step.id);
    if (visiting.has(step.id)) return 0;
    visiting.add(step.id);
    const dependencies = (step.dependsOn || []).map((id) => byID.get(id)).filter(Boolean);
    const level = dependencies.length ? Math.max(...dependencies.map(visit)) + 1 : 0;
    visiting.delete(step.id);
    levels.set(step.id, level);
    return level;
  };
  for (const step of steps) visit(step);
  return levels;
}

function renderGraphEdges() {
  if (!plan?.steps?.length || stepGraph.hidden || !stepList.children.length) return;
  const width = Math.max(stepGraph.scrollWidth, stepGraph.clientWidth);
  const height = Math.max(stepGraph.scrollHeight, stepGraph.clientHeight);
  stepEdges.setAttribute("width", String(width));
  stepEdges.setAttribute("height", String(height));
  stepEdges.setAttribute("viewBox", `0 0 ${width} ${height}`);
  stepEdges.style.width = `${width}px`;
  stepEdges.style.height = `${height}px`;

  const svgNS = "http://www.w3.org/2000/svg";
  const defs = document.createElementNS(svgNS, "defs");
  for (const [status, color] of [["waiting", "#49677f"], ["running", "#39d5ff"], ["succeeded", "#48d597"], ["failed", "#ff7b87"]]) {
    const marker = document.createElementNS(svgNS, "marker");
    marker.id = `step-arrow-${status}`;
    marker.setAttribute("viewBox", "0 0 10 10");
    marker.setAttribute("refX", "9");
    marker.setAttribute("refY", "5");
    marker.setAttribute("markerWidth", "6");
    marker.setAttribute("markerHeight", "6");
    marker.setAttribute("orient", "auto-start-reverse");
    const arrow = document.createElementNS(svgNS, "path");
    arrow.setAttribute("d", "M 0 0 L 10 5 L 0 10 z");
    arrow.setAttribute("fill", color);
    marker.append(arrow);
    defs.append(marker);
  }

  const graphRect = stepGraph.getBoundingClientRect();
  const paths = [];
  for (const step of plan.steps) {
    const target = findStepCard(step.id);
    if (!target) continue;
    const targetRect = target.getBoundingClientRect();
    for (const dependencyID of step.dependsOn || []) {
      const dependency = findStepCard(dependencyID);
      if (!dependency) continue;
      const dependencyRect = dependency.getBoundingClientRect();
      const startX = dependencyRect.right - graphRect.left + stepGraph.scrollLeft;
      const startY = dependencyRect.top + dependencyRect.height / 2 - graphRect.top + stepGraph.scrollTop;
      const endX = targetRect.left - graphRect.left + stepGraph.scrollLeft;
      const endY = targetRect.top + targetRect.height / 2 - graphRect.top + stepGraph.scrollTop;
      const curve = Math.max(36, Math.abs(endX - startX) * 0.42);
      const path = document.createElementNS(svgNS, "path");
      const status = graphEdgeStatus(dependency.dataset.status, target.dataset.status);
      path.classList.add("step-graph-edge", `step-graph-edge-${status}`);
      path.setAttribute("d", `M ${startX} ${startY} C ${startX + curve} ${startY}, ${endX - curve} ${endY}, ${endX} ${endY}`);
      path.setAttribute("marker-end", `url(#step-arrow-${status})`);
      paths.push(path);
    }
  }
  stepEdges.replaceChildren(defs, ...paths);
}

function graphEdgeStatus(dependencyStatus, targetStatus) {
  if ([dependencyStatus, targetStatus].some((status) => ["failed", "blocked", "canceled"].includes(status))) return "failed";
  if (targetStatus === "running") return "running";
  if (dependencyStatus === "succeeded" && targetStatus === "succeeded") return "succeeded";
  return "waiting";
}

function findStepCard(stepID) {
  return Array.from(stepList.querySelectorAll(".step-card")).find((card) => card.dataset.stepId === stepID);
}

function updateSteps(snapshot) {
  for (const step of snapshot.steps || []) {
    const card = findStepCard(step.id);
    if (!card) continue;
    card.dataset.status = step.status;
    card.querySelector(".status-badge").textContent = statusText(step.status);
    card.querySelector(".step-error")?.remove();
    if (step.error) {
      const error = document.createElement("p");
      error.className = "step-error";
      error.textContent = step.error;
      card.append(error);
    }
    updateStepProgress(card, step.progress);
  }
  requestAnimationFrame(renderGraphEdges);
}

function updateStepProgress(card, progress) {
  const container = card.querySelector(".step-live-progress");
  container.hidden = !progress;
  if (!progress) return;
  container.querySelector(".step-progress-summary").textContent = progress.summary || "Working…";
  const detail = container.querySelector(".step-progress-detail");
  detail.textContent = progress.detail || "";
  detail.hidden = !progress.detail;
  const track = container.querySelector(".step-progress-track");
  track.hidden = !(progress.total > 0);
  track.querySelector("span").style.width = progress.total > 0
    ? `${Math.max(0, Math.min(100, Math.round((progress.completed / progress.total) * 100)))}%`
    : "0";
  const items = container.querySelector(".step-progress-items");
  items.replaceChildren(...(progress.items || []).map((item) => {
    const row = document.createElement("li");
    row.textContent = item;
    return row;
  }));
  items.hidden = !(progress.items || []).length;
}

function updateProgress(snapshot) {
  const steps = snapshot.steps || [];
  const terminal = new Set(["succeeded", "failed", "blocked", "canceled"]);
  const complete = steps.filter((step) => terminal.has(step.status)).length;
  const succeeded = steps.filter((step) => step.status === "succeeded").length;
  progressCount.textContent = `${complete} / ${steps.length} complete`;
  progressBar.style.width = `${steps.length ? Math.round((complete / steps.length) * 100) : 0}%`;
  progressLabel.textContent = snapshot.active
    ? "Release workflow in progress"
    : complete === steps.length && steps.length
      ? succeeded === steps.length ? "Workflow completed successfully" : "Workflow stopped"
      : "Ready";
  const counts = new Map();
  for (const step of steps) counts.set(step.status, (counts.get(step.status) || 0) + 1);
  statusCounts.replaceChildren(...Array.from(counts.entries()).map(([status, count]) => {
    const label = document.createElement("span");
    label.textContent = `${statusText(status)} ${count}`;
    return label;
  }));
}

function statusText(status) {
  return status === "running" ? "in progress" : status;
}

function renderRun(run) {
  if (!run?.buildId) {
    pipelineRunLink.hidden = true;
    pipelineRunLink.removeAttribute("href");
    return;
  }
  pipelineRunLink.href = run.url;
  pipelineRunLink.textContent = `Open Azure DevOps run ${run.buildId} ↗ · ${run.complete ? "Completed" : "In progress"}`;
  pipelineRunLink.hidden = false;
}

function updateActionButtons() {
  const hasRun = Boolean(plan?.execution?.run?.buildId);
  const complete = Boolean(plan?.execution?.run?.complete);
  demoButton.disabled = !plan || executionActive || hasRun || complete;
  demoButton.textContent = executionActive ? "Workflow running…" : "Simulate workflow";
  updateExecutionButton();
}

function updateExecutionButton() {
  const execution = plan?.execution;
  const enabled = Boolean(execution?.eligible && preflight?.goImagesExecutionEnabled);
  const complete = Boolean(execution?.run?.complete);
  const mode = plan?.input?.mode;
  runConfirmation.hidden = !runConfirmationPending || complete || executionActive;
  executionCancel.hidden = runConfirmation.hidden;
  executionTitle.textContent = runConfirmationPending
    ? mode === "test" ? "Confirm test release" : mode === "rollback" ? "Confirm rollback / republish" : "Confirm production release"
    : mode === "test" ? "Run test release" : mode === "rollback" ? "Run rollback / republish" : "Run production release";
  executionButton.disabled = !enabled || complete || executionActive;
  executionButton.textContent = complete
    ? "Release completed"
    : executionActive
      ? "Monitoring release…"
    : execution?.run?.buildId
      ? "Resume release monitoring"
      : runConfirmationPending
        ? "Confirm run"
        : mode === "test" ? "Run test release" : mode === "rollback" ? "Run rollback" : "Run production release";
}

function connectEvents() {
  if (!plan) return;
  if (eventSource) eventSource.close();
  eventSource = new EventSource("/api/events");
  eventSource.addEventListener("state", (event) => {
    const snapshot = JSON.parse(event.data);
    executionActive = snapshot.active;
    if (snapshot.steps.length) {
      updateSteps(snapshot);
      updateProgress(snapshot);
    }
    updateActionButtons();
    if (!snapshot.active && snapshot.error) showError(snapshot.error);
    if (!snapshot.active || snapshot.steps.some((step) => step.id === "go-images.release.queue" && step.status === "succeeded")) {
      refreshPlan().catch((error) => showError(error.message));
    }
  });
}

async function refreshPlan() {
  const response = await fetch("/api/plan");
  if (response.status === 204) return;
  const latest = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(latest.error || `${response.status} ${response.statusText}`);
  if (!plan || latest.sessionId !== plan.sessionId) return;
  plan = latest;
  renderRun(latest.execution.run);
  renderExecution(latest.execution, latest.input.mode);
  updateActionButtons();
}

async function loadExistingPlan() {
  try {
    const response = await fetch("/api/plan");
    if (response.status === 204) return;
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.error || `${response.status} ${response.statusText}`);
    renderPlan(data);
    connectEvents();
  } catch (error) {
    showError(`Unable to restore release plan: ${error.message}`);
  }
}

async function loadPreflight() {
  try {
    preflight = await requestJSON("/api/preflight");
    preflightList.replaceChildren(...preflight.checks.map((check) => {
      const item = document.createElement("li");
      item.dataset.status = check.status;
      item.title = check.details;
      item.textContent = check.name;
      return item;
    }));
    if (preflight.goImagesExecutionEnabled) {
      safetyTitle.textContent = "Real pipeline execution enabled";
      safetyCopy.textContent = "Every new run requires an explicit second confirmation. Normal and rollback publish to public/; test is fixed to dev/.";
    } else if (preflight.azureReadOnlyEnabled) {
      safetyTitle.textContent = "Read-only planning ready";
      safetyCopy.textContent = "Current main and rollback builds can be validated. No endpoint can queue a pipeline.";
    } else {
      safetyTitle.textContent = "Azure access disabled";
      safetyCopy.textContent = "Start with read-only access to resolve current main and prepare a plan.";
    }
    if (plan) renderExecution(plan.execution, plan.input.mode);
  } catch (error) {
    showError(`Unable to check local readiness: ${error.message}`);
  }
}

function formatMode(mode) {
  return mode ? `${mode[0].toUpperCase()}${mode.slice(1)}` : "";
}

async function requestJSON(path, options = {}) {
  const response = await fetch(path, {
    ...options,
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(data.error || `${response.status} ${response.statusText}`);
  return data;
}

function setBusy(button, busy, label) {
  button.disabled = busy;
  const text = button.querySelector("span:first-child");
  if (text) text.textContent = label;
  else button.textContent = label;
}

function showError(message) {
  toast.textContent = message;
  toast.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { toast.hidden = true; }, 8000);
}
