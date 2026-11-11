"use strict";

function releaseStatusText(status) {
  if (status === "running") return "in progress";
  if (status === "saved") return "Interrupted";
  if (status === "stale") return "Probably interrupted";
  return status;
}

function publishedReleaseStatus(status, updatedAt) {
  if (status === "running" && Date.now() - new Date(updatedAt).getTime() >= 60000) status = "stale";
  return { status, label: releaseStatusText(status) };
}
