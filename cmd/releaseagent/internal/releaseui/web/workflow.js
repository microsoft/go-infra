"use strict";

(() => {
  const workflowSection = document.querySelector("#workflow-section");
  const workflowHeading = document.querySelector("#workflow-heading");
  const workflowDescription = document.querySelector("#workflow-description");
  const processSafety = document.querySelector("#process-safety");
  const safetyTitle = document.querySelector("#safety-title");
  const safetyCopy = document.querySelector("#safety-copy");
  const preflightList = document.querySelector("#preflight-list");
  const form = document.querySelector("#plan-form");
  const processInputs = document.querySelector("#process-inputs");
  const planButton = document.querySelector("#plan-button");
  const demoButton = document.querySelector("#demo-button");
  const emptyState = document.querySelector("#empty-state");
  const planContent = document.querySelector("#plan-content");
  const planSubtitle = document.querySelector("#plan-subtitle");
  const intentBanner = document.querySelector("#intent-banner");
  const intentTitle = document.querySelector("#intent-title");
  const intentBadge = document.querySelector("#intent-badge");
  const planFacts = document.querySelector("#plan-facts");
  const requestPreview = document.querySelector("#request-preview");
  const requestPreviewEyebrow = document.querySelector("#request-preview-eyebrow");
  const requestPreviewTitle = document.querySelector("#request-preview-title");
  const requestPreviewTarget = document.querySelector("#request-preview-target");
  const requestPreviewFields = document.querySelector("#request-preview-fields");
  const progressSummary = document.querySelector(".progress-summary");
  const progressLabel = document.querySelector("#progress-label");
  const progressCount = document.querySelector("#progress-count");
  const progressBar = document.querySelector("#progress-bar");
  const statusCounts = document.querySelector("#status-counts");
  const stepGraph = document.querySelector("#step-graph");
  const stepEdges = document.querySelector("#step-edges");
  const stepList = document.querySelector("#step-list");
  const pipelineRunLink = document.querySelector("#pipeline-run-link");
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
  let endpointBase = "";
  let plan = null;
  let preflight = null;
  let eventSource = null;
  let executionActive = false;
  let runConfirmationPending = false;
  let lastRefreshSignature = "";
  let refreshInFlight = false;
  let toastTimer = null;

  initialize().catch((error) => showError(error.message));

  async function initialize() {
    processDefinition = await globalThis.releaseProcessReady;
    workflow = processDefinition.workflow;
    if (!workflow) return;

    endpointBase = `/api/processes/${encodeURIComponent(processDefinition.id)}`;
    workflowSection.hidden = false;
    workflowHeading.textContent = workflow.heading;
    workflowDescription.textContent = workflow.description || "";
    workflowDescription.hidden = !workflow.description;
    planButton.querySelector("span:first-child").textContent = workflow.submitLabel || "Prepare release";
    form.hidden = !workflow.canPrepare;
    demoButton.hidden = !workflow.canSimulate;
    if (workflow.hasPreflight) planButton.disabled = true;

    processInputs.replaceChildren(...(workflow.inputs || []).map(createInput));
    updateInputState();
    form.addEventListener("submit", prepareRelease);
    form.addEventListener("change", updateInputState);
    demoButton.addEventListener("click", startSimulation);
    executionButton.addEventListener("click", handleExecutionAction);
    executionCancel.addEventListener("click", cancelRunConfirmation);
    if ("ResizeObserver" in globalThis) {
      new ResizeObserver(() => renderGraphEdges()).observe(stepList);
    } else {
      globalThis.addEventListener("resize", renderGraphEdges);
    }

    await loadPreflight();
    await loadExistingPlan();
  }

  function createInput(input, inputIndex) {
    const wrapper = document.createElement("div");
    wrapper.dataset.inputId = input.id;
    const record = { schema: input, wrapper, controls: [], notice: null };

    if (input.type === "choice") {
      wrapper.className = "process-input-group";
      const label = document.createElement("p");
      label.className = "input-group-label";
      label.textContent = input.label;
      const list = document.createElement("div");
      list.className = "mode-list";
      list.setAttribute("role", "radiogroup");
      list.setAttribute("aria-label", input.label);
      for (const [optionIndex, option] of (input.options || []).entries()) {
        const card = document.createElement("label");
        card.className = "mode-card";
        card.dataset.value = option.value;
        const control = document.createElement("input");
        control.type = "radio";
        control.name = input.id;
        control.value = option.value;
        control.checked = option.value === input.default || !input.default && optionIndex === 0;
        const icon = document.createElement("span");
        icon.className = `mode-icon option-icon-${(optionIndex + inputIndex) % 3}`;
        icon.setAttribute("aria-hidden", "true");
        icon.textContent = option.mark || option.name.slice(0, 1).toUpperCase();
        const copy = document.createElement("span");
        const name = document.createElement("strong");
        name.textContent = option.name;
        const description = document.createElement("small");
        description.textContent = option.description;
        copy.append(name, description);
        card.append(control, icon, copy);
        list.append(card);
        record.controls.push(control);
      }
      wrapper.append(label, list);
      if (input.description) {
        const description = document.createElement("p");
        description.className = "field-help";
        description.textContent = input.description;
        wrapper.append(description);
      }
      record.notice = createInputNotice();
      wrapper.append(record.notice.container);
    } else {
      wrapper.className = "mode-inputs";
      const id = `process-input-${input.id}`;
      const label = document.createElement("label");
      label.htmlFor = id;
      label.textContent = input.label;
      const control = document.createElement("input");
      control.id = id;
      control.name = input.id;
      control.type = input.type;
      control.placeholder = input.placeholder || "";
      control.value = input.default || "";
      control.autocomplete = "off";
      if (input.type === "number") {
        control.inputMode = "numeric";
        control.min = "1";
        control.step = "1";
      }
      wrapper.append(label, control);
      record.controls.push(control);
      if (input.description) {
        const description = document.createElement("p");
        description.className = "field-help";
        description.textContent = input.description;
        wrapper.append(description);
      }
    }

    inputRecords.set(input.id, record);
    return wrapper;
  }

  function createInputNotice() {
    const container = document.createElement("div");
    container.className = "lock-summary";
    container.hidden = true;
    const icon = document.createElement("span");
    icon.setAttribute("aria-hidden", "true");
    icon.textContent = "🔒";
    const copy = document.createElement("p");
    const title = document.createElement("strong");
    const detail = document.createElement("span");
    copy.append(title, " ", detail);
    container.append(icon, copy);
    return { container, title, detail };
  }

  function updateInputState() {
    for (const record of inputRecords.values()) {
      const condition = record.schema.visibleWhen;
      const visible = !condition || inputValue(condition.inputId) === condition.equals;
      record.wrapper.hidden = !visible;
      for (const control of record.controls) control.required = visible && Boolean(record.schema.required);
    }
    for (const record of inputRecords.values()) {
      if (record.schema.type !== "choice") continue;
      const value = inputValue(record.schema.id);
      for (const control of record.controls) {
        control.closest(".mode-card").classList.toggle("selected", control.checked);
      }
      const option = (record.schema.options || []).find((candidate) => candidate.value === value);
      record.notice.container.hidden = !option?.noticeTitle && !option?.notice;
      record.notice.title.textContent = option?.noticeTitle || "";
      record.notice.detail.textContent = option?.notice || "";
    }
  }

  function inputValue(id) {
    const record = inputRecords.get(id);
    if (!record) return "";
    if (record.schema.type === "choice") {
      return record.controls.find((control) => control.checked)?.value || "";
    }
    return record.controls[0]?.value.trim() || "";
  }

  function serializeInputs() {
    const result = {};
    for (const [id, record] of inputRecords) {
      if (record.wrapper.hidden) continue;
      const value = inputValue(id);
      if (value !== "" || record.schema.required) result[id] = value;
    }
    return result;
  }

  function restoreInputs(values) {
    if (!values) return;
    for (const [id, record] of inputRecords) {
      if (!Object.prototype.hasOwnProperty.call(values, id)) continue;
      const value = String(values[id] ?? "");
      if (record.schema.type === "choice") {
        for (const control of record.controls) control.checked = control.value === value;
      } else {
        record.controls[0].value = value;
      }
    }
    updateInputState();
  }

  async function prepareRelease(event) {
    event.preventDefault();
    setBusy(planButton, true, "Resolving and validating…");
    try {
      plan = await requestJSON(`${endpointBase}/plan`, {
        method: "POST",
        body: JSON.stringify(serializeInputs()),
      });
      renderPlan(plan);
      connectEvents();
    } catch (error) {
      showError(error.message);
    } finally {
      setBusy(planButton, false, workflow.submitLabel || "Prepare release");
      if (workflow.hasPreflight && !preflight?.planningEnabled) planButton.disabled = true;
    }
  }

  async function startSimulation() {
    setBusy(demoButton, true, "Starting simulation…");
    try {
      await requestJSON(`${endpointBase}/simulate`, { method: "POST", body: "{}" });
      connectEvents();
    } catch (error) {
      showError(error.message);
      updateActionButtons();
    }
  }

  async function startRelease() {
    setBusy(executionButton, true, plan?.execution?.run?.buildId ? "Resuming monitoring…" : "Starting release…");
    try {
      await requestJSON(`${endpointBase}/start`, {
        method: "POST",
        body: JSON.stringify({ planDigest: plan.execution.planDigest, confirmed: true }),
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
    restoreInputs(nextPlan.input);
    emptyState.hidden = true;
    planContent.hidden = false;

    const view = nextPlan.view || {};
    planSubtitle.textContent = view.subtitle || `${(nextPlan.steps || []).length} workflow steps`;
    intentBanner.hidden = !view.intentTitle;
    intentTitle.textContent = view.intentTitle || "";
    intentBadge.textContent = view.intentBadge || "";
    intentBadge.hidden = !view.intentBadge;
    planFacts.replaceChildren(...(view.facts || []).map(createPlanFact));
    renderRequest(view.request);

    const steps = nextPlan.steps || [];
    progressSummary.hidden = steps.length === 0;
    stepGraph.hidden = steps.length === 0;
    if (steps.length) renderStepGraph(steps);
    const snapshot = initialSnapshot(nextPlan);
    executionActive = snapshot.active;
    updateSteps(snapshot);
    updateProgress(snapshot);
    renderRun(nextPlan.execution?.run);
    renderExecution(nextPlan.execution, view);
    updateActionButtons();
  }

  function createPlanFact(fact) {
    const card = document.createElement("article");
    card.className = "source-card";
    const label = document.createElement("span");
    label.textContent = fact.label;
    const value = document.createElement("strong");
    value.textContent = fact.value;
    card.append(label, value);
    if (fact.detail) {
      const detail = document.createElement("code");
      detail.textContent = fact.detail;
      card.append(detail);
    }
    if (fact.href) {
      const link = document.createElement("a");
      link.href = fact.href;
      link.target = "_blank";
      link.rel = "noreferrer";
      link.textContent = "Open details ↗";
      card.append(link);
    }
    return card;
  }

  function renderRequest(request) {
    requestPreview.hidden = !request;
    if (!request) return;
    requestPreviewEyebrow.textContent = request.eyebrow || "Request preview";
    requestPreviewTitle.textContent = request.title || "External request";
    requestPreviewTarget.textContent = request.target || "";
    requestPreviewTarget.hidden = !request.target;
    requestPreviewFields.replaceChildren(...(request.fields || []).flatMap((field) => {
      const term = document.createElement("dt");
      term.textContent = field.name;
      const description = document.createElement("dd");
      description.textContent = field.value;
      return [term, description];
    }));
  }

  function renderExecution(execution, view) {
    const preflightReady = !workflow.hasPreflight || Boolean(preflight?.externalExecutionEnabled);
    const visible = Boolean(workflow.canStart && execution?.enabled && execution?.eligible && preflightReady);
    executionControls.hidden = !visible;
    const unavailableReason = execution?.unavailableReason ||
      (workflow.canStart && execution?.enabled && !preflightReady
        ? "External execution is not ready. Review the preflight checks."
        : "");
    executionUnavailable.hidden = visible || !unavailableReason;
    executionUnavailable.textContent = unavailableReason;
    if (!workflow.canStart) return;

    if (executionControls.dataset.planDigest !== execution?.planDigest) {
      runConfirmationPending = false;
      executionControls.dataset.planDigest = execution?.planDigest || "";
    }
    executionControls.dataset.buttonLabel = view.executionButtonLabel || "Run release";
    executionControls.dataset.title = view.executionTitle || "Run release";
    executionWarning.textContent = view.executionWarning || "This starts the configured external release workflow.";
    runConfirmationCopy.textContent = view.executionConfirmation || "Confirm this release before starting it.";
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
    const steps = (nextPlan.steps || []).map((step) => ({ ...step, status: step.status || "waiting" }));
    const monitoring = Boolean(nextPlan.execution?.run?.buildId && !nextPlan.execution.run.complete);
    return { active: monitoring, steps };
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
    item.append(head, liveProgress);
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

  function renderRun(run) {
    if (!run?.buildId) {
      pipelineRunLink.hidden = true;
      pipelineRunLink.removeAttribute("href");
      return;
    }
    pipelineRunLink.href = run.url;
    pipelineRunLink.textContent = `${run.linkLabel || `Open external run ${run.buildId}`} ↗ · ${run.complete ? "Completed" : "In progress"}`;
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
    const preflightReady = !workflow?.hasPreflight || Boolean(preflight?.externalExecutionEnabled);
    const enabled = Boolean(workflow?.canStart && execution?.enabled && execution?.eligible && preflightReady);
    const complete = Boolean(execution?.run?.complete);
    runConfirmation.hidden = !runConfirmationPending || complete || executionActive;
    executionCancel.hidden = runConfirmation.hidden;
    executionTitle.textContent = runConfirmationPending ? "Confirm release" : executionControls.dataset.title || "Run release";
    executionButton.disabled = !enabled || complete || executionActive;
    executionButton.textContent = complete
      ? "Release completed"
      : executionActive
        ? "Monitoring release…"
        : execution?.run?.buildId
          ? "Resume release monitoring"
          : runConfirmationPending ? "Confirm run" : executionControls.dataset.buttonLabel || "Run release";
  }

  function connectEvents() {
    if (!plan) return;
    if (eventSource) eventSource.close();
    eventSource = new EventSource(`${endpointBase}/events`);
    eventSource.addEventListener("state", (event) => {
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
        refreshPlan().catch((error) => showError(error.message));
      }
    });
  }

  async function refreshPlan() {
    if (refreshInFlight) return;
    refreshInFlight = true;
    try {
      const response = await fetch(`${endpointBase}/plan`);
      if (response.status === 204) return;
      const latest = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(latest.error || `${response.status} ${response.statusText}`);
      if (!plan || latest.sessionId !== plan.sessionId) return;
      plan = latest;
      renderRun(latest.execution?.run);
      renderExecution(latest.execution, latest.view || {});
      updateActionButtons();
    } finally {
      refreshInFlight = false;
    }
  }

  async function loadExistingPlan() {
    if (!workflow.canPrepare) return;
    try {
      const response = await fetch(`${endpointBase}/plan`);
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
    if (!workflow.hasPreflight) return;
    processSafety.hidden = false;
    try {
      preflight = await requestJSON(`${endpointBase}/preflight`);
      planButton.disabled = !preflight.planningEnabled;
      planButton.title = preflight.planningEnabled ? "" : "Planning is unavailable until the readiness checks pass";
      preflightList.replaceChildren(...(preflight.checks || []).map((check) => {
        const item = document.createElement("li");
        item.dataset.status = check.status;
        item.title = check.details;
        item.textContent = check.name;
        return item;
      }));
      if (preflight.externalExecutionEnabled) {
        safetyTitle.textContent = "External execution enabled";
        safetyCopy.textContent = "Preparing a plan does not start it. Every run requires explicit confirmation.";
      } else {
        safetyTitle.textContent = "Planning readiness";
        safetyCopy.textContent = "Review the local checks before preparing or running this release.";
      }
    } catch (error) {
      showError(`Unable to check local readiness: ${error.message}`);
    }
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
})();