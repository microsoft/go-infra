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
const safetyTitle = document.querySelector("#safety-title");
const safetyCopy = document.querySelector("#safety-copy");
const pipelineRunLink = document.querySelector("#pipeline-run-link");
const findRunsButton = document.querySelector("#find-runs-button");
const runCandidates = document.querySelector("#run-candidates");
const candidateCount = document.querySelector("#candidate-count");
const candidateList = document.querySelector("#candidate-list");
const unofficialDemoControls = document.querySelector("#unofficial-demo-controls");
const unofficialDemoUnavailable = document.querySelector("#unofficial-demo-unavailable");
const unofficialDemoSource = document.querySelector("#unofficial-demo-source");
const unofficialDemoParameters = document.querySelector("#unofficial-demo-parameters");
const unofficialDemoDigest = document.querySelector("#unofficial-demo-digest");
const unofficialDemoPhrase = document.querySelector("#unofficial-demo-phrase");
const unofficialDemoConfirmation = document.querySelector("#unofficial-demo-confirmation");
const unofficialDemoButton = document.querySelector("#unofficial-demo-button");
const unofficialDemoRunLink = document.querySelector("#unofficial-demo-run-link");
const unofficialDemoProgressLabel = document.querySelector("#unofficial-demo-progress-label");
const unofficialDemoProgressCount = document.querySelector("#unofficial-demo-progress-count");
const unofficialDemoProgressBar = document.querySelector("#unofficial-demo-progress-bar");
const unofficialDemoStatusCounts = document.querySelector("#unofficial-demo-status-counts");
const unofficialDemoStepList = document.querySelector("#unofficial-demo-step-list");

let plan = null;
let eventSource = null;
let unofficialDemoEventSource = null;
let toastTimer = null;
let preflight = null;
let executionActive = false;
let unofficialDemoActive = false;

loadExistingPlan();
loadPreflight();

form.addEventListener("submit", async (event) => {
  event.preventDefault();
  setBusy(planButton, true, "Building plan…");
  try {
    const input = currentFormInput();
    plan = await requestJSON("/api/plan", {
      method: "POST",
      body: JSON.stringify(input),
    });
    renderPlan(plan);
    connectEvents();
  } catch (error) {
    showError(error.message);
  } finally {
    setBusy(planButton, false, "Build go-images plan");
  }
});

findRunsButton.addEventListener("click", async () => {
  setBusy(findRunsButton, true, "Searching recent runs…");
  try {
    const { versions } = currentFormInput();
    const result = await requestJSON("/api/go-images/runs/search", {
      method: "POST",
      body: JSON.stringify({ versions }),
    });
    renderCandidates(result.candidates, result.versions);
  } catch (error) {
    showError(error.message);
  } finally {
    setBusy(findRunsButton, false, "Find existing runs for versions");
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

unofficialDemoConfirmation.addEventListener("input", updateUnofficialDemoButton);

unofficialDemoButton.addEventListener("click", async () => {
  setBusy(unofficialDemoButton, true, plan?.unofficialDemo?.run?.buildId ? "Resuming monitoring…" : "Queueing real demo…");
  try {
    await requestJSON("/api/go-images/unofficial-demo/start", {
      method: "POST",
      body: JSON.stringify({
        planDigest: plan.unofficialDemo.planDigest,
        confirmation: unofficialDemoConfirmation.value,
      }),
    });
    connectUnofficialDemoEvents();
    await refreshPlanMetadata();
  } catch (error) {
    showError(error.message);
    updateUnofficialDemoButton();
  }
});

function renderPlan(nextPlan) {
  document.querySelector("#versions").value = nextPlan.input.versions.join("\n");
  emptyState.hidden = true;
  planContent.hidden = false;
  const restored = nextPlan.restored ? " · restored from disk" : "";
  const imported = nextPlan.run?.imported ? " · imported existing run" : "";
  planSubtitle.textContent = `${nextPlan.input.versions.join(", ")} · direct pipeline ${nextPlan.pipeline.definitionId} · ${nextPlan.steps.length} steps · production queueing disabled${restored}${imported}`;
  renderPipeline(nextPlan.pipeline);
  renderPipelineRun(nextPlan.run);
  stepList.replaceChildren(...nextPlan.steps.map(createStepCard));
  demoButton.disabled = Boolean(nextPlan.run?.imported);
  demoButton.textContent = nextPlan.run?.imported ? "Imported run loaded" : "Simulate queue and monitor";
  const snapshot = initialPlanSnapshot(nextPlan);
  updateSteps(snapshot);
  updateProgress(snapshot);
  renderUnofficialDemo(nextPlan.unofficialDemo);
}

function initialPlanSnapshot(nextPlan) {
  const steps = nextPlan.steps.map((step) => ({ ...step, status: "waiting" }));
  if (nextPlan.run?.buildId) {
    const queue = steps.find((step) => step.id === "go-images.pipeline.queue");
    if (queue) queue.status = "succeeded";
  }
  if (nextPlan.run?.complete) {
    for (const step of steps) step.status = "succeeded";
  }
  return { active: false, steps };
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
    preflight = await requestJSON("/api/preflight");
    preflightList.replaceChildren(...preflight.checks.map((check) => {
      const item = document.createElement("li");
      item.dataset.status = check.status;
      item.title = check.details;
      item.textContent = check.name;
      return item;
    }));
    if (preflight.azureReadOnlyEnabled) {
      safetyTitle.textContent = preflight.unofficialDemoEnabled ? "Real unofficial demo enabled" : "Read-only Azure discovery ready";
      safetyCopy.textContent = preflight.unofficialDemoEnabled
        ? "Pipeline 1492 can be queued only after importing a completed pipeline 1023 source run and typing an exact confirmation. It publishes dev/ images to the test ACR."
        : "Authenticated pipeline 1023 lookup is enabled. No endpoint can queue, cancel, or approve a run.";
      findRunsButton.hidden = false;
    }
    renderUnofficialDemo(plan?.unofficialDemo);
  } catch (error) {
    showError(`Unable to check local readiness: ${error.message}`);
  }
}

function currentFormInput() {
  return {
    versions: document.querySelector("#versions").value
      .split(/[\n,]+/)
      .map((version) => version.trim())
      .filter(Boolean),
  };
}

function renderCandidates(candidates, versions) {
  runCandidates.hidden = false;
  candidateCount.textContent = `${candidates.length} found`;
  if (!candidates.length) {
    const empty = document.createElement("p");
    empty.className = "candidate-empty";
    empty.textContent = `No recent pipeline 1023 source commits contained ${versions.join(", ")}.`;
    candidateList.replaceChildren(empty);
    return;
  }
  candidateList.replaceChildren(...candidates.map((candidate) => createCandidate(candidate, versions)));
}

function createCandidate(candidate, versions) {
  const card = document.createElement("article");
  card.className = "candidate-card";
  card.dataset.status = candidate.state;

  const header = document.createElement("div");
  header.className = "candidate-card-heading";
  const title = document.createElement("strong");
  title.textContent = `Build ${candidate.buildId}`;
  const status = document.createElement("span");
  status.className = "status-badge";
  status.textContent = candidate.state;
  header.append(title, status);

  const details = document.createElement("p");
  const queued = candidate.queueTime ? new Date(candidate.queueTime).toLocaleString() : "queue time unavailable";
  const commit = candidate.sourceVersion ? candidate.sourceVersion.slice(0, 12) : "commit unavailable";
  const branch = candidate.sourceBranch || "branch unavailable";
  details.textContent = `${queued} · ${branch} @ ${commit}`;

  const switches = document.createElement("p");
  switches.className = "candidate-switches";
  const parameters = candidate.parameters || {};
  switches.textContent = [
    `artifact source ${parameters.sourceBuildPipelineRunId ?? "pipeline default"}`,
    `publish prefix ${parameters.publishRepoPrefix ?? "pipeline default"}`,
  ].join(" · ");

  const actions = document.createElement("div");
  actions.className = "candidate-actions";
  if (candidate.url) {
    const link = document.createElement("a");
    link.href = candidate.url;
    link.target = "_blank";
    link.rel = "noreferrer";
    link.textContent = "Open Azure run";
    actions.append(link);
  }
  const importButton = document.createElement("button");
  importButton.type = "button";
  importButton.className = "button button-secondary";
  importButton.textContent = candidate.state === "succeeded" ? "Load completed run" : "Load and monitor";
  importButton.addEventListener("click", async () => {
    setBusy(importButton, true, "Validating run…");
    try {
      const input = currentFormInput();
      input.versions = versions;
      plan = await requestJSON("/api/go-images/runs/import", {
        method: "POST",
        body: JSON.stringify({ buildId: candidate.buildId, ...input }),
      });
      renderPlan(plan);
      connectEvents();
      runCandidates.hidden = true;
      if (!plan.run.complete) {
        await requestJSON("/api/go-images/runs/monitor", { method: "POST", body: "{}" });
        executionActive = true;
      }
    } catch (error) {
      showError(error.message);
      setBusy(importButton, false, candidate.state === "succeeded" ? "Load completed run" : "Load and monitor");
    }
  });
  actions.append(importButton);
  card.append(header, details, switches, actions);
  return card;
}

function renderPipelineRun(run) {
  if (!run?.buildId) {
    pipelineRunLink.hidden = true;
    pipelineRunLink.removeAttribute("href");
    pipelineRunLink.textContent = "";
    return;
  }
  pipelineRunLink.href = run.url;
  const origin = run.imported ? " · imported" : "";
  const source = run.sourceVersion ? ` · ${run.sourceVersion.slice(0, 12)}` : "";
  pipelineRunLink.textContent = `Open Azure Pipeline run ${run.buildId}${run.complete ? " · complete" : " · monitoring"}${source}${origin}`;
  pipelineRunLink.hidden = false;
}

function renderUnofficialDemo(demo) {
  const visible = Boolean(demo?.eligible && preflight?.unofficialDemoEnabled);
  const unavailable = Boolean(demo?.enabled && demo?.unavailableReason && preflight?.unofficialDemoEnabled);
  unofficialDemoUnavailable.hidden = !unavailable;
  unofficialDemoUnavailable.textContent = unavailable ? `Real unofficial demo unavailable: ${demo.unavailableReason}` : "";
  unofficialDemoControls.hidden = !visible;
  if (!visible) {
    if (unofficialDemoEventSource) {
      unofficialDemoEventSource.close();
      unofficialDemoEventSource = null;
    }
    return;
  }

  unofficialDemoSource.textContent = `pipeline 1023 build ${demo.sourceBuildId} · ${demo.sourceBranch} @ ${demo.sourceVersion}`;
  unofficialDemoParameters.textContent = Object.entries(demo.parameters)
    .filter(([name]) => name !== "_info")
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([name, value]) => `${name}=${value}`)
    .join(" · ");
  unofficialDemoDigest.textContent = demo.planDigest;
  unofficialDemoPhrase.textContent = demo.confirmation;
  if (unofficialDemoConfirmation.dataset.phrase !== demo.confirmation) {
    unofficialDemoConfirmation.value = "";
    unofficialDemoConfirmation.dataset.phrase = demo.confirmation;
  }
  unofficialDemoStepList.replaceChildren(...demo.steps.map(createStepCard));
  const snapshot = initialUnofficialDemoSnapshot(demo);
  updateStepsInList(snapshot, unofficialDemoStepList);
  updateProgressElements(snapshot, {
    label: unofficialDemoProgressLabel,
    count: unofficialDemoProgressCount,
    bar: unofficialDemoProgressBar,
    counts: unofficialDemoStatusCounts,
  }, "Real demo");
  renderUnofficialDemoRun(demo.run);
  updateUnofficialDemoButton();
  connectUnofficialDemoEvents();
}

function initialUnofficialDemoSnapshot(demo) {
  const steps = demo.steps.map((step) => ({ ...step, status: "waiting" }));
  if (demo.run?.buildId) {
    const queue = steps.find((step) => step.id === "go-images.unofficial-demo.queue");
    if (queue) queue.status = "succeeded";
  }
  if (demo.run?.complete) {
    for (const step of steps) step.status = "succeeded";
  }
  return { active: false, steps };
}

function renderUnofficialDemoRun(run) {
  if (!run?.buildId) {
    unofficialDemoRunLink.hidden = true;
    unofficialDemoRunLink.removeAttribute("href");
    unofficialDemoRunLink.textContent = "";
    return;
  }
  unofficialDemoRunLink.href = run.url;
  unofficialDemoRunLink.textContent = `Open unofficial Azure run ${run.buildId}${run.complete ? " · complete" : " · monitoring"}`;
  unofficialDemoRunLink.hidden = false;
}

function updateUnofficialDemoButton() {
  const demo = plan?.unofficialDemo;
  const enabled = Boolean(demo?.eligible && preflight?.unofficialDemoEnabled);
  const complete = Boolean(demo?.run?.complete);
  unofficialDemoButton.disabled = !enabled || complete || executionActive || unofficialDemoActive ||
    unofficialDemoConfirmation.value !== demo?.confirmation;
  if (complete) {
    unofficialDemoButton.textContent = "Unofficial demo completed";
  } else if (demo?.run?.buildId) {
    unofficialDemoButton.textContent = "Resume real demo monitoring";
  } else {
    unofficialDemoButton.textContent = "Queue real unofficial demo";
  }
}

async function refreshPlanMetadata() {
  const response = await fetch("/api/plan");
  if (!response.ok) return;
  const latest = await response.json();
  if (!plan || latest.sessionId !== plan.sessionId) return;
  plan.run = latest.run;
  plan.unofficialDemo = latest.unofficialDemo;
  renderPipelineRun(plan.run);
  renderUnofficialDemoRun(plan.unofficialDemo.run);
  updateUnofficialDemoButton();
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
    executionActive = snapshot.active;
    if (snapshot.steps.length) {
      updateSteps(snapshot);
      updateProgress(snapshot);
    }
    demoButton.disabled = snapshot.active || Boolean(plan?.run?.imported);
    demoButton.textContent = plan?.run?.imported
      ? snapshot.active ? "Monitoring imported run…" : "Imported run loaded"
      : snapshot.active ? "Simulation running…" : "Simulate queue and monitor";
    updateUnofficialDemoButton();
    if (!snapshot.active && snapshot.error) {
      showError(snapshot.error);
    }
    if (snapshot.steps.some((step) => step.id === "go-images.pipeline.queue" && step.status === "succeeded") || !snapshot.active) {
      refreshPlanMetadata().catch((error) => showError(error.message));
    }
  });
}

function connectUnofficialDemoEvents() {
  if (!plan?.unofficialDemo?.eligible) return;
  if (unofficialDemoEventSource) {
    unofficialDemoEventSource.close();
  }
  unofficialDemoEventSource = new EventSource("/api/go-images/unofficial-demo/events");
  unofficialDemoEventSource.addEventListener("state", (event) => {
    const snapshot = JSON.parse(event.data);
    unofficialDemoActive = snapshot.active;
    if (snapshot.steps.length) {
      updateStepsInList(snapshot, unofficialDemoStepList);
      updateProgressElements(snapshot, {
        label: unofficialDemoProgressLabel,
        count: unofficialDemoProgressCount,
        bar: unofficialDemoProgressBar,
        counts: unofficialDemoStatusCounts,
      }, "Real demo");
    }
    updateUnofficialDemoButton();
    if (!snapshot.active && snapshot.error) {
      showError(snapshot.error);
    }
    if (snapshot.steps.some((step) => step.id === "go-images.unofficial-demo.queue" && step.status === "succeeded") || !snapshot.active) {
      refreshPlanMetadata().catch((error) => showError(error.message));
    }
  });
}

function updateSteps(snapshot) {
  updateStepsInList(snapshot, stepList);
}

function updateStepsInList(snapshot, list) {
  for (const step of snapshot.steps) {
    const card = Array.from(list.children).find((candidate) => candidate.dataset.stepId === step.id);
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
      list.scrollTo({
        top: Math.max(0, card.offsetTop - list.offsetTop - 12),
        behavior: "smooth",
      });
    }
  }
}

function updateProgressElements(snapshot, elements, name) {
  const steps = snapshot.steps || [];
  const terminal = new Set(["succeeded", "failed", "blocked", "canceled"]);
  const complete = steps.filter((step) => terminal.has(step.status)).length;
  const succeeded = steps.filter((step) => step.status === "succeeded").length;
  const percent = steps.length ? Math.round((complete / steps.length) * 100) : 0;
  elements.count.textContent = `${complete} / ${steps.length} complete`;
  elements.bar.style.width = `${percent}%`;
  if (snapshot.active) {
    elements.label.textContent = `${name} in progress`;
  } else if (complete === steps.length && steps.length) {
    elements.label.textContent = succeeded === steps.length ? `${name} completed successfully` : `${name} stopped`;
  } else {
    elements.label.textContent = "Ready for confirmation";
  }
  const counts = new Map();
  for (const step of steps) counts.set(step.status, (counts.get(step.status) || 0) + 1);
  elements.counts.replaceChildren(...Array.from(counts.entries()).map(([status, count]) => {
    const label = document.createElement("span");
    label.textContent = `${status} ${count}`;
    return label;
  }));
}

function updateProgress(snapshot) {
  const steps = snapshot.steps || [];
  const terminal = new Set(["succeeded", "failed", "blocked", "canceled"]);
  const complete = steps.filter((step) => terminal.has(step.status)).length;
  const succeeded = steps.filter((step) => step.status === "succeeded").length;
  const percent = steps.length ? Math.round((complete / steps.length) * 100) : 0;

  progressCount.textContent = `${complete} / ${steps.length} complete`;
  progressBar.style.width = `${percent}%`;
  const imported = Boolean(plan?.run?.imported);
  progressLabel.textContent = snapshot.active
    ? imported ? "Monitoring imported run" : "Simulation in progress"
    : complete === steps.length && steps.length
      ? imported ? "Imported run complete" : "Simulation complete"
      : imported ? "Imported run ready to monitor" : "Ready to simulate";

  const counts = new Map();
  for (const step of steps) counts.set(step.status, (counts.get(step.status) || 0) + 1);
  statusCounts.replaceChildren(...Array.from(counts.entries()).map(([status, count]) => {
    const label = document.createElement("span");
    label.textContent = `${status} ${count}`;
    return label;
  }));
  if (!snapshot.active && complete === steps.length && succeeded === steps.length && steps.length) {
    progressLabel.textContent = imported ? "Imported run completed successfully" : "Simulation completed successfully";
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
