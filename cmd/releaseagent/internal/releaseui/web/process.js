"use strict";

(() => {
  const processMark = document.querySelector("#process-mark");
  const processName = document.querySelector("#process-name");
  const processDescription = document.querySelector("#process-description");
  const processDocumentation = document.querySelector("#process-documentation");
  const methodsSection = document.querySelector("#methods-section");
  const processMethods = document.querySelector("#process-methods");
  const toast = document.querySelector("#toast");

  globalThis.releaseProcessReady = loadProcess();

  async function loadProcess() {
    try {
      const processID = location.pathname.split("/").filter(Boolean).at(-1);
      const process = await requestJSON(`/api/processes/${encodeURIComponent(processID)}`);
      document.title = `${process.name} Release · Microsoft Build of Go Release UI`;
      processMark.textContent = process.mark;
      processName.textContent = `Release ${process.name}.`;
      processDescription.textContent = process.description;
      processDocumentation.href = process.documentationUrl || "";
      processDocumentation.hidden = !process.documentationUrl;
      const methods = process.methods || [];
      processMethods.replaceChildren(...methods.map(createMethod));
      methodsSection.hidden = methods.length === 0;
      return process;
    } catch (error) {
      showError(error.message);
      throw error;
    }
  }

  function createMethod(method) {
    const card = document.createElement("article");
    card.className = "process-method";

    const heading = document.createElement("div");
    heading.className = "process-method-heading";
    const title = document.createElement("h3");
    title.textContent = method.name;
    heading.append(title);
    if (method.badge) {
      const badge = document.createElement("span");
      badge.textContent = method.badge;
      heading.append(badge);
    }

    const description = document.createElement("p");
    description.textContent = method.description;
    const steps = document.createElement("ol");
    steps.replaceChildren(...method.steps.map((text) => {
      const item = document.createElement("li");
      item.textContent = text;
      return item;
    }));
    const action = document.createElement("a");
    action.className = "button button-primary process-method-action";
    action.href = method.actionHref;
    action.target = "_blank";
    action.rel = "noreferrer";
    action.textContent = `${method.actionLabel} ↗`;
    card.append(heading, description, steps, action);
    return card;
  }

  async function requestJSON(path) {
    const response = await fetch(path);
    const data = await response.json().catch(() => ({}));
    if (!response.ok) throw new Error(data.error || `${response.status} ${response.statusText}`);
    return data;
  }

  function showError(message) {
    toast.textContent = message;
    toast.hidden = false;
  }
})();