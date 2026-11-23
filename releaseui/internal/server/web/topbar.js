"use strict";

(() => {
  const heartbeatInterval = 5000;
  const heartbeatTimeout = 4000;
  const badge = document.getElementById("server-connection");
  if (!badge) return;
  const dot = badge.querySelector(".connection-dot");
  const label = badge.querySelector(".connection-label");

  function show(connected) {
    dot.classList.toggle("disconnected", !connected);
    label.textContent = connected ? "Server connected" : "Server unreachable";
    badge.title = label.textContent;
  }

  async function heartbeat() {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), heartbeatTimeout);
    try {
      const response = await fetch("/api/health", { cache: "no-store", signal: controller.signal });
      show(response.ok);
    } catch {
      show(false);
    } finally {
      clearTimeout(timer);
      setTimeout(heartbeat, heartbeatInterval);
    }
  }

  heartbeat();
})();
