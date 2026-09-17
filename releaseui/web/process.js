"use strict";

(() => {
  const processMark = document.querySelector("#process-mark");
  const processName = document.querySelector("#process-name");
  const processDescription = document.querySelector("#process-description");
  const processDocumentation = document.querySelector("#process-documentation");
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
      return process;
    } catch (error) {
      showError(error.message);
      throw error;
    }
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