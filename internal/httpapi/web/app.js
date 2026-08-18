"use strict";

const state = {
  conversations: [],
  activeID: null,
  messages: [],
  documents: [],
  memories: [],
  officeDrafts: [],
  officeOperations: [],
  runSummaries: [],
  officeExecution: false,
  memoryAutoCapture: false,
  memoryRecall: false,
  multiAgent: false,
  humanApproval: false,
  editingMemoryID: null,
  busy: false,
  controller: null,
  draft: null,
};

const elements = {
  sidebar: document.querySelector("#sidebar"),
  sidebarScrim: document.querySelector("#sidebarScrim"),
  openSidebar: document.querySelector("#openSidebar"),
  closeSidebar: document.querySelector("#closeSidebar"),
  newConversation: document.querySelector("#newConversation"),
  conversationList: document.querySelector("#conversationList"),
  conversationTitle: document.querySelector("#conversationTitle"),
  renameConversation: document.querySelector("#renameConversation"),
  messageList: document.querySelector("#messageList"),
  welcome: document.querySelector("#welcome"),
  chatScroll: document.querySelector("#chatScroll"),
  composer: document.querySelector("#composer"),
  messageInput: document.querySelector("#messageInput"),
  sendButton: document.querySelector("#sendButton"),
  sendIcon: document.querySelector("#sendIcon"),
  runtimeModel: document.querySelector("#runtimeModel"),
  runtimeProvider: document.querySelector("#runtimeProvider"),
  statusDot: document.querySelector("#statusDot"),
  toolBadge: document.querySelector("#toolBadge"),
  openKnowledge: document.querySelector("#openKnowledge"),
  closeKnowledge: document.querySelector("#closeKnowledge"),
  knowledgeDialog: document.querySelector("#knowledgeDialog"),
  knowledgeUpload: document.querySelector("#knowledgeUpload"),
  knowledgeFile: document.querySelector("#knowledgeFile"),
  knowledgeVisibility: document.querySelector("#knowledgeVisibility"),
  selectedFile: document.querySelector("#selectedFile"),
  uploadKnowledge: document.querySelector("#uploadKnowledge"),
  knowledgeDocuments: document.querySelector("#knowledgeDocuments"),
  knowledgeCount: document.querySelector("#knowledgeCount"),
  embeddingModel: document.querySelector("#embeddingModel"),
  openMemory: document.querySelector("#openMemory"),
  closeMemory: document.querySelector("#closeMemory"),
  memoryDialog: document.querySelector("#memoryDialog"),
  memoryForm: document.querySelector("#memoryForm"),
  memoryKind: document.querySelector("#memoryKind"),
  memoryContent: document.querySelector("#memoryContent"),
  memoryImportance: document.querySelector("#memoryImportance"),
  memoryExpiresAt: document.querySelector("#memoryExpiresAt"),
  saveMemory: document.querySelector("#saveMemory"),
  cancelMemoryEdit: document.querySelector("#cancelMemoryEdit"),
  memoryList: document.querySelector("#memoryList"),
  memoryCount: document.querySelector("#memoryCount"),
  memoryDescription: document.querySelector("#memoryDescription"),
  openOfficeDrafts: document.querySelector("#openOfficeDrafts"),
  closeOfficeDrafts: document.querySelector("#closeOfficeDrafts"),
  officeDraftDialog: document.querySelector("#officeDraftDialog"),
  officeDraftList: document.querySelector("#officeDraftList"),
  officeDraftCount: document.querySelector("#officeDraftCount"),
  openRunMetrics: document.querySelector("#openRunMetrics"),
  closeRunMetrics: document.querySelector("#closeRunMetrics"),
  runMetricsDialog: document.querySelector("#runMetricsDialog"),
  runMetricsList: document.querySelector("#runMetricsList"),
  toast: document.querySelector("#toast"),
};

async function api(path, options = {}) {
  const headers = { ...(options.headers || {}) };
  // multipart/form-data 的 boundary 必须由浏览器生成，不能手动覆盖 Content-Type。
  if (!(options.body instanceof FormData) && !headers["Content-Type"]) {
    headers["Content-Type"] = "application/json";
  }
  const response = await fetch(path, {
    ...options,
    headers,
  });
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    throw new Error(body.error || `请求失败 (${response.status})`);
  }
  if (response.status === 204) return null;
  return response.json();
}

async function initialize() {
  bindEvents();
  resizeInput();
  try {
    const [info, result, knowledgeResult, memoryResult, officeDraftResult, officeOperationResult] = await Promise.all([
      api("/api/info"), api("/api/conversations"), api("/api/knowledge/documents"),
      api("/api/memories?include_expired=true"),
      api("/api/office/drafts?limit=200"),
      api("/api/office/operations?limit=200"),
    ]);
    elements.runtimeModel.textContent = info.model;
    state.multiAgent = Boolean(info.multi_agent);
    state.humanApproval = Boolean(info.human_approval);
    elements.runtimeProvider.textContent = `${info.provider} · ${info.version}${state.multiAgent ? " · 多 Agent" : ""}`;
    elements.toolBadge.innerHTML = `<i></i> ${Number(info.tool_count || 6)} 个受控工具${info.mcp_enabled ? " · MCP" : ""}`;
    state.conversations = result.conversations || [];
    state.documents = knowledgeResult.documents || [];
    state.memories = memoryResult.memories || [];
    state.officeDrafts = officeDraftResult.drafts || [];
    state.officeOperations = officeOperationResult.operations || [];
    state.officeExecution = Boolean(info.office_execution);
    state.memoryAutoCapture = Boolean(info.memory_auto_capture);
    state.memoryRecall = Boolean(info.memory_recall);
    elements.memoryDescription.textContent = memoryStatusText();
    elements.embeddingModel.textContent = `Embedding: ${info.embedding_model}`;
    renderConversations();
    renderKnowledgeDocuments();
    renderMemories();
    renderOfficeDrafts();
    if (state.conversations.length) {
      await selectConversation(state.conversations[0].id);
    }
  } catch (error) {
    elements.statusDot.classList.add("offline");
    elements.runtimeModel.textContent = "服务不可用";
    elements.runtimeProvider.textContent = "请检查后端日志";
    notify(error.message);
  }
  elements.messageInput.focus();
}

function bindEvents() {
  elements.newConversation.addEventListener("click", () => createConversation());
  elements.openKnowledge.addEventListener("click", openKnowledge);
  elements.closeKnowledge.addEventListener("click", () => elements.knowledgeDialog.close());
  elements.knowledgeUpload.addEventListener("submit", uploadKnowledgeDocument);
  elements.knowledgeFile.addEventListener("change", () => {
    elements.selectedFile.textContent = elements.knowledgeFile.files[0]?.name || "尚未选择";
  });
  elements.openMemory.addEventListener("click", openMemory);
  elements.closeMemory.addEventListener("click", () => elements.memoryDialog.close());
  elements.memoryForm.addEventListener("submit", saveMemory);
  elements.cancelMemoryEdit.addEventListener("click", resetMemoryForm);
  elements.openOfficeDrafts.addEventListener("click", openOfficeDrafts);
  elements.closeOfficeDrafts.addEventListener("click", () => elements.officeDraftDialog.close());
  elements.openRunMetrics.addEventListener("click", openRunMetrics);
  elements.closeRunMetrics.addEventListener("click", () => elements.runMetricsDialog.close());
  elements.renameConversation.addEventListener("click", renameActiveConversation);
  elements.composer.addEventListener("submit", event => {
    event.preventDefault();
    if (state.busy) {
      state.controller?.abort();
      return;
    }
    sendMessage(elements.messageInput.value);
  });
  elements.messageInput.addEventListener("input", resizeInput);
  elements.messageInput.addEventListener("keydown", event => {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      elements.composer.requestSubmit();
    }
  });
  document.addEventListener("keydown", event => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      createConversation();
    }
  });
  document.querySelectorAll(".suggestion").forEach(button => {
    button.addEventListener("click", () => sendMessage(button.dataset.prompt));
  });
  elements.openSidebar.addEventListener("click", openSidebar);
  elements.closeSidebar.addEventListener("click", closeSidebar);
  elements.sidebarScrim.addEventListener("click", closeSidebar);
}

async function openKnowledge() {
  try {
    await refreshKnowledgeDocuments();
    elements.knowledgeDialog.showModal();
    closeSidebar();
  } catch (error) {
    notify(error.message);
  }
}

async function openMemory() {
  try {
    await refreshMemories();
    elements.memoryDialog.showModal();
    closeSidebar();
  } catch (error) {
    notify(error.message);
  }
}

async function openOfficeDrafts() {
  try {
    await refreshOfficeDrafts();
    elements.officeDraftDialog.showModal();
    closeSidebar();
  } catch (error) {
    notify(error.message);
  }
}

async function openRunMetrics() {
  try {
    await refreshRunMetrics();
    elements.runMetricsDialog.showModal();
    closeSidebar();
  } catch (error) {
    notify(error.message);
  }
}

async function refreshRunMetrics() {
  const result = await api("/api/runs?limit=50");
  state.runSummaries = result.runs || [];
  renderRunMetrics();
}

function renderRunMetrics() {
  elements.runMetricsList.replaceChildren();
  if (!state.runSummaries.length) {
    const empty = document.createElement("div");
    empty.className = "document-empty";
    empty.textContent = "还没有 Agent 运行记录。发送一条消息后即可查看指标。";
    elements.runMetricsList.append(empty);
    return;
  }
  for (const summary of state.runSummaries) {
    const run = summary.run || {};
    const metrics = summary.metrics || {};
    const card = document.createElement("article");
    card.className = "run-metrics-item";
    const heading = document.createElement("div");
    heading.className = "run-metrics-heading";
    const status = document.createElement("strong");
    status.className = `status-${run.status || "unknown"}`;
    status.textContent = runStatusText(run.status);
    const model = document.createElement("span");
    model.textContent = run.model || "未知模型";
    const started = document.createElement("span");
    started.textContent = formatDateTime(run.started_at);
    heading.append(status, model, started);

    const grid = document.createElement("div");
    grid.className = "run-metrics-grid";
    grid.append(
      runMetric("总耗时", formatMilliseconds(metrics.duration_ms)),
      runMetric("首字延迟", metrics.time_to_first_token_ms == null ? "—" : formatMilliseconds(metrics.time_to_first_token_ms)),
      runMetric("Token", tokenUsageText(metrics)),
      runMetric("调用", `${metrics.model_calls || 0} 模型 · ${metrics.tool_calls || 0} 工具 · ${metrics.agent_handoffs || 0} 交接`),
    );
    const meta = document.createElement("div");
    meta.className = "run-metrics-meta";
    const toolDuration = `工具累计 ${formatMilliseconds(metrics.tool_duration_ms)}，最慢 ${formatMilliseconds(metrics.maximum_tool_duration_ms)}`;
    const handoffDuration = `交接累计 ${formatMilliseconds(metrics.agent_handoff_duration_ms)}，最慢 ${formatMilliseconds(metrics.maximum_agent_handoff_duration_ms)}`;
    meta.textContent = `${run.id || ""} · ${toolDuration} · ${handoffDuration}${run.error ? ` · 错误：${run.error}` : ""}`;
    card.append(heading, grid, meta);
    elements.runMetricsList.append(card);
  }
}

function runMetric(label, value) {
  const item = document.createElement("div");
  item.className = "run-metric";
  const name = document.createElement("span");
  name.textContent = label;
  const content = document.createElement("strong");
  content.textContent = value;
  item.append(name, content);
  return item;
}

function tokenUsageText(metrics) {
  if (metrics.usage_complete) return `${metrics.total_tokens || 0}（输入 ${metrics.prompt_tokens || 0} / 输出 ${metrics.completion_tokens || 0}）`;
  if (metrics.usage_reported_calls > 0) return `${metrics.total_tokens || 0}（部分调用上报）`;
  return "Provider 未上报";
}

function formatMilliseconds(value) {
  const milliseconds = Number(value || 0);
  if (milliseconds < 1000) return `${milliseconds} ms`;
  return `${(milliseconds / 1000).toFixed(milliseconds < 10000 ? 2 : 1)} s`;
}

function runStatusText(status) {
  return ({ running: "执行中", completed: "已完成", failed: "失败", cancelled: "已取消", rejected: "已拒绝" })[status] || status || "未知";
}

async function refreshOfficeDrafts() {
  const [draftResult, operationResult] = await Promise.all([
    api("/api/office/drafts?limit=200"),
    api("/api/office/operations?limit=200"),
  ]);
  state.officeDrafts = draftResult.drafts || [];
  state.officeOperations = operationResult.operations || [];
  renderOfficeDrafts();
}

function renderOfficeDrafts() {
  elements.officeDraftCount.textContent = `${state.officeDrafts.length} 份草稿`;
  elements.officeDraftList.replaceChildren();
  if (!state.officeDrafts.length) {
    const empty = document.createElement("div");
    empty.className = "document-empty";
    empty.textContent = "还没有办公草稿。可以在对话中让 Zora 起草邮件或拟定日程。";
    elements.officeDraftList.append(empty);
    return;
  }
  for (const item of state.officeDrafts) elements.officeDraftList.append(officeDraftNode(item));
}

function officeDraftNode(item) {
  const operation = state.officeOperations.find(value => value.draft_id === item.id);
  const card = document.createElement("article");
  card.className = "office-draft-item";
  const heading = document.createElement("div");
  heading.className = "office-draft-heading";
  const kind = document.createElement("strong");
  kind.textContent = item.kind === "email" ? "邮件" : "日程";
  const status = document.createElement("span");
  status.textContent = officeDraftStatusText(item.status);
  status.className = `status-${item.status}`;
  heading.append(kind, status);
  const title = document.createElement("h3");
  title.textContent = item.title;
  const detail = document.createElement("pre");
  const payload = item.payload || {};
  if (item.kind === "email") {
    detail.textContent = `收件人：${(payload.to || []).join("、") || "未填写"}\n抄送：${(payload.cc || []).join("、") || "无"}\n\n${payload.body || ""}`;
  } else {
    detail.textContent = `时间：${payload.start || "未填写"} — ${payload.end || "未填写"}\n地点：${payload.location || "未填写"}\n参与人：${(payload.attendees || []).join("、") || "无"}\n\n${payload.body || ""}`;
  }
  const meta = document.createElement("small");
  meta.textContent = `创建于 ${formatDateTime(item.created_at)} · 草稿 ID ${item.id}`;
  const actions = document.createElement("div");
  actions.className = "office-draft-actions";
  if (item.status === "draft") {
    actions.append(
      officeDraftAction("提交人工确认", "primary", () => submitOfficeDraft(item)),
      officeDraftAction("删除草稿", "danger", () => deleteOfficeDraft(item)),
    );
  } else if (item.status === "pending_confirmation") {
    actions.append(
      officeDraftAction("拒绝", "danger", () => decideOfficeDraft(item, "rejected")),
      officeDraftAction("批准", "primary", () => decideOfficeDraft(item, "approved")),
    );
  } else if (item.status === "approved" && !operation) {
    actions.append(officeDraftAction("准备执行任务", "primary", () => prepareOfficeOperation(item)));
  } else if (operation?.status === "pending" && state.officeExecution) {
    actions.append(officeDraftAction("执行已批准草稿", "primary", () => executeOfficeOperation(item, operation)));
  } else if (operation?.status === "failed" && state.officeExecution) {
    actions.append(officeDraftAction("使用原幂等键重试", "primary", () => executeOfficeOperation(item, operation)));
  }
  actions.append(officeDraftAction("查看记录", "", () => showOfficeDraftEvents(item, card)));
  if (operation) {
    actions.append(officeDraftAction("查看执行审计", "", () => showOfficeOperationEvents(operation, card)));
  }
  const safety = document.createElement("p");
  safety.className = "office-draft-safety";
  safety.textContent = operation?.status === "completed"
    ? `外部操作已完成${operation.external_reference ? `，远端引用：${operation.external_reference}` : ""}。`
    : operation?.status === "failed"
      ? `第 ${operation.attempt} 次执行失败：${operation.last_error || "未返回错误详情"}`
      : operation?.status === "executing"
        ? `第 ${operation.attempt} 次执行中，任务由数据库租约保护。`
        : operation?.status === "pending"
          ? state.officeExecution
            ? "执行任务已持久化，等待你再次确认后调用外部系统。"
            : "执行任务已持久化，但当前未配置真实外部写执行器，不能执行。"
    : item.status === "approved"
      ? "已记录批准决定，但尚未准备执行任务。"
    : item.status === "rejected"
      ? "已拒绝，不会产生外部操作。"
      : item.status === "pending_confirmation"
        ? "正在等待人工决定，内容已冻结。"
        : "仅保存在 Zora 内部，尚未提交确认。";
  card.append(heading, title, detail, meta, safety, actions);
  return card;
}

async function prepareOfficeOperation(item) {
  if (!confirm(`为草稿「${item.title}」创建持久化执行任务？\n\n此步骤不会调用外部系统，任务会绑定唯一幂等键。`)) return;
  try {
    const result = await api(`/api/office/drafts/${item.id}/operation`, { method: "POST" });
    await refreshOfficeDrafts();
    notify(result.execution_enabled
      ? "执行任务已准备，请核对后再执行"
      : "执行任务已准备；真实写执行器尚未配置");
  } catch (error) {
    notify(error.message);
  }
}

async function executeOfficeOperation(item, operation) {
  const action = operation.status === "failed" ? "重试" : "执行";
  if (!confirm(`${action}草稿「${item.title}」？\n\n确认后将调用真实外部系统。失败重试会继续使用原幂等键，避免重复业务操作。`)) return;
  try {
    const result = await api(`/api/office/operations/${operation.id}/execute`, { method: "POST" });
    await refreshOfficeDrafts();
    notify(result.message);
  } catch (error) {
    await refreshOfficeDrafts().catch(() => {});
    notify(error.message);
  }
}

async function showOfficeOperationEvents(operation, card) {
  try {
    const result = await api(`/api/office/operations/${operation.id}/events`);
    card.querySelector(".office-operation-events")?.remove();
    const list = document.createElement("ol");
    list.className = "office-draft-events office-operation-events";
    const events = result.events || [];
    for (const event of events) {
      const row = document.createElement("li");
      const from = event.from_status ? officeOperationStatusText(event.from_status) : "任务创建";
      row.textContent = `${formatDateTime(event.created_at)} · ${from} → ${officeOperationStatusText(event.to_status)} · 第 ${event.attempt} 次${event.reason ? ` · ${event.reason}` : ""}`;
      list.append(row);
    }
    if (!events.length) {
      const row = document.createElement("li");
      row.textContent = "尚无执行审计记录";
      list.append(row);
    }
    card.append(list);
  } catch (error) {
    notify(error.message);
  }
}

function officeOperationStatusText(status) {
  return ({
    pending: "等待执行",
    executing: "执行中",
    completed: "执行完成",
    failed: "执行失败",
  })[status] || status;
}

function officeDraftStatusText(status) {
  return ({
    draft: "仅预览",
    pending_confirmation: "等待确认",
    approved: "已批准 · 未执行",
    rejected: "已拒绝",
    executing: "执行中",
    completed: "已完成",
    failed: "执行失败",
    cancelled: "已取消",
  })[status] || status;
}

function officeDraftAction(label, style, action) {
  const button = document.createElement("button");
  button.type = "button";
  button.textContent = label;
  if (style) button.classList.add(style);
  button.addEventListener("click", action);
  return button;
}

async function submitOfficeDraft(item) {
  if (!confirm(`提交草稿「${item.title}」进行人工确认？提交后内容将冻结。`)) return;
  try {
    const result = await api(`/api/office/drafts/${item.id}/confirmation`, { method: "POST" });
    await refreshOfficeDrafts();
    notify(result.message || "草稿已进入等待确认状态");
  } catch (error) {
    notify(error.message);
  }
}

async function decideOfficeDraft(item, decision) {
  const approved = decision === "approved";
  const action = approved ? "批准" : "拒绝";
  const warning = approved
    ? "批准只记录决定；之后还要创建持久化执行任务并再次确认，不会立即发送或创建日程。"
    : "拒绝后不会产生外部操作。";
  if (!confirm(`${action}草稿「${item.title}」？\n\n${warning}`)) return;
  try {
    const result = await api(`/api/office/drafts/${item.id}/decision`, {
      method: "POST",
      body: JSON.stringify({ decision, reason: `用户在 Web 草稿箱${action}` }),
    });
    await refreshOfficeDrafts();
    notify(result.message);
  } catch (error) {
    notify(error.message);
  }
}

async function showOfficeDraftEvents(item, card) {
  try {
    const result = await api(`/api/office/drafts/${item.id}/events`);
    card.querySelector(".office-draft-events")?.remove();
    const list = document.createElement("ol");
    list.className = "office-draft-events";
    const events = result.events || [];
    if (!events.length) {
      const row = document.createElement("li");
      row.textContent = "尚无状态迁移记录";
      list.append(row);
    }
    for (const event of events) {
      const row = document.createElement("li");
      row.textContent = `${formatDateTime(event.created_at)} · ${officeDraftStatusText(event.from_status)} → ${officeDraftStatusText(event.to_status)}${event.reason ? ` · ${event.reason}` : ""}`;
      list.append(row);
    }
    card.append(list);
  } catch (error) {
    notify(error.message);
  }
}

async function deleteOfficeDraft(item) {
  if (!confirm(`删除草稿「${item.title}」？此操作不可恢复。`)) return;
  try {
    await api(`/api/office/drafts/${item.id}`, { method: "DELETE" });
    state.officeDrafts = state.officeDrafts.filter(draft => draft.id !== item.id);
    renderOfficeDrafts();
    notify("办公草稿已删除");
  } catch (error) {
    notify(error.message);
  }
}

async function saveMemory(event) {
  event.preventDefault();
  const content = elements.memoryContent.value.trim();
  if (!content) return;
  const payload = {
    kind: elements.memoryKind.value,
    content,
    importance: Number(elements.memoryImportance.value),
    expires_at: elements.memoryExpiresAt.value ? new Date(elements.memoryExpiresAt.value).toISOString() : "",
  };
  const editingID = state.editingMemoryID;
  elements.saveMemory.disabled = true;
  elements.saveMemory.textContent = editingID ? "保存中…" : "添加中…";
  try {
    await api(editingID ? `/api/memories/${editingID}` : "/api/memories", {
      method: editingID ? "PUT" : "POST",
      body: JSON.stringify(payload),
    });
    resetMemoryForm();
    await refreshMemories();
    notify(editingID ? "长期记忆已更新" : "长期记忆已添加");
  } catch (error) {
    notify(error.message);
  } finally {
    elements.saveMemory.disabled = false;
    elements.saveMemory.textContent = state.editingMemoryID ? "保存修改" : "添加记忆";
  }
}

async function refreshMemories() {
  const result = await api("/api/memories?include_expired=true");
  state.memories = result.memories || [];
  renderMemories();
}

function memoryStatusText() {
  if (state.memoryAutoCapture && state.memoryRecall) {
    return "已开启对话候选提取、同 Key 合并和相关记忆召回。自动记忆会标注来源，手动修正后不会被覆盖；本轮输入与旧记忆冲突时以本轮为准。";
  }
  if (state.memoryAutoCapture) {
    return "已开启对话候选提取与同 Key 合并，但召回注入已关闭。所有记忆仍可随时编辑或删除。";
  }
  if (state.memoryRecall) {
    return "自动提取已关闭；手动维护的相关记忆仍会在回答前召回。所有记忆都可随时编辑或删除。";
  }
  return "自动提取和召回当前均已关闭，仅支持手动管理长期记忆。";
}

function renderMemories() {
  elements.memoryCount.textContent = `${state.memories.length} 条记忆`;
  elements.memoryList.replaceChildren();
  if (!state.memories.length) {
    const empty = document.createElement("div");
    empty.className = "document-empty";
    empty.textContent = state.memoryAutoCapture
      ? "还没有长期记忆。可以在对话中明确表达稳定偏好，也可以手动添加。"
      : "还没有长期记忆。可以先手动添加一条可控记忆。";
    elements.memoryList.append(empty);
    return;
  }
  for (const item of state.memories) elements.memoryList.append(memoryNode(item));
}

function memoryNode(item) {
  const card = document.createElement("article");
  card.className = `memory-item${item.expires_at && new Date(item.expires_at) <= new Date() ? " expired" : ""}`;
  const heading = document.createElement("div");
  heading.className = "memory-item-heading";
  const kind = document.createElement("strong");
  kind.textContent = item.kind === "semantic" ? "Semantic" : "Episodic";
  const importance = document.createElement("span");
  importance.textContent = `重要性 ${Number(item.importance).toFixed(1)}`;
  heading.append(kind, importance);
  const content = document.createElement("p");
  content.textContent = item.content;
  const meta = document.createElement("small");
  const source = item.source_type === "manual" ? "手动创建" : "对话提取";
  const corrected = item.user_edited && item.source_type === "conversation" ? " · 已人工修正" : "";
  const expiry = item.expires_at ? ` · 过期 ${formatDateTime(item.expires_at)}` : " · 永不过期";
  meta.textContent = source + corrected + expiry;
  const actions = document.createElement("div");
  actions.className = "memory-item-actions";
  const edit = document.createElement("button");
  edit.type = "button";
  edit.textContent = "编辑";
  edit.addEventListener("click", () => startMemoryEdit(item));
  const remove = document.createElement("button");
  remove.type = "button";
  remove.textContent = "删除";
  remove.addEventListener("click", () => deleteMemory(item));
  actions.append(edit, remove);
  card.append(heading, content, meta, actions);
  return card;
}

function startMemoryEdit(item) {
  state.editingMemoryID = item.id;
  elements.memoryKind.value = item.kind;
  elements.memoryContent.value = item.content;
  elements.memoryImportance.value = item.importance;
  elements.memoryExpiresAt.value = item.expires_at ? toLocalDateTime(item.expires_at) : "";
  elements.saveMemory.textContent = "保存修改";
  elements.cancelMemoryEdit.hidden = false;
  elements.memoryContent.focus();
}

function resetMemoryForm() {
  state.editingMemoryID = null;
  elements.memoryForm.reset();
  elements.memoryKind.value = "semantic";
  elements.memoryImportance.value = "0.5";
  elements.saveMemory.textContent = "添加记忆";
  elements.cancelMemoryEdit.hidden = true;
}

async function deleteMemory(item) {
  if (!confirm(`删除这条 ${item.kind} 记忆？此操作不可恢复。`)) return;
  try {
    await api(`/api/memories/${item.id}`, { method: "DELETE" });
    if (state.editingMemoryID === item.id) resetMemoryForm();
    state.memories = state.memories.filter(memory => memory.id !== item.id);
    renderMemories();
    notify("长期记忆已删除");
  } catch (error) {
    notify(error.message);
  }
}

function toLocalDateTime(value) {
  const date = new Date(value);
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60000);
  return local.toISOString().slice(0, 16);
}

function formatDateTime(value) {
  return new Intl.DateTimeFormat("zh-CN", { dateStyle: "medium", timeStyle: "short" }).format(new Date(value));
}

async function uploadKnowledgeDocument(event) {
  event.preventDefault();
  const file = elements.knowledgeFile.files[0];
  if (!file) return;
  const formData = new FormData();
  formData.append("file", file);
  formData.append("visibility", elements.knowledgeVisibility.value);
  elements.uploadKnowledge.disabled = true;
  elements.uploadKnowledge.textContent = "索引中…";
  try {
    const result = await api("/api/knowledge/documents", { method: "POST", body: formData });
    await refreshKnowledgeDocuments();
    elements.knowledgeUpload.reset();
    elements.selectedFile.textContent = "尚未选择";
    notify(result.deduplicated ? "文档内容已存在，未重复索引" : "文档已完成分块和索引");
  } catch (error) {
    notify(error.message);
  } finally {
    elements.uploadKnowledge.disabled = false;
    elements.uploadKnowledge.textContent = "上传并索引";
  }
}

async function refreshKnowledgeDocuments() {
  const result = await api("/api/knowledge/documents");
  state.documents = result.documents || [];
  renderKnowledgeDocuments();
}

async function deleteKnowledgeDocument(document) {
	if (!confirm(`删除「${document.name}」及其全部分块？`)) return;
	try {
		await api(`/api/knowledge/documents/${document.id}`, { method: "DELETE" });
		// 删除当前版本后，服务端可能自动恢复上一版本，因此必须重新读取列表。
		await refreshKnowledgeDocuments();
		notify("文档已删除");
	} catch (error) {
		notify(error.message);
	}
}

function renderKnowledgeDocuments() {
	elements.knowledgeCount.textContent = `${state.documents.length} 份文档`;
	elements.knowledgeDocuments.replaceChildren();
	if (!state.documents.length) {
		const empty = document.createElement("div");
		empty.className = "document-empty";
		empty.textContent = "还没有文档，上传后就可以在对话中提问。";
		elements.knowledgeDocuments.append(empty);
		return;
	}
	for (const document of state.documents) {
		const item = documentNode(document);
		elements.knowledgeDocuments.append(item);
	}
}

function documentNode(document) {
	const item = window.document.createElement("div");
	item.className = "document-item";
	const info = window.document.createElement("div");
	const name = window.document.createElement("strong");
	name.textContent = document.name;
	const detail = window.document.createElement("span");
	const visibility = document.visibility === "public" ? "所有用户" : "仅自己";
	detail.textContent = `v${document.version} · ${visibility} · ${document.chunk_count} 个分块 · ${document.embedding_model}`;
	info.append(name, detail);
	const versions = window.document.createElement("button");
	versions.type = "button";
	versions.textContent = "版本";
	versions.addEventListener("click", () => showKnowledgeVersions(document));
	const remove = window.document.createElement("button");
	remove.type = "button";
	remove.textContent = "删除";
	remove.addEventListener("click", () => deleteKnowledgeDocument(document));
	item.append(info, versions, remove);
	return item;
}

async function showKnowledgeVersions(document) {
	try {
		const result = await api(`/api/knowledge/documents/${document.id}/versions`);
		const labels = (result.documents || []).map(item => `v${item.version}${item.is_latest ? "（当前）" : ""}`);
		notify(labels.length ? `「${document.name}」版本：${labels.join("、")}` : "没有可见版本");
	} catch (error) {
		notify(error.message);
	}
}

async function createConversation() {
  if (state.busy) return;
  try {
    const conversation = await api("/api/conversations", {
      method: "POST",
      body: JSON.stringify({ title: "" }),
    });
    state.conversations.unshift(conversation);
    renderConversations();
    await selectConversation(conversation.id);
    closeSidebar();
    elements.messageInput.focus();
  } catch (error) {
    notify(error.message);
  }
}

async function selectConversation(id) {
  if (state.busy || id === state.activeID) return;
  state.activeID = id;
  const conversation = state.conversations.find(item => item.id === id);
  elements.conversationTitle.textContent = conversation?.title || "对话";
  renderConversations();
  try {
    const result = await api(`/api/conversations/${id}/messages`);
    state.messages = result.messages || [];
    renderMessages();
    closeSidebar();
  } catch (error) {
    notify(error.message);
  }
}

async function deleteConversation(id) {
  if (state.busy || !confirm("删除这个对话及其执行记录？此操作不可恢复。")) return;
  try {
    await api(`/api/conversations/${id}`, { method: "DELETE" });
    state.conversations = state.conversations.filter(item => item.id !== id);
    if (state.activeID === id) {
      state.activeID = null;
      state.messages = [];
      if (state.conversations.length) await selectConversation(state.conversations[0].id);
      else renderMessages();
    }
    renderConversations();
  } catch (error) {
    notify(error.message);
  }
}

async function renameActiveConversation() {
  if (!state.activeID || state.busy) return;
  const current = state.conversations.find(item => item.id === state.activeID);
  const title = prompt("对话名称", current?.title || "");
  if (!title || title.trim() === current?.title) return;
  try {
    await api(`/api/conversations/${state.activeID}`, {
      method: "PATCH",
      body: JSON.stringify({ title: title.trim() }),
    });
    current.title = title.trim();
    elements.conversationTitle.textContent = current.title;
    renderConversations();
  } catch (error) {
    notify(error.message);
  }
}

async function sendMessage(rawContent) {
  const content = rawContent.trim();
  if (!content || state.busy) return;
  if (!state.activeID) await createConversation();
  if (!state.activeID) return;

  elements.messageInput.value = "";
  resizeInput();
  setBusy(true);
  const now = new Date().toISOString();
  state.messages.push({ id: `local-${Date.now()}`, role: "user", content, created_at: now });
  state.draft = { id: `draft-${Date.now()}`, role: "assistant", content: "", created_at: now, traces: [], streaming: true };
  state.messages.push(state.draft);
  renderMessages();

  const controller = new AbortController();
  state.controller = controller;
  try {
    const response = await fetch(`/api/conversations/${state.activeID}/messages`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "Accept": "text/event-stream" },
      body: JSON.stringify({ content }),
      signal: controller.signal,
    });
    if (!response.ok || !response.body) {
      const body = await response.json().catch(() => ({}));
      throw new Error(body.error || `请求失败 (${response.status})`);
    }
    await consumeSSE(response.body, handleAgentEvent);
  } catch (error) {
    if (error.name === "AbortError") {
      notify("已停止生成");
    } else {
      notify(error.message);
    }
    if (state.draft && !state.draft.content) {
      state.draft.content = error.name === "AbortError" ? "生成已停止。" : `执行失败：${error.message}`;
    }
  } finally {
    if (state.draft) state.draft.streaming = false;
    state.draft = null;
    state.controller = null;
    setBusy(false);
    renderMessages();
    await refreshConversations();
    elements.messageInput.focus();
  }
}

function handleAgentEvent(type, event) {
  if (!state.draft) return;
  switch (type) {
    case "start": {
      const optimisticUser = state.messages[state.messages.length - 2];
      if (event.message && optimisticUser) Object.assign(optimisticUser, event.message);
      state.draft.runID = event.run_id;
      break;
    }
    case "delta":
      state.draft.content += event.content || "";
      break;
    case "tool_call":
      state.draft.traces.push({
        name: event.tool_name,
		key: event.tool_name,
        id: event.tool_call_id,
        arguments: prettyJSON(event.arguments),
        result: "执行中…",
        done: false,
      });
      break;
	case "agent_handoff_started":
	  state.draft.traces.push({
		name: `协作：${agentDisplayName(event.tool_name)}`,
		key: event.tool_name,
		id: event.tool_call_id,
		childRunID: event.child_run_id,
		arguments: prettyJSON(event.arguments),
		result: "专业 Agent 正在处理…",
		done: false,
	  });
	  break;
	case "agent_output": {
	  const trace = [...state.draft.traces].reverse().find(item => !item.done && item.key === event.agent_name);
	  if (trace && event.content) trace.result = event.content;
	  break;
	}
    case "tool_result": {
	  const trace = [...state.draft.traces].reverse().find(item => !item.done && (!event.tool_name || item.key === event.tool_name));
      if (trace) {
        trace.result = prettyJSON(event.content);
        trace.done = true;
      }
	  if (event.tool_name === "preview_email_draft" || event.tool_name === "preview_calendar_draft") {
		refreshOfficeDrafts().catch(() => {});
	  }
      break;
    }
	case "agent_handoff_completed": {
	  const trace = [...state.draft.traces].reverse().find(item => !item.done && (item.id === event.tool_call_id || item.key === event.tool_name));
	  if (trace) {
		trace.result = event.content || trace.result;
		trace.childRunID = event.child_run_id || trace.childRunID;
		trace.done = true;
	  }
	  break;
	}
    case "approval_required":
      state.draft.approval = event.approval;
      break;
    case "approval_approved":
    case "approval_rejected":
    case "approval_expired":
      if (event.approval) state.draft.approval = event.approval;
      break;
    case "done":
      if (event.message) {
        const traces = state.draft.traces;
        Object.assign(state.draft, event.message, { traces, metrics: event.metrics, streaming: false });
      }
      if (event.memory && (event.memory.created > 0 || event.memory.updated > 0)) {
        // 回答完成后刷新记忆计数；失败不会影响本轮回答展示。
        refreshMemories().catch(() => {});
      }
      break;
    case "error":
      throw new Error(event.content || "Agent 执行失败");
  }
  renderMessages();
  scrollToBottom();
}

function agentDisplayName(name) {
  return ({
	research_agent: "研究专家",
	document_agent: "文档专家",
	writer_agent: "写作专家",
  })[name] || name || "专业 Agent";
}

async function consumeSSE(stream, onEvent) {
  const reader = stream.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  while (true) {
    const { value, done } = await reader.read();
    buffer += decoder.decode(value || new Uint8Array(), { stream: !done });
    const frames = buffer.split("\n\n");
    buffer = frames.pop() || "";
    for (const frame of frames) {
      let type = "message";
      const data = [];
      for (const line of frame.split("\n")) {
        if (line.startsWith("event:")) type = line.slice(6).trim();
        if (line.startsWith("data:")) data.push(line.slice(5).trimStart());
      }
      if (!data.length) continue;
      const payload = JSON.parse(data.join("\n"));
      onEvent(type, payload);
    }
    if (done) break;
  }
}

async function refreshConversations() {
  try {
    const result = await api("/api/conversations");
    state.conversations = result.conversations || [];
    const active = state.conversations.find(item => item.id === state.activeID);
    if (active) elements.conversationTitle.textContent = active.title;
    renderConversations();
  } catch (_) {
    // The active result is already rendered; a sidebar refresh can retry later.
  }
}

function renderConversations() {
  elements.conversationList.replaceChildren();
  for (const conversation of state.conversations) {
    const item = document.createElement("div");
    item.className = `conversation-item${conversation.id === state.activeID ? " active" : ""}`;
    item.setAttribute("role", "button");
    item.tabIndex = 0;
    item.innerHTML = `<span class="bubble-icon">◌</span><span class="item-title"></span><button type="button" class="conversation-delete" title="删除" aria-label="删除">×</button>`;
    item.querySelector(".item-title").textContent = conversation.title;
    item.addEventListener("click", () => selectConversation(conversation.id));
    item.addEventListener("keydown", event => {
      if (event.key === "Enter" || event.key === " ") selectConversation(conversation.id);
    });
    item.querySelector(".conversation-delete").addEventListener("click", event => {
      event.stopPropagation();
      deleteConversation(conversation.id);
    });
    elements.conversationList.append(item);
  }
}

function renderMessages() {
  elements.messageList.querySelectorAll(".message").forEach(node => node.remove());
  elements.welcome.hidden = state.messages.length > 0;
  for (const message of state.messages) {
    const article = document.createElement("article");
    article.className = `message ${message.role}`;
    if (message.role === "user") {
      article.innerHTML = `<div class="message-content"><div class="message-meta">你</div><div class="bubble"></div></div>`;
    } else {
      article.innerHTML = `<div class="avatar">Z</div><div class="message-content"><div class="message-meta">Zora · Agent</div><div class="approval-slot"></div><div class="trace-list"></div><div class="bubble"></div></div>`;
      renderApproval(article.querySelector(".approval-slot"), message.approval);
      renderTraces(article.querySelector(".trace-list"), message.traces || []);
    }
    const bubble = article.querySelector(".bubble");
    bubble.innerHTML = renderMarkdown(message.content || "");
    if (message.streaming) {
      const cursor = document.createElement("span");
      cursor.className = "cursor";
      bubble.append(cursor);
    }
    if (message.role !== "user" && message.metrics) {
      article.querySelector(".message-content").append(renderMessageMetrics(message.metrics));
    }
    elements.messageList.append(article);
  }
}

function renderMessageMetrics(metrics) {
  const container = document.createElement("div");
  container.className = "message-metrics";
  const values = [
    `耗时 ${formatMilliseconds(metrics.duration_ms)}`,
    metrics.time_to_first_token_ms == null ? "首字 —" : `首字 ${formatMilliseconds(metrics.time_to_first_token_ms)}`,
    metrics.usage_complete ? `Token ${metrics.total_tokens || 0}` : "Token 未完整上报",
    `模型 ${metrics.model_calls || 0} · 工具 ${metrics.tool_calls || 0}`,
  ];
  for (const value of values) {
    const item = document.createElement("span");
    item.textContent = value;
    container.append(item);
  }
  return container;
}

function renderApproval(container, approval) {
  if (!approval) return;
  const card = document.createElement("section");
  card.className = `approval-card ${approval.status}`;
  const title = document.createElement("strong");
  title.textContent = approval.status === "pending" ? "等待人工确认" : approvalStatusText(approval.status);
  const reason = document.createElement("p");
  reason.textContent = approval.trigger_reason || "该操作需要人工确认后才能继续。";
  card.append(title, reason);
  if (approval.status === "pending") {
    const actions = document.createElement("div");
    actions.className = "approval-actions";
    const reject = document.createElement("button");
    reject.type = "button";
    reject.className = "secondary";
    reject.textContent = "拒绝";
    const approve = document.createElement("button");
    approve.type = "button";
    approve.textContent = "批准并继续";
    reject.addEventListener("click", () => decideApproval(approval.id, "rejected", [approve, reject]));
    approve.addEventListener("click", () => decideApproval(approval.id, "approved", [approve, reject]));
    actions.append(reject, approve);
    card.append(actions);
  } else if (approval.decision_reason) {
    const decisionReason = document.createElement("small");
    decisionReason.textContent = `说明：${approval.decision_reason}`;
    card.append(decisionReason);
  }
  container.append(card);
}

async function decideApproval(approvalID, decision, buttons) {
  buttons.forEach(button => { button.disabled = true; });
  try {
    const item = await api(`/api/approvals/${approvalID}/decision`, {
      method: "POST",
      body: JSON.stringify({ decision }),
    });
    if (state.draft?.approval?.id === approvalID) {
      state.draft.approval = item;
      renderMessages();
      scrollToBottom();
    }
  } catch (error) {
    buttons.forEach(button => { button.disabled = false; });
    notify(error.message);
  }
}

function approvalStatusText(status) {
  return ({ approved: "已批准，继续执行", rejected: "审批未通过", expired: "审批已超时" })[status] || "审批状态已更新";
}

function renderTraces(container, traces) {
  for (const trace of traces) {
    const details = document.createElement("details");
    details.className = `trace-item${trace.done ? " done" : ""}`;
    const summary = document.createElement("summary");
    summary.textContent = trace.name || "tool";
    const status = document.createElement("span");
    status.textContent = trace.done ? "已完成" : "执行中";
    summary.append(status);
    const body = document.createElement("div");
    body.className = "trace-body";
    const runLine = trace.childRunID ? `子 Run\n${trace.childRunID}\n\n` : "";
    body.textContent = `${runLine}输入\n${trace.arguments || "{}"}\n\n输出\n${trace.result || ""}`;
    details.append(summary, body);
    container.append(details);
  }
}

function renderMarkdown(value) {
  if (!value) return "";
  const parts = value.split("```");
  return parts.map((part, index) => {
    if (index % 2 === 1) {
      const lines = part.replace(/^\w+\n/, "");
      return `<pre><code>${escapeHTML(lines.trim())}</code></pre>`;
    }
    return escapeHTML(part)
      .split(/\n{2,}/)
      .map(paragraph => `<p>${paragraph.replace(/\n/g, "<br>").replace(/`([^`]+)`/g, "<code>$1</code>")}</p>`)
      .join("");
  }).join("");
}

function escapeHTML(value) {
  return value.replace(/[&<>"']/g, char => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#039;" })[char]);
}

function prettyJSON(value) {
  if (!value) return "";
  try {
    const parsed = typeof value === "string" ? JSON.parse(value) : value;
    return JSON.stringify(parsed, null, 2);
  } catch (_) {
    return String(value);
  }
}

function resizeInput() {
  elements.messageInput.style.height = "auto";
  elements.messageInput.style.height = `${Math.min(elements.messageInput.scrollHeight, 180)}px`;
  elements.sendButton.disabled = !elements.messageInput.value.trim() && !state.busy;
}

function setBusy(busy) {
  state.busy = busy;
  elements.sendButton.disabled = !busy && !elements.messageInput.value.trim();
  elements.sendButton.classList.toggle("running", busy);
  elements.sendButton.title = busy ? "停止生成" : "发送";
  elements.sendIcon.textContent = busy ? "■" : "↑";
}

function scrollToBottom() {
  requestAnimationFrame(() => {
    elements.chatScroll.scrollTop = elements.chatScroll.scrollHeight;
  });
}

let toastTimer;
function notify(message) {
  clearTimeout(toastTimer);
  elements.toast.textContent = message;
  elements.toast.classList.add("visible");
  toastTimer = setTimeout(() => elements.toast.classList.remove("visible"), 3200);
}

function openSidebar() { elements.sidebar.classList.add("open"); }
function closeSidebar() { elements.sidebar.classList.remove("open"); }

initialize();
