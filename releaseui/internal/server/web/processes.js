"use strict";

const processList = document.querySelector("#process-list");
const processesError = document.querySelector("#processes-error");

loadProcesses();

async function loadProcesses() {
  try {
    const response = await fetch("/api/dashboard", { signal: AbortSignal.timeout(10000) });
    const data = await response.json();
    if (!response.ok) throw new Error(data.error || `${response.status} ${response.statusText}`);
    processList.replaceChildren(...data.processes.map(createProcessCard));
  } catch (error) {
    processList.replaceChildren();
    processesError.textContent = `Unable to load release processes: ${error.message}`;
    processesError.hidden = false;
  }
}

function createProcessCard(process) {
  const card = document.createElement("a");
  card.className = "process-card";
  card.href = process.href;

  const mark = document.createElement("span");
  mark.className = "process-mark";
  mark.textContent = process.mark;

  const title = document.createElement("h2");
  title.textContent = process.name;
  const description = document.createElement("p");
  description.textContent = process.description;
  const action = document.createElement("span");
  action.className = "process-action";
  action.textContent = "Open release process";

  card.append(mark, title, description, action);
  return card;
}
