"use strict";

(() => {
  const workflowSection = document.querySelector("#workflow-section");
  const workflowHeading = document.querySelector("#workflow-heading");
  const processSafety = document.querySelector("#process-safety");
  const safetyTitle = document.querySelector("#safety-title");
  const safetyCopy = document.querySelector("#safety-copy");
  const preflightList = document.querySelector("#preflight-list");
  const form = document.querySelector("#plan-form");
  const processVariant = document.querySelector("#process-variant");
  const variantDescription = document.querySelector("#variant-description");
  const variantNotice = document.querySelector("#variant-notice");
  const variantNoticeTitle = document.querySelector("#variant-notice-title");
  const variantNoticeCopy = document.querySelector("#variant-notice-copy");
  const processInputs = document.querySelector("#process-inputs");
  const planButton = document.querySelector("#plan-button");
  const emptyState = document.querySelector("#empty-state");
  const planContent = document.querySelector("#plan-content");
  const planSubtitle = document.querySelector("#plan-subtitle");
  const planFacts = document.querySelector("#plan-facts");
  const progressSummary = document.querySelector(".progress-summary");
  const progressLabel = document.querySelector("#progress-label");
  const progressCount = document.querySelector("#progress-count");
  const progressBar = document.querySelector("#progress-bar");
  const statusCounts = document.querySelector("#status-counts");
  const stepGraph = document.querySelector("#step-graph");
  const stepEdges = document.querySelector("#step-edges");
  const stepList = document.querySelector("#step-list");
  const executionLinks = document.querySelector("#execution-links");
  const runLink = document.querySelector("#run-link");
  const recordLink = document.querySelector("#record-link");
  const executionControls = document.querySelector("#execution-controls");
  const executionUnavailable = document.querySelector("#execution-unavailable");
  const executionTitle = document.querySelector("#execution-title");
  const executionWarning = document.querySelector("#execution-warning");
  const runConfirmation = document.querySelector("#run-confirmation");
  const runConfirmationCopy = document.querySelector("#run-confirmation-copy");
  const executionCancel = document.querySelector("#execution-cancel");
  const executionButton = document.querySelector("#execution-button");
  const toast = document.querySelector("#toast");

  const inputRecords = new Map();
  let processDefinition = null;
  let workflow = null;
  let selectedSession = null;
  let sessionReady = false;
  let submitting = "";
  let plan = null;
  let preflight = null;
  let eventSource = null;
  let executionActive = false;
  let runConfirmationPending = false;
  let lastRefreshSignature = "";
  let refreshInFlight = null;
  let toastTimer = null;

  initialize().catch((error) => showError(error.message));

  async function initialize() {
    processDefinition = await globalThis.releaseProcessReady;
    workflow = processDefinition.workflow;

    workflowSection.hidden = false;
    workflowHeading.textContent = workflow.heading;
    planButton.querySelector("span:first-child").textContent = workflow.submitLabel || "Prepare release";
    planButton.disabled = true;

    processVariant.replaceChildren(...workflow.variants.map((variant) => {
      const option = document.createElement("option");
      option.value = variant.id;
      option.textContent = variant.name;
      return option;
    }));
    processVariant.value = processDefinition.id;
    form.addEventListener("submit", prepareRelease);
    processVariant.addEventListener("change", selectVariant);
    executionButton.addEventListener("click", handleExecutionAction);
    executionCancel.addEventListener("click", cancelRunConfirmation);
    if ("ResizeObserver" in globalThis) {
      new ResizeObserver(() => renderGraphEdges()).observe(stepList);
    } else {
      globalThis.addEventListener("resize", renderGraphEdges);
    }

    await selectVariant();
  }

  function selectedVariant() {
    return workflow.variants.find((variant) => variant.id === processVariant.value) || workflow.variants[0];
  }

  function renderVariant() {
    const variant = selectedVariant();
    workflowHeading.textContent = variant.preamble || workflow.heading || "Configure release";
    planButton.querySelector("span:first-child").textContent =
      variant.submitLabel || workflow.submitLabel || "Prepare release";
    variantDescription.textContent = variant.description;
    variantNotice.hidden = !variant.noticeTitle && !variant.notice;
    variantNoticeTitle.textContent = variant.noticeTitle || "";
    variantNoticeCopy.textContent = variant.notice || "";
    inputRecords.clear();
    processInputs.replaceChildren(...(variant.inputs || []).map(createInput));
  }

  async function selectVariant() {
    if (selectedSession) selectedSession.controller.abort();
    if (eventSource) eventSource.close();
    eventSource = null;
    const session = {
      processID: selectedVariant().id,
      endpoint: `/api/processes/${encodeURIComponent(selectedVariant().id)}`,
      controller: new AbortController(),
    };
    selectedSession = session;
    sessionReady = false;
    submitting = "";
    plan = null;
    preflight = null;
    executionActive = false;
    runConfirmationPending = false;
    lastRefreshSignature = "";
    refreshInFlight = null;
    renderVariant();
    history.replaceState(null, "", `/${encodeURIComponent(session.processID)}`);
    planContent.hidden = true;
    emptyState.hidden = false;
    planSubtitle.textContent = "Configure the release to prepare a validated plan.";
    planFacts.replaceChildren();
    stepList.replaceChildren();
    stepEdges.replaceChildren();
    executionControls.hidden = true;
    executionUnavailable.hidden = true;
    processSafety.hidden = false;
    safetyTitle.textContent = "Checking readiness";
    safetyCopy.textContent = "Loading this release process.";
    preflightList.replaceChildren();
    updateActionButtons();

    try {
      const options = { signal: session.controller.signal };
      const [report, currentPlan] = await Promise.all([
        requestJSON(`${session.endpoint}/preflight`, options),
        requestJSON(`${session.endpoint}/plan`, options),
      ]);
      if (selectedSession !== session) return;
      preflight = report;
      renderPreflight();
      if (currentPlan) {
        renderPlan(currentPlan);
        await refreshState(session);
        if (selectedSession !== session) return;
        connectEvents(session);
      }
      sessionReady = true;
    } catch (error) {
      if (selectedSession !== session) return;
      safetyTitle.textContent = "Release state unavailable";
      safetyCopy.textContent = error.message;
      showError(`Unable to load this release: ${error.message}`);
    } finally {
      if (selectedSession === session) updateActionButtons();
    }
  }

  function createInput(input) {
    const wrapper = document.createElement("div");
    wrapper.dataset.inputId = input.id;
    wrapper.className = "variant-input";
    const id = `process-input-${input.id}`;
    const label = document.createElement("label");
    label.htmlFor = id;
    label.textContent = input.label;
    const control = document.createElement("input");
    control.id = id;
    control.name = input.id;
    control.type = input.type;
    control.placeholder = input.placeholder || "";
    control.autocomplete = "off";
    control.required = true;
    if (input.type === "number") {
      control.inputMode = "numeric";
      control.min = "1";
      control.step = "1";
    }
    wrapper.append(label, control);
    if (input.description) {
      const description = document.createElement("p");
      description.className = "field-help";
      description.textContent = input.description;
      wrapper.append(description);
    }
    inputRecords.set(input.id, control);
    return wrapper;
  }

  function inputValue(id) {
    const control = inputRecords.get(id);
    return control ? control.value.trim() : "";
  }

  function serializeInputs() {
    const input = {};
    for (const id of inputRecords.keys()) {
      input[id] = inputValue(id);
    }
    return input;
  }

  function restoreInputs(variantId, values) {
    if (workflow.variants.some((variant) => variant.id === variantId)) {
      processVariant.value = variantId;
    }
    renderVariant();
    if (!values) return;
    for (const [id, control] of inputRecords) {
      if (!Object.prototype.hasOwnProperty.call(values, id)) continue;
      control.value = String(values[id] ?? "");
    }
  }

  async function prepareRelease(event) {
    event.preventDefault();
    if (planButton.disabled) return;
    const session = selectedSession;
    submitting = "prepare";
    updateActionButtons();
    try {
      const prepared = await requestJSON(`${session.endpoint}/plan`, {
        method: "POST",
        body: JSON.stringify(serializeInputs()),
      });
      if (selectedSession !== session) return;
      renderPlan(prepared);
      await refreshState(session);
      if (selectedSession === session) connectEvents(session);
    } catch (error) {
      if (selectedSession === session) showError(error.message);
    } finally {
      if (selectedSession === session) {
        submitting = "";
        updateActionButtons();
      }
    }
  }

  async function startRelease() {
    const session = selectedSession;
    const digest = plan.execution.planDigest;
    const resume = plan.execution.canResume;
    const revision = plan.execution.record?.revision;
    submitting = "start";
    updateActionButtons();
    try {
      await requestJSON(`${session.endpoint}/${resume ? "resume" : "start"}`, {
        method: "POST",
        body: JSON.stringify({ planDigest: digest, confirmed: true, revision }),
      });
      if (selectedSession !== session) return;
      runConfirmationPending = false;
      connectEvents(session);
      await refreshPlan(session);
      await refreshState(session);
    } catch (error) {
      if (selectedSession === session) showError(error.message);
    } finally {
      if (selectedSession === session) {
        submitting = "";
        updateActionButtons();
      }
    }
  }

  function renderPlan(nextPlan) {
    if (nextPlan.variantId !== selectedSession.processID) {
      throw new Error("The returned plan does not belong to the selected release process.");
    }
    plan = nextPlan;
    restoreInputs(nextPlan.variantId, nextPlan.input);
    emptyState.hidden = true;
    planContent.hidden = false;

    const review = nextPlan.plan || {};
    planSubtitle.textContent = review.Subtitle || `${(nextPlan.steps || []).length} workflow steps`;
    planFacts.replaceChildren(...(review.Facts || []).map(createPlanFact));

    const steps = nextPlan.steps || [];
    progressSummary.hidden = steps.length === 0;
    stepGraph.hidden = steps.length === 0;
    if (steps.length) renderStepGraph(steps);
    const snapshot = initialSnapshot(nextPlan);
    executionActive = snapshot.active;
    updateSteps(snapshot);
    updateProgress(snapshot);
    renderLinks(nextPlan.execution);
    renderExecution(nextPlan.execution, review);
    renderPreflight();
    updateActionButtons();
  }

  function createPlanFact(fact) {
    const card = document.createElement("article");
    card.className = "source-card";
    const label = document.createElement("span");
    label.textContent = fact.Label;
    const value = document.createElement("strong");
    value.textContent = fact.Value;
    card.append(label, value);
    if (fact.Detail) {
      const detail = document.createElement("div");
      detail.className = "field-help";
      detail.innerHTML = fact.Detail;
      card.append(detail);
    }
    return card;
  }

  function renderExecution(execution, review) {
    const preflightReady = Boolean(preflight?.externalExecutionEnabled);
    const visible = preflightReady || Boolean(execution?.record);
    executionControls.hidden = !visible;
    const unavailableReason = preflightReady ? "" : preflightFailureSummary();
    executionUnavailable.hidden = visible || !unavailableReason;
    executionUnavailable.textContent = unavailableReason;

    const reviewedState = `${execution?.planDigest}:${execution?.record?.revision}`;
    if (executionControls.dataset.planDigest !== reviewedState) {
      runConfirmationPending = false;
      executionControls.dataset.planDigest = reviewedState;
    }
    executionControls.dataset.buttonLabel = review.ExecutionButtonLabel || "Run release";
    executionControls.dataset.title = execution?.canResume ? "Resume release" : "Run release";
    executionWarning.textContent = execution?.saved
      ? execution.canResume
        ? "Viewing and refreshing external status does not execute action steps. Resume continues the saved release and may perform external actions."
        : "This saved release is read-only. External status checks do not resume it."
      : "This starts the configured external release workflow.";
    runConfirmationCopy.textContent = execution?.canResume
      ? "Confirm that you want to continue this saved release from its checkpoint."
      : "Confirm this release before starting it.";
    updateExecutionButton();
  }

  function preflightFailureSummary() {
    const failures = (preflight?.checks || []).filter((check) => check.status !== "passed");
    if (!failures.length) return "External execution is unavailable.";
    return failures.map((check) => `${check.name}: ${check.details}`).join("; ");
  }

  function handleExecutionAction() {
    if (executionButton.disabled) return;
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
    const steps = (nextPlan.steps || []).map((step) => ({ ...step, status: step.status || "waiting" }));
    return { active: false, steps };
  }

  function createStepCard(step) {
    const item = document.createElement("article");
    item.className = "step-card";
    item.dataset.stepName = step.name;
    item.dataset.status = step.status || "waiting";
    item.setAttribute("role", "listitem");
    const head = document.createElement("div");
    head.className = "step-card-head";
    const title = document.createElement("h3");
    title.className = "step-title";
    title.textContent = step.name;
    const status = document.createElement("span");
    status.className = "status-badge";
    status.textContent = statusText(step.status || "waiting");
    head.append(title, status);
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
    liveProgress.append(progressSummary, progressDetail, progressTrack);
    const runLink = document.createElement("a");
    runLink.className = "step-run-link";
    runLink.textContent = "Open external run";
    runLink.target = "_blank";
    runLink.rel = "noopener noreferrer";
    runLink.hidden = true;
    const observationNote = document.createElement("p");
    observationNote.className = "step-observation-note";
    observationNote.hidden = true;
    item.append(head, liveProgress, runLink, observationNote);
    return item;
  }

  function renderStepGraph(steps) {
    const levels = computeStepLevels(steps);
    const maxLevel = Math.max(0, ...levels.values());
    const columns = Array.from({ length: maxLevel + 1 }, () => []);
    for (const step of steps) columns[levels.get(step.name) || 0].push(step);

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
    const byName = new Map(steps.map((step) => [step.name, step]));
    const levels = new Map();
    const visiting = new Set();
    const visit = (step) => {
      if (levels.has(step.name)) return levels.get(step.name);
      if (visiting.has(step.name)) return 0;
      visiting.add(step.name);
      const dependencies = (step.dependsOn || []).map((name) => byName.get(name)).filter(Boolean);
      const level = dependencies.length ? Math.max(...dependencies.map(visit)) + 1 : 0;
      visiting.delete(step.name);
      levels.set(step.name, level);
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
      const target = findStepCard(step.name);
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

  function findStepCard(stepName) {
    return Array.from(stepList.querySelectorAll(".step-card")).find((card) => card.dataset.stepName === stepName);
  }

  function updateSteps(snapshot) {
    for (const step of snapshot.steps || []) {
      const card = findStepCard(step.name);
      if (!card) continue;
      card.dataset.status = step.status || "waiting";
      card.querySelector(".status-badge").textContent = statusText(step.status || "waiting");
      card.querySelectorAll(".step-error").forEach((error) => error.remove());
      for (const message of [step.error, step.observationError && `Unable to refresh external status: ${step.observationError}`].filter(Boolean)) {
        const error = document.createElement("p");
        error.className = "step-error";
        error.textContent = message;
        card.append(error);
      }
      const link = card.querySelector(".step-run-link");
      const runURL = step.progress?.runUrl;
      link.hidden = !runURL;
      if (runURL) link.href = runURL;
      const observationNote = card.querySelector(".step-observation-note");
      observationNote.hidden = !step.observedAt;
      observationNote.textContent = step.observedAt
        ? `Last checked ${new Date(step.observedAt).toLocaleTimeString()}`
        : "";
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
      : steps.some((step) => step.observationError) ? "External status refresh failed; showing last known progress"
      : steps.some((step) => step.observedAt) ? "Saved graph with refreshed external-step status"
      : plan?.execution?.saved ? "Last saved graph progress, not live status"
      : snapshot.error ? "Workflow stopped; review the error"
      : complete === steps.length && steps.length
        ? succeeded === steps.length ? "Workflow completed successfully" : "Workflow stopped"
        : "Ready";
    const counts = new Map();
    for (const step of steps) counts.set(step.status || "waiting", (counts.get(step.status || "waiting") || 0) + 1);
    statusCounts.replaceChildren(...Array.from(counts.entries()).map(([status, count]) => {
      const label = document.createElement("span");
      label.textContent = `${statusText(status)} ${count}`;
      return label;
    }));
  }

  function statusText(status) {
    return status === "running" ? "in progress" : status;
  }

  function renderLinks(execution) {
    runLink.hidden = !execution?.runUrl;
    if (execution?.runUrl) {
      runLink.href = execution.runUrl;
    } else {
      runLink.removeAttribute("href");
    }
    const record = execution?.record;
    recordLink.hidden = !record?.url;
    if (record?.url) {
      recordLink.href = record.url;
      recordLink.textContent = `Open tracking record ${record.id} ↗`;
    } else {
      recordLink.removeAttribute("href");
    }

    executionLinks.hidden = runLink.hidden && recordLink.hidden;
  }

  function updateActionButtons() {
    planButton.disabled = !sessionReady || !preflight?.planningEnabled || executionActive || Boolean(submitting);
    planButton.title = executionActive
      ? "This release is running. Follow its progress below."
      : preflight && !preflight.planningEnabled ? preflightFailureSummary() : "";
    planButton.querySelector("span:first-child").textContent = submitting === "prepare"
      ? "Resolving and validating…"
      : selectedVariant().submitLabel || workflow.submitLabel || "Prepare release";
    processVariant.disabled = Boolean(submitting);
    updateExecutionButton();
  }

  function updateExecutionButton() {
    const execution = plan?.execution;
    const preflightReady = Boolean(preflight?.externalExecutionEnabled);
    const enabled = sessionReady && (preflightReady || execution?.canResume) && Boolean(plan) && !submitting;
    const complete = Boolean(execution?.run?.complete);
    const readOnly = execution?.saved && !execution.canResume;
    runConfirmation.hidden = !runConfirmationPending || complete || executionActive || readOnly;
    executionCancel.hidden = runConfirmation.hidden;
    executionTitle.textContent = runConfirmationPending ? "Confirm release" : executionControls.dataset.title || "Run release";
    executionButton.disabled = !enabled || complete || executionActive || readOnly;
    const completionLabel = {
      succeeded: "Release completed",
      failed: "Release failed",
      canceled: "Release canceled",
      uncertain: "Outcome needs attention",
    }[execution?.run?.result] || "Outcome needs attention";
    executionButton.textContent = complete
      ? completionLabel
      : readOnly ? "Read-only saved run"
      : submitting === "start"
        ? execution?.canResume ? "Resuming release…" : "Starting release…"
        : executionActive
        ? "Monitoring release…"
        : runConfirmationPending
          ? execution?.canResume ? "Confirm resume" : "Confirm run"
          : execution?.canResume ? "Resume release" : executionControls.dataset.buttonLabel || "Run release";
  }

  function connectEvents(session) {
    if (selectedSession !== session || !plan) return;
    if (eventSource) eventSource.close();
    const source = new EventSource(`${session.endpoint}/events`);
    eventSource = source;
    source.addEventListener("state", (event) => {
      if (selectedSession !== session || eventSource !== source) return;
      const snapshot = JSON.parse(event.data);
      executionActive = snapshot.active;
      if (snapshot.steps?.length) {
        updateSteps(snapshot);
        updateProgress(snapshot);
      }
      updateActionButtons();
      if (!snapshot.active && snapshot.error) showError(snapshot.error);
      const signature = (snapshot.steps || []).map((step) => `${step.name}:${step.status}`).join("|");
      if (signature !== lastRefreshSignature || !snapshot.active) {
        lastRefreshSignature = signature;
        refreshPlan(session).catch((error) => {
          if (selectedSession === session) showError(error.message);
        });
      }
    });
  }

  async function refreshPlan(session) {
    if (selectedSession !== session || refreshInFlight === session) return;
    refreshInFlight = session;
    try {
      const latest = await requestJSON(`${session.endpoint}/plan`, { signal: session.controller.signal });
      if (selectedSession !== session || !latest) return;
      if (!plan || latest.variantId !== plan.variantId) return;
      plan = latest;
      renderLinks(latest.execution);
      renderExecution(latest.execution, latest.plan || {});
      updateActionButtons();
    } finally {
      if (refreshInFlight === session) refreshInFlight = null;
    }
  }

  async function refreshState(session) {
    const snapshot = await requestJSON(`${session.endpoint}/state`, { signal: session.controller.signal });
    if (selectedSession !== session) return;
    executionActive = snapshot.active;
    if (snapshot.steps?.length) {
      updateSteps(snapshot);
      updateProgress(snapshot);
    }
    if (!snapshot.active && snapshot.error) showError(snapshot.error);
    updateActionButtons();
  }

  function renderPreflight() {
    processSafety.hidden = false;
    preflightList.replaceChildren(...(preflight.checks || []).map((check) => {
      const item = document.createElement("li");
      item.dataset.status = check.status;
      item.title = check.details;
      item.textContent = check.status !== "passed" && check.details
        ? `${check.name}: ${check.details}`
        : check.name;
      return item;
    }));
    if (preflight.externalExecutionEnabled) {
      safetyTitle.textContent = "External execution enabled";
      safetyCopy.textContent = "Preparing a plan does not start it. Every run requires explicit confirmation.";
    } else if (plan?.execution?.canResume) {
      safetyTitle.textContent = "Review readiness before resuming";
      safetyCopy.textContent = `New release preparation is blocked. Resuming this saved release remains available with confirmation. ${preflightFailureSummary()}`;
    } else {
      safetyTitle.textContent = "Planning is blocked";
      safetyCopy.textContent = preflightFailureSummary();
    }
  }

  async function requestJSON(path, options = {}) {
    const response = await fetch(path, {
      ...options,
      headers: { "Content-Type": "application/json", ...(options.headers || {}) },
    });
    if (response.status === 204) return null;
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || `${response.status} ${response.statusText}`);
    return data;
  }

  function showError(message) {
    toast.textContent = message;
    toast.hidden = false;
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => { toast.hidden = true; }, 8000);
  }
})();