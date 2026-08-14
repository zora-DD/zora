"use strict";

const state = {
  conversations: [],
  activeID: null,
  messages: [],
	 documents: [],
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
	openKnowledge: document.querySelector("#openKnowledge"),
	closeKnowledge: document.querySelector("#closeKnowledge"),
	knowledgeDialog: document.querySelector("#knowledgeDialog"),
	knowledgeUpload: document.querySelector("#knowledgeUpload"),
	knowledgeFile: document.querySelector("#knowledgeFile"),
	selectedFile: document.querySelector("#selectedFile"),
	uploadKnowledge: document.querySelector("#uploadKnowledge"),
	knowledgeDocuments: document.querySelector("#knowledgeDocuments"),
	knowledgeCount: document.querySelector("#knowledgeCount"),
	embeddingModel: document.querySelector("#embeddingModel"),
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
		const [info, result, knowledgeResult] = await Promise.all([
			api("/api/info"), api("/api/conversations"), api("/api/knowledge/documents"),
		]);
    elements.runtimeModel.textContent = info.model;
    elements.runtimeProvider.textContent = `${info.provider} · ${info.version}`;
    state.conversations = result.conversations || [];
		state.documents = knowledgeResult.documents || [];
		elements.embeddingModel.textContent = `Embedding: ${info.embedding_model}`;
    renderConversations();
		renderKnowledgeDocuments();
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

async function uploadKnowledgeDocument(event) {
	event.preventDefault();
	const file = elements.knowledgeFile.files[0];
	if (!file) return;
	const formData = new FormData();
	formData.append("file", file);
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
		state.documents = state.documents.filter(item => item.id !== document.id);
		renderKnowledgeDocuments();
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
	detail.textContent = `${document.chunk_count} 个分块 · ${document.embedding_model}`;
	info.append(name, detail);
	const remove = window.document.createElement("button");
	remove.type = "button";
	remove.textContent = "删除";
	remove.addEventListener("click", () => deleteKnowledgeDocument(document));
	item.append(info, remove);
	return item;
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
        id: event.tool_call_id,
        arguments: prettyJSON(event.arguments),
        result: "执行中…",
        done: false,
      });
      break;
    case "tool_result": {
      const trace = [...state.draft.traces].reverse().find(item => !item.done && (!event.tool_name || item.name === event.tool_name));
      if (trace) {
        trace.result = prettyJSON(event.content);
        trace.done = true;
      }
      break;
    }
    case "done":
      if (event.message) {
        const traces = state.draft.traces;
        Object.assign(state.draft, event.message, { traces, streaming: false });
      }
      break;
    case "error":
      throw new Error(event.content || "Agent 执行失败");
  }
  renderMessages();
  scrollToBottom();
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
      article.innerHTML = `<div class="avatar">Z</div><div class="message-content"><div class="message-meta">Zora · Agent</div><div class="trace-list"></div><div class="bubble"></div></div>`;
      renderTraces(article.querySelector(".trace-list"), message.traces || []);
    }
    const bubble = article.querySelector(".bubble");
    bubble.innerHTML = renderMarkdown(message.content || "");
    if (message.streaming) {
      const cursor = document.createElement("span");
      cursor.className = "cursor";
      bubble.append(cursor);
    }
    elements.messageList.append(article);
  }
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
    body.textContent = `输入\n${trace.arguments || "{}"}\n\n输出\n${trace.result || ""}`;
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
