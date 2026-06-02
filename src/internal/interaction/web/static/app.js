/* ============================================================================
 * CodePilot Web · Frontend Logic
 * 模块：WS 客户端、消息路由、Markdown 渲染、/ 命令下拉、状态栏、错误卡片。
 * 不引入构建工具，原生 ES2020 即可运行；通过 marked 库做 Markdown 渲染。
 * ========================================================================== */

(() => {
    'use strict';

    // ---- DOM 缓存 ----
    const $ = (id) => document.getElementById(id);
    const dom = {
        loading:        $('loading'),
        versionBadge:   $('version-badge'),
        workspacePath:  $('workspace-path'),
        statusText:     $('agent-status-text'),
        statusDot:      $('agent-status-dot'),
        newSessionBtn:  $('new-session-btn'),
        sessionList:    $('session-list'),
        sessionTitle:   $('current-session-title'),
        sessionMeta:    $('current-session-meta'),
        messages:       $('messages'),
        input:          $('input'),
        charCount:      $('char-count'),
        modelName:      $('model-name'),
        ctxPercent:     $('ctx-percent'),
        ctxBar:         null,            // 渲染时按需创建
        sendBtn:        $('send-btn'),
    };

    // ---- 全局状态 ----
    const state = {
        ws: null,
        wsReady: false,
        wsReconnectAttempts: 0,
        wsReconnectTimer: null,
        wsMaxReconnectAttempts: 10,
        wsReconnectIntervalMs: 3000,

        sessionId: null,
        messages: [],                  // [{ role, content }]  与 DOM 镜像
        agentStatus: 'idle',           // idle | thinking | error
        ctx: { used: 0, limit: 100, percentLeft: 100 },
        modelName: '--',
        streaming: false,              // 当前是否有流式响应进行中
        expectingAssistant: false,     // 用户刚发了消息，正在等待 assistant 首个 chunk
        slashOpen: false,
        slashIndex: 0,
        slashItems: [],
        userScrolledUp: false,         // 用户向上滚动后停止自动滚动
        sessionsTableActive: false,    // /sessions 表格视图是否启用（true 时 session_list 响应会渲染表格）
    };

    // ---- / 快捷命令清单（Step 9 落地后将替换为命令注册表查询） ----
    // 带 exec 的项：选中后直接执行命令（不补全到输入框）。
    // 不带 exec 的项：选中后补全到输入框，由用户继续编辑后按 Enter 提交。
    // 补全型命令（如 /resume <id>）在 onSendClicked 中识别前缀后转成对应消息。
    const SLASH_COMMANDS = [
        { cmd: '/new',           desc: '新建一个会话',
          exec: () => sendWS(MsgType.NewSession, {}) },
        { cmd: '/sessions',      desc: '查看历史会话列表',
          exec: () => openSessionsTable() },
        { cmd: '/resume',        desc: '恢复指定 ID 的会话（需后接 ID 前缀）' },
        { cmd: '/clear',         desc: '清空当前会话上下文',
          exec: () => sendWS(MsgType.ClearSession, {}) },
    ];

    // ---- 工具函数 ----
    const escapeHTML = (s) => String(s ?? '')
        .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;').replace(/'/g, '&#39;');

    const formatTime = (iso) => {
        try {
            const d = new Date(iso);
            const now = new Date();
            const sameDay = d.toDateString() === now.toDateString();
            if (sameDay) {
                return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' });
            }
            return d.toLocaleDateString('zh-CN', { month: '2-digit', day: '2-digit' });
        } catch { return '--'; }
    };

    // =========================================================================
    // WebSocket 客户端
    // =========================================================================

    function wsURL() {
        const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
        return `${proto}//${location.host}/ws`;
    }

    function connectWS() {
        if (state.ws && (state.ws.readyState === WebSocket.OPEN || state.ws.readyState === WebSocket.CONNECTING)) {
            return;
        }
        try {
            state.ws = new WebSocket(wsURL());
        } catch (err) {
            console.error('WebSocket 创建失败', err);
            scheduleReconnect();
            return;
        }

        state.ws.addEventListener('open', onWSOpen);
        state.ws.addEventListener('message', onWSMessage);
        state.ws.addEventListener('close', onWSClose);
        state.ws.addEventListener('error', (e) => console.warn('WebSocket 错误', e));
    }

    function onWSOpen() {
        state.wsReady = true;
        state.wsReconnectAttempts = 0;
        hideLoading();
        // 连上后拉取：当前会话 + 历史列表
        sendWS(MsgType.ListSessions, {});
        sendWS(MsgType.GetCurrentSession, {});
    }

    function onWSClose() {
        state.wsReady = false;
        showLoading('连接已断开，正在重连…');
        scheduleReconnect();
    }

    function scheduleReconnect() {
        if (state.wsReconnectTimer) return;
        if (state.wsReconnectAttempts >= state.wsMaxReconnectAttempts) {
            showLoading('无法连接到 CodePilot 服务，请检查后端是否运行');
            return;
        }
        state.wsReconnectAttempts += 1;
        const delay = state.wsReconnectIntervalMs;
        state.wsReconnectTimer = setTimeout(() => {
            state.wsReconnectTimer = null;
            connectWS();
        }, delay);
    }

    function sendWS(type, payload) {
        if (!state.ws || state.ws.readyState !== WebSocket.OPEN) {
            console.warn('WebSocket 未连接，消息丢弃:', type);
            return false;
        }
        try {
            state.ws.send(JSON.stringify({ type, payload }));
            return true;
        } catch (err) {
            console.error('WebSocket 发送失败', err);
            return false;
        }
    }

    function onWSMessage(ev) {
        let msg;
        try { msg = JSON.parse(ev.data); }
        catch (err) { console.error('消息 JSON 解析失败', err, ev.data); return; }
        if (!msg || !msg.type) return;
        handleServerMessage(msg);
    }

    // ---- 消息类型常量（与服务端 protocol.go 一致） ----
    const MsgType = {
        UserInput:         'user_input',
        ListSessions:      'list_sessions',
        NewSession:        'new_session',
        ResumeSession:     'resume_session',
        AbortStream:       'abort_stream',
        GetCurrentSession: 'get_current_session',
        ClearSession:      'clear_session',
        DeleteSession:     'delete_session',
        StreamChunk:       'stream_chunk',
        StreamDone:        'stream_done',
        StreamError:       'stream_error',
        SessionList:       'session_list',
        SessionLoaded:     'session_loaded',
        SessionDeleted:    'session_deleted',
        StatusUpdate:      'status_update',
        ContextUsage:      'context_usage',
    };

    // =========================================================================
    // 服务端消息分发
    // =========================================================================

    function handleServerMessage(msg) {
        switch (msg.type) {
            case MsgType.StatusUpdate:    return onStatusUpdate(msg.payload);
            case MsgType.StreamChunk:     return onStreamChunk(msg.payload);
            case MsgType.StreamDone:      return onStreamDone(msg.payload);
            case MsgType.StreamError:     return onStreamError(msg.payload);
            case MsgType.SessionList:     return onSessionList(msg.payload);
            case MsgType.SessionLoaded:   return onSessionLoaded(msg.payload);
            case MsgType.SessionDeleted:  return onSessionDeleted(msg.payload);
            case MsgType.ContextUsage:    return onContextUsage(msg.payload);
            default: console.warn('未知消息类型:', msg.type, msg.payload);
        }
    }

    function onStatusUpdate(p) {
        if (!p || !p.status) return;
        setAgentStatus(p.status);
    }

    function onStreamChunk(p) {
        if (!p || !p.delta) return;
        state.streaming = true;
        // 首个 chunk 到达：移除占位的 thinking 节点
        if (state.expectingAssistant) {
            state.expectingAssistant = false;
            hideThinking();
        }
        appendStreamDelta(p.delta);
    }

    function onStreamDone(p) {
        state.streaming = false;
        hideThinking();
        finalizeAssistantMessage();
        // 完成后 Send 按钮恢复
        renderSendButton();
        // 流结束：刷新会话列表（保证左侧条目的预览/时间同步）
        sendWS(MsgType.ListSessions, {});
    }

    function onStreamError(p) {
        state.streaming = false;
        state.expectingAssistant = false;
        hideThinking();
        const code = p?.code || 'unknown';
        const message = p?.message || '未知错误';
        renderErrorCard(code, message);
        renderSendButton();
    }

    function onSessionList(p) {
        const sessions = p?.sessions || [];
        renderSessionList(sessions);
        if (state.sessionsTableActive) {
            renderSessionsTable(sessions);
        }
    }

    function onSessionLoaded(p) {
        if (!p) return;
        // 切到任意会话时收起表格视图（/new、/resume、点侧边栏、点击表格行）
        hideSessionsTable();
        state.sessionId = p.session_id || null;
        state.messages = (p.messages || []).map(m => ({ role: m.role, content: m.content || '' }));
        renderAllMessages();
        updateSessionHeader(p.summary);
        // 同步模型名（后端在 session_loaded 中带回 model 字段）
        if (p.model) {
            state.modelName = p.model;
            dom.modelName.textContent = p.model;
        }
        // 同步启动时所在的工作目录（顶栏展示用）
        if (p.workdir && dom.workspacePath) {
            dom.workspacePath.textContent = p.workdir;
            dom.workspacePath.title = p.workdir;
        }
        // 高亮左侧对应会话项
        highlightActiveSession();
        // 任何"会话切换/重置"事件都会改变左侧列表的预览、消息数、更新时间等字段，
        // 这里统一拉一次最新列表，避免出现 /clear 后侧栏标题仍展示旧首条消息这类不一致。
        sendWS(MsgType.ListSessions, {});
    }

    // onSessionDeleted 收到删除完成回执。
    // 后端已经在删除当前会话的情况下追加了一条 session_loaded，
    // 因此这里只需要刷新一次会话列表即可，无需再处理消息区。
    function onSessionDeleted(p) {
        if (!p) return;
        // 不论删除的是不是当前会话，都需要刷新一次列表
        sendWS(MsgType.ListSessions, {});
    }

    function onContextUsage(p) {
        if (!p) return;
        state.ctx.used = p.used || 0;
        state.ctx.limit = p.limit || 100;
        state.ctx.percentLeft = p.percent_left ?? 100;
        renderCtxBar();
    }

    // =========================================================================
    // 渲染层
    // =========================================================================

    function setAgentStatus(status) {
        state.agentStatus = status;
        const map = { idle: '就绪', thinking: '思考中', error: '错误' };
        dom.statusText.textContent = map[status] || status;
        dom.statusDot.dataset.status = status;
        // 输入框禁用态
        dom.input.disabled = (status === 'thinking');
        // 若用户刚发了消息而 thinking 节点尚未渲染（后端 status_update 抢先到达），
        // 兜底补一个；正常情况下 onSendClicked 已主动插入。
        if (status === 'thinking' && state.expectingAssistant) {
            showThinking();
        }
        renderSendButton();
    }

    function renderSendButton() {
        if (state.streaming || state.agentStatus === 'thinking') {
            dom.sendBtn.classList.remove('send-btn');
            dom.sendBtn.classList.add('abort-btn');
            dom.sendBtn.textContent = 'Stop';
            dom.sendBtn.onclick = () => sendWS(MsgType.AbortStream, {});
            dom.sendBtn.title = '停止当前响应 (Esc)';
        } else {
            dom.sendBtn.classList.remove('abort-btn');
            dom.sendBtn.classList.add('send-btn');
            dom.sendBtn.textContent = 'Send';
            dom.sendBtn.onclick = onSendClicked;
            dom.sendBtn.title = '发送 (Enter)';
        }
    }

    function renderSessionList(sessions) {
        if (!sessions.length) {
            dom.sessionList.innerHTML = `
                <div class="sidebar-empty">
                    尚无历史会话<br>
                    <span style="color: var(--fg-faint)">新建一次对话后会自动出现在这里</span>
                </div>`;
            return;
        }
        const frag = document.createDocumentFragment();
        for (const s of sessions) {
            const el = document.createElement('div');
            el.className = 'session-item';
            el.dataset.id = s.id;
            el.setAttribute('role', 'listitem');
            // 删除按钮放在 meta 行右侧；点击时停止冒泡避免触发整行的 resume
            el.innerHTML = `
                <span class="session-preview">${escapeHTML(s.preview || '(空会话)')}</span>
                <span class="session-meta">
                    <span class="session-meta-info">
                        <span>${s.message_count} 条消息</span>
                        <span>${formatTime(s.updated_at)}</span>
                    </span>
                    <button
                        class="session-delete"
                        type="button"
                        title="删除该会话"
                        aria-label="删除会话"
                        data-id="${escapeHTML(s.id)}"
                    ><svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                        <path d="M3 4h10"></path>
                        <path d="M6 4V2.5h4V4"></path>
                        <path d="M4 4l.7 9.2A1.5 1.5 0 0 0 6.2 14.5h3.6a1.5 1.5 0 0 0 1.5-1.3L12 4"></path>
                        <path d="M6.5 7v5"></path>
                        <path d="M9.5 7v5"></path>
                    </svg></button>
                </span>
            `;
            // 行点击：恢复该会话
            el.addEventListener('click', () => {
                sendWS(MsgType.ResumeSession, { id: s.id });
            });
            // 删除按钮：拦截冒泡避免触发整行 resume，然后直接发送 delete_session
            const delBtn = el.querySelector('.session-delete');
            if (delBtn) {
                delBtn.addEventListener('click', (ev) => {
                    ev.stopPropagation();
                    ev.preventDefault();
                    sendWS(MsgType.DeleteSession, { id: s.id });
                });
            }
            frag.appendChild(el);
        }
        dom.sessionList.innerHTML = '';
        dom.sessionList.appendChild(frag);
        highlightActiveSession();
    }

    function highlightActiveSession() {
        if (!dom.sessionList) return;
        for (const el of dom.sessionList.querySelectorAll('.session-item')) {
            el.classList.toggle('is-active', el.dataset.id === state.sessionId);
        }
    }

    // =========================================================================
    // /sessions 表格视图
    // 在主区以表格形式展示「最近 10 个、创建时间倒序」的会话，
    // 点击行直接 resume；可点关闭按钮或开始新对话收起。
    // =========================================================================

    function getSessionsTableEl() {
        let el = document.getElementById('sessions-table');
        if (!el) {
            el = document.createElement('div');
            el.id = 'sessions-table';
            el.className = 'sessions-table';
            el.hidden = true;
            // 插入到 messages 之前，作为 main 的同级 flex 子项
            dom.messages.parentElement.insertBefore(el, dom.messages);
        }
        return el;
    }

    function openSessionsTable() {
        // 标记为表格视图：onSessionList 收到响应时会渲染表格
        state.sessionsTableActive = true;
        getSessionsTableEl();
        sendWS(MsgType.ListSessions, { mode: 'table' });
    }

    function hideSessionsTable() {
        state.sessionsTableActive = false;
        const el = document.getElementById('sessions-table');
        if (el) el.hidden = true;
        // 表格关闭后恢复 messages 区域
        if (dom.messages) dom.messages.hidden = false;
    }

    function renderSessionsTable(sessions) {
        const el = getSessionsTableEl();
        el.innerHTML = buildSessionsTableHTML(sessions);
        el.hidden = false;
        // 表格显示时藏起 messages 区域，避免下方露出空状态 / 旧消息
        if (dom.messages) dom.messages.hidden = true;

        // 行点击 → 触发 resume_session
        el.querySelectorAll('.sessions-table-row').forEach(row => {
            row.addEventListener('click', () => {
                const id = row.dataset.id;
                if (!id) return;
                hideSessionsTable();
                sendWS(MsgType.ResumeSession, { id });
            });
        });
        // 关闭按钮
        const closeBtn = el.querySelector('.sessions-table-close');
        if (closeBtn) closeBtn.addEventListener('click', hideSessionsTable);
    }

    function buildSessionsTableHTML(sessions) {
        const closeBtn = `<button class="sessions-table-close" type="button" aria-label="关闭表格" title="关闭">×</button>`;
        if (!sessions.length) {
            return `
                <div class="sessions-table-header">
                    <span class="sessions-table-title">Recent Sessions</span>
                    <span class="sessions-table-subtitle">按创建时间倒序 · 最近 10 个</span>
                    ${closeBtn}
                </div>
                <div class="sessions-table-empty">尚无历史会话</div>
            `;
        }
        const rows = sessions.map((s, i) => {
            const idShort = s.id.slice(0, 8);
            const name = s.preview || '(空会话)';
            const created = formatDateTime(s.created_at);
            const msgCount = `${s.message_count || 0} 条消息`;
            return `
                <tr class="sessions-table-row" data-id="${escapeHTML(s.id)}" title="点击恢复 · 完整 ID: ${escapeHTML(s.id)}">
                    <td class="col-idx">${i + 1}</td>
                    <td class="col-id"><code>${escapeHTML(idShort)}…</code></td>
                    <td class="col-name">${escapeHTML(name)}</td>
                    <td class="col-count">${escapeHTML(msgCount)}</td>
                    <td class="col-time">${escapeHTML(created)}</td>
                </tr>
            `;
        }).join('');
        return `
            <div class="sessions-table-header">
                <span class="sessions-table-title">Recent Sessions</span>
                <span class="sessions-table-subtitle">按创建时间倒序 · 最近 ${sessions.length} 个</span>
                ${closeBtn}
            </div>
            <div class="sessions-table-scroll">
                <table class="sessions-table-tbl">
                    <thead>
                        <tr>
                            <th class="col-idx">#</th>
                            <th class="col-id">Session ID</th>
                            <th class="col-name">名称</th>
                            <th class="col-count">消息</th>
                            <th class="col-time">创建时间</th>
                        </tr>
                    </thead>
                    <tbody>${rows}</tbody>
                </table>
            </div>
            <div class="sessions-table-hint">
                点击行即可恢复该会话；也可在输入框执行 <kbd>/resume &lt;id&gt;</kbd>
            </div>
        `;
    }

    // formatDateTime 把 ISO 时间格式化为 YYYY-MM-DD HH:MM。
    function formatDateTime(iso) {
        if (!iso) return '--';
        try {
            const d = new Date(iso);
            if (isNaN(d.getTime())) return '--';
            const pad = (n) => String(n).padStart(2, '0');
            return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}`;
        } catch { return '--'; }
    }

    function updateSessionHeader(summary) {
        if (!summary) {
            dom.sessionTitle.textContent = 'CURRENT SESSION';
            dom.sessionMeta.textContent = '--';
            return;
        }
        dom.sessionMeta.textContent = `${summary.message_count} 条 · ${formatTime(summary.updated_at)}`;
    }

    function renderAllMessages() {
        dom.messages.innerHTML = '';
        if (!state.messages.length) {
            renderEmptyState();
            return;
        }
        for (const m of state.messages) {
            appendMessageNode(m.role, m.content, /*streaming=*/ false);
        }
        scrollToBottomIfNeeded();
    }

    function renderEmptyState() {
        dom.messages.innerHTML = `
            <div class="messages-empty">
                <span class="messages-empty-logo" aria-hidden="true">CP</span>
                <span class="messages-empty-title">准备好开始一次新对话</span>
                <span class="messages-empty-hint">
                    直接输入消息后按 <kbd>Enter</kbd> 发送<br>
                    <kbd>Shift</kbd> + <kbd>Enter</kbd> 换行<br>
                    输入 <kbd>/</kbd> 唤出快捷命令（/new · /sessions · /resume · /clear）
                </span>
            </div>`;
    }

    function renderErrorCard(code, message) {
        const card = document.createElement('div');
        card.className = 'error-card';
        card.dataset.code = code;
        card.innerHTML = `<strong>[${escapeHTML(code)}]</strong> ${escapeHTML(message)}`;
        dom.messages.appendChild(card);
        scrollToBottomIfNeeded();
    }

    // ---- 消息节点 ----

    function appendMessageNode(role, content, streaming) {
        const isUser = role === 'user';
        const wrap = document.createElement('div');
        wrap.className = `message ${isUser ? 'message-user' : 'message-assistant'}`;
        wrap.dataset.role = role;

        // 头像：Agent 用 CP（与 LOGO 一致），User 用 U（蓝底白字）
        const avatar = document.createElement('div');
        avatar.className = 'message-avatar';
        avatar.textContent = isUser ? 'U' : 'CP';
        wrap.appendChild(avatar);

        const bubble = document.createElement('div');
        bubble.className = 'message-bubble';
        if (isUser) {
            bubble.textContent = content;       // 用户消息纯文本，避免 XSS
        } else {
            // 助手消息：流式时不渲染，结束时调用 marked.parse
            if (streaming) {
                bubble.textContent = content;
            } else {
                bubble.innerHTML = renderMarkdown(content);
            }
        }
        wrap.appendChild(bubble);

        dom.messages.appendChild(wrap);
        return wrap;
    }

    // ---- Thinking 占位节点（3 个弹跳圆点 + "Thinking…" 文字） ----
    // 在用户发消息后立即插入，在首个 stream_chunk 到达时由 hideThinking 移除。
    function showThinking() {
        if (state._thinkingWrap) return;
        // 清空空状态
        const empty = dom.messages.querySelector('.messages-empty');
        if (empty) empty.remove();

        const wrap = document.createElement('div');
        wrap.className = 'message message-assistant thinking-message';
        wrap.dataset.thinking = '1';

        const avatar = document.createElement('div');
        avatar.className = 'message-avatar';
        avatar.textContent = 'CP';
        wrap.appendChild(avatar);

        const bubble = document.createElement('div');
        bubble.className = 'message-bubble';
        bubble.innerHTML = `
            <span class="thinking-indicator" aria-label="Agent 正在思考">
                <span class="thinking-dot"></span>
                <span class="thinking-dot"></span>
                <span class="thinking-dot"></span>
                <span class="thinking-text">Thinking…</span>
            </span>`;
        wrap.appendChild(bubble);

        dom.messages.appendChild(wrap);
        state._thinkingWrap = wrap;
        scrollToBottomIfNeeded();
    }

    function hideThinking() {
        if (state._thinkingWrap) {
            state._thinkingWrap.remove();
            state._thinkingWrap = null;
        }
    }

    function appendStreamDelta(delta) {
        // 流式 chunk 追加：保证状态机只有一个"in-progress"助手消息
        if (!state._streamingWrap) {
            // 清空空状态 + thinking 占位
            const empty = dom.messages.querySelector('.messages-empty');
            if (empty) empty.remove();
            hideThinking();
            state._streamingWrap = appendMessageNode('assistant', '', true);
            state._streamingBuffer = '';
        }
        state._streamingBuffer += delta;
        const bubble = state._streamingWrap.querySelector('.message-bubble');
        bubble.textContent = state._streamingBuffer;
        scrollToBottomIfNeeded();
    }

    function finalizeAssistantMessage() {
        if (!state._streamingWrap) return;
        const bubble = state._streamingWrap.querySelector('.message-bubble');
        const text = state._streamingBuffer || '';
        bubble.innerHTML = renderMarkdown(text);
        // 把流式消息固化为普通消息
        state.messages.push({ role: 'assistant', content: text });
        state._streamingWrap = null;
        state._streamingBuffer = '';
        scrollToBottomIfNeeded();
    }

    // ---- Markdown 渲染 ----

    function renderMarkdown(text) {
        if (!text) return '';
        try {
            // marked v15 同步返回 string
            return window.marked.parse(text);
        } catch (err) {
            console.error('Markdown 渲染失败', err);
            return escapeHTML(text);
        }
    }

    // ---- 上下文进度条 ----
    function renderCtxBar() {
        const v = state.ctx.percentLeft;
        dom.ctxPercent.textContent = `${v}%`;
        if (!dom.ctxBar) {
            // 在 ctx-percent 元素后插入进度条
            const bar = document.createElement('div');
            bar.className = 'ctx-bar';
            bar.innerHTML = '<div class="ctx-bar-fill"></div>';
            const stat = dom.ctxPercent.closest('.inputbar-stat');
            stat.appendChild(bar);
            dom.ctxBar = bar.firstElementChild;
        }
        dom.ctxBar.style.width = `${v}%`;
        dom.ctxBar.dataset.warning = v < 20 ? 'true' : 'false';
    }

    // ---- 滚动行为 ----

    function scrollToBottomIfNeeded() {
        if (state.userScrolledUp) return;
        requestAnimationFrame(() => {
            dom.messages.scrollTop = dom.messages.scrollHeight;
        });
    }

    function bindScrollWatcher() {
        dom.messages.addEventListener('scroll', () => {
            const el = dom.messages;
            const distFromBottom = el.scrollHeight - el.scrollTop - el.clientHeight;
            // 距底部 > 80px 视为"用户向上滚动"
            state.userScrolledUp = distFromBottom > 80;
        });
    }

    // =========================================================================
    // 输入与 / 命令下拉
    // =========================================================================

    function onSendClicked() {
        const raw = dom.input.value;
        const trimmed = raw.trim();
        if (!trimmed) return;
        if (state.streaming) return;

        // /resume <id> 内部命令：直接发 resume_session，不走 LLM
        // 用 trimmed === '/resume' 或 startsWith('/resume ') 两种形态识别，
        // 避免 tail 空格被 trim 掉后漏判（如 "/resume "）。
        if (trimmed === '/resume' || trimmed.startsWith('/resume ')) {
            const id = trimmed.slice('/resume'.length).trim();
            if (id) {
                // /resume 触发的恢复由 onSessionLoaded 收起表格
                sendWS(MsgType.ResumeSession, { id });
                dom.input.value = '';
                updateCharCount();
                closeSlashDropdown();
            }
            // id 为空时不做任何动作，保留输入让用户继续补全
            return;
        }

        // 表格视图打开时，用户开始输入新对话即收起表格
        if (state.sessionsTableActive) {
            hideSessionsTable();
        }

        // 清空空状态
        const empty = dom.messages.querySelector('.messages-empty');
        if (empty) empty.remove();
        // 用户消息节点
        state.messages.push({ role: 'user', content: raw });
        appendMessageNode('user', raw, false);
        scrollToBottomIfNeeded();
        dom.input.value = '';
        updateCharCount();
        closeSlashDropdown();
        // 立即插入 thinking 占位，标记"等待 assistant 首个 chunk"
        // 首个 stream_chunk 到达时由 onStreamChunk 移除
        state.expectingAssistant = true;
        showThinking();
        // 发送
        sendWS(MsgType.UserInput, { text: raw });
    }

    function updateCharCount() {
        dom.charCount.textContent = String(dom.input.value.length);
    }

    // getMatchingCommands 根据当前输入框内容做前缀过滤。
    // 用户输入 "/" 时返回全部；输入 "/se" 时只返回以 "/se" 起始的命令。
    // 该函数是下拉显示候选的唯一来源，避免 open / refresh 两条路径走出不同列表。
    function getMatchingCommands() {
        const cur = (dom.input.value || '').trim();
        if (!cur.startsWith('/')) return SLASH_COMMANDS.slice();
        return SLASH_COMMANDS.filter(c => c.cmd.startsWith(cur));
    }

    function openSlashDropdown() {
        if (state.slashOpen) return;
        const matches = getMatchingCommands();
        // 无候选时不打开下拉，避免空面板
        if (!matches.length) return;

        const dropdown = document.createElement('div');
        dropdown.className = 'slash-dropdown';
        dropdown.id = 'slash-dropdown';
        dropdown.setAttribute('role', 'listbox');
        state.slashItems = matches.map((c, i) => {
            const item = document.createElement('div');
            item.className = 'slash-item' + (i === 0 ? ' is-selected' : '');
            item.dataset.cmd = c.cmd;
            item.dataset.index = String(i);
            item.setAttribute('role', 'option');
            item.innerHTML = `<span class="slash-cmd">${escapeHTML(c.cmd)}</span><span class="slash-desc">${escapeHTML(c.desc)}</span>`;
            item.addEventListener('mousedown', (e) => {
                e.preventDefault();
                applySlashCompletion(c);
            });
            dropdown.appendChild(item);
            return item;
        });
        state.slashIndex = 0;
        state.slashOpen = true;
        dom.input.parentElement.parentElement.appendChild(dropdown);
    }

    function closeSlashDropdown() {
        const d = document.getElementById('slash-dropdown');
        if (d) d.remove();
        state.slashOpen = false;
        state.slashItems = [];
        state.slashIndex = 0;
    }

    function updateSlashSelection(delta) {
        if (!state.slashOpen || !state.slashItems.length) return;
        state.slashIndex = (state.slashIndex + delta + state.slashItems.length) % state.slashItems.length;
        for (const it of state.slashItems) {
            it.classList.toggle('is-selected', Number(it.dataset.index) === state.slashIndex);
        }
    }

    function applySlashCompletion(entry) {
        // 带 exec 的项：直接执行命令（如 /clear），不补全到输入框
        if (entry && typeof entry.exec === 'function') {
            try { entry.exec(entry.cmd); } catch (err) { console.error('slash exec 失败', err); }
            dom.input.value = '';
            closeSlashDropdown();
            updateCharCount();
            return;
        }
        const cmd = (entry && entry.cmd) || String(entry);
        // 替换当前输入中的 /xxx... 为 cmd（若 cmd 是 /resume，则补一个空格）
        const tail = cmd + (cmd === '/resume' ? ' ' : '');
        dom.input.value = tail;
        dom.input.setSelectionRange(tail.length, tail.length);
        closeSlashDropdown();
        updateCharCount();
        dom.input.focus();
    }

    // refreshSlashDropdown 在输入变化时重建下拉，沿用 getMatchingCommands 的过滤结果。
    // 思路：销毁旧 DOM 后调用 openSlashDropdown 重新构造一份匹配当前输入的候选。
    function refreshSlashDropdown() {
        if (!state.slashOpen) return;
        const matches = getMatchingCommands();
        if (!matches.length) { closeSlashDropdown(); return; }
        const old = document.getElementById('slash-dropdown');
        if (old) old.remove();
        state.slashOpen = false;
        openSlashDropdown();
    }

    // ---- 键盘事件 ----
    function bindInputKeys() {
        dom.input.addEventListener('input', () => {
            updateCharCount();
            const v = dom.input.value;
            // / 命令触发条件：以 / 开头、且不含空格（避免 /foo bar 时仍展开）
            if (v.startsWith('/') && !v.includes(' ')) {
                if (!state.slashOpen) openSlashDropdown();
                else refreshSlashDropdown();
            } else if (state.slashOpen) {
                closeSlashDropdown();
            }
        });

        dom.input.addEventListener('keydown', (e) => {
            // / 下拉的键盘交互优先
            if (state.slashOpen) {
                if (e.key === 'ArrowDown') { e.preventDefault(); updateSlashSelection(+1); return; }
                if (e.key === 'ArrowUp')   { e.preventDefault(); updateSlashSelection(-1); return; }
                if (e.key === 'Enter' || e.key === 'Tab') {
                    e.preventDefault();
                    const sel = state.slashItems[state.slashIndex];
                    if (sel) {
                        const entry = SLASH_COMMANDS.find(c => c.cmd === sel.dataset.cmd);
                        applySlashCompletion(entry);
                    }
                    return;
                }
                if (e.key === 'Escape') { e.preventDefault(); closeSlashDropdown(); return; }
            }
            // 发送 / 换行
            if (e.key === 'Enter' && !e.shiftKey) {
                e.preventDefault();
                onSendClicked();
                return;
            }
            // Esc 在流式时中断
            if (e.key === 'Escape' && state.streaming) {
                e.preventDefault();
                sendWS(MsgType.AbortStream, {});
            }
        });
    }

    // =========================================================================
    // 杂项：全局键盘、新建会话按钮、加载占位
    // =========================================================================

    function bindGlobalKeys() {
        document.addEventListener('keydown', (e) => {
            // 全局 Esc 关闭下拉
            if (e.key === 'Escape' && state.slashOpen) closeSlashDropdown();
        });
    }

    function bindNewSessionBtn() {
        dom.newSessionBtn.addEventListener('click', () => {
            sendWS(MsgType.NewSession, {});
        });
    }

    function showLoading(text) {
        const t = dom.loading.querySelector('.loading-text');
        if (t && text) t.textContent = text;
        dom.loading.classList.remove('is-hidden');
    }

    function hideLoading() {
        dom.loading.classList.add('is-hidden');
        // 动画结束后从 DOM 移除
        setTimeout(() => { if (dom.loading.classList.contains('is-hidden')) dom.loading.style.display = 'none'; }, 400);
    }

    // =========================================================================
    // 启动
    // =========================================================================

    function init() {
        // 防止输入框禁用态导致无法聚焦
        dom.input.disabled = false;
        bindInputKeys();
        bindGlobalKeys();
        bindNewSessionBtn();
        bindScrollWatcher();
        // 初始状态
        renderSendButton();
        renderEmptyState();
        renderCtxBar();
        // 默认状态 idle
        setAgentStatus('idle');
        // 占位显示
        showLoading('CONNECTING TO CODEPILOT');
        // 建立 WS
        connectWS();
        // 失去焦点不影响输入流
        window.addEventListener('beforeunload', () => {
            if (state.ws) try { state.ws.close(); } catch {}
        });
    }

    if (document.readyState === 'loading') {
        document.addEventListener('DOMContentLoaded', init);
    } else {
        init();
    }
})();
