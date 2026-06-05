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
        messages: [],                  // [{ role, content, tool_call? }]  与 DOM 镜像
        agentStatus: 'idle',           // idle | thinking | tool_running | error
        ctx: { used: 0, limit: 100, percentLeft: 100 },
        modelName: '--',
        streaming: false,              // 当前是否有流式响应进行中
        expectingAssistant: false,     // 用户刚发了消息，正在等待 assistant 首个 chunk
        slashOpen: false,
        slashIndex: 0,
        slashItems: [],
        userScrolledUp: false,         // 用户向上滚动后停止自动滚动
        sessionsTableActive: false,    // /sessions 表格视图是否启用（true 时 session_list 响应会渲染表格）
        _toolById: {},                 // tool_use_id -> DOM 节点，用于 end 事件定位
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
        ToolCallStart:     'tool_call_start',
        ToolCallEnd:       'tool_call_end',
    };

    // ---- 工具名 → 短缩写（图标方块字符）。未知工具回退到 '⚙' ----
    const TOOL_ICON = {
        read_file:    '📖',
        write_file:   '✏',
        bash:         '$_',
        glob:         '*',
        grep:         '⌕',
    };
    const TOOL_ICON_FALLBACK = '⚙';

    // ---- 状态徽章文案映射（与服务端 ToolCallStatus* 一致） ----
    const TOOL_STATUS_LABEL = {
        running:   'running',
        completed: 'done',
        error:     'failed',
        aborted:   'aborted',
        timeout:   'timeout',
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
            case MsgType.ToolCallStart:   return onToolCallStart(msg.payload);
            case MsgType.ToolCallEnd:     return onToolCallEnd(msg.payload);
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
        // 中断/错误时先将已接收的流式内容固化为完整消息，避免内容丢失
        finalizeAssistantMessage();
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
        state.messages = (p.messages || []).map(m => ({
            role: m.role,
            content: m.content || '',
            // 保留 tool_call 字段，否则历史会话中的工具调用记录会丢失
            tool_call: m.tool_call || null,
        }));
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

    // ---- 工具消息：开始 / 结束事件 ----
    // Step 2 引入。tool_call_start 立即插入"running"占位块;
    // tool_call_end 按 tool_use_id 找到对应块并切换为完成/失败/取消态。
    // 设计为幂等：重复收到同一 id 的 start 不重复插入；end 在没有匹配 start
    // 时直接插入一个"已完成"块（兜底）。
    function onToolCallStart(p) {
        if (!p || !p.tool_use_id) return;
        // 若已存在同 id 的块（异常重发），跳过
        if (state._toolById[p.tool_use_id]) return;
        // 关键：在插入工具块之前，先将当前流式助手消息固化为独立消息。
        // 因为 stream_done 在 AgentLoop 全部迭代结束后才发送（不是每次迭代后），
        // 如果不提前固化，所有迭代的文本会累积到同一个流式消息中，
        // 导致"所有分析文本在前 → 所有工具调用在后"的非交替布局。
        finalizeAssistantMessage();
        const node = appendToolStartNode(p.tool_use_id, p.name, p.input, p.started_at);
        state._toolById[p.tool_use_id] = node;
        scrollToBottomIfNeeded();
    }

    function onToolCallEnd(p) {
        if (!p || !p.tool_use_id) return;
        let node = state._toolById[p.tool_use_id];
        if (!node) {
            // 异常路径：end 先到 start 后到（或 start 丢失），按已完成态直接插入
            node = appendToolStartNode(p.tool_use_id, p.name, '', p.started_at);
            state._toolById[p.tool_use_id] = node;
        }
        updateToolEndNode(node, p);
        scrollToBottomIfNeeded();
    }

    // =========================================================================
    // 渲染层
    // =========================================================================

    function setAgentStatus(status) {
        state.agentStatus = status;
        const map = {
            idle: '就绪',
            thinking: '思考中',
            tool_running: '工具执行中',
            error: '错误',
        };
        dom.statusText.textContent = map[status] || status;
        dom.statusDot.dataset.status = status;
        // 输入框禁用态：思考中 / 工具执行中 都不可输入
        dom.input.disabled = (status === 'thinking' || status === 'tool_running');
        // 若用户刚发了消息而 thinking 节点尚未渲染（后端 status_update 抢先到达），
        // 兜底补一个；正常情况下 onSendClicked 已主动插入。
        if (status === 'thinking' && state.expectingAssistant) {
            showThinking();
        }
        renderSendButton();
    }

    function renderSendButton() {
        if (state.streaming || state.agentStatus === 'thinking' || state.agentStatus === 'tool_running') {
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
        // 切换会话时清理打字机动画状态（rAF 可能仍持有旧 DOM 引用）
        if (state._typewriterRafId) {
            cancelAnimationFrame(state._typewriterRafId);
            state._typewriterRafId = null;
        }
        state._streamingWrap = null;
        state._streamingBuffer = '';
        // 切换会话时清空工具 id 索引（旧的 DOM 节点已不在 DOM 树里）
        state._toolById = {};
        if (!state.messages.length) {
            renderEmptyState();
            return;
        }
        for (const m of state.messages) {
            if (m.tool_call) {
                // 历史工具消息：直接以"已完成/失败"态插入，不带 running 占位
                const node = appendToolStartNode(
                    m.tool_call.id,
                    m.tool_call.name,
                    m.tool_call.input,
                    null,
                );
                updateToolEndNode(node, {
                    tool_use_id: m.tool_call.id,
                    name:        m.tool_call.name,
                    output:      m.tool_call.output,
                    is_error:    m.tool_call.is_error,
                    duration_ms: m.tool_call.duration_ms,
                    status:      m.tool_call.status,
                });
                state._toolById[m.tool_call.id] = node;
            } else {
                appendMessageNode(m.role, m.content, /*streaming=*/ false);
            }
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
            // 助手消息：流式时先设空文本占位，实际渲染由 typewriterTick 打字机动画驱动
            if (streaming) {
                bubble.textContent = content;
            } else {
                bubble.innerHTML = renderMarkdown(content);
                enhanceCodeBlocks(bubble);
            }
        }
        wrap.appendChild(bubble);

        dom.messages.appendChild(wrap);
        return wrap;
    }

    // ---- 工具消息块：appendToolStartNode / updateToolEndNode ----
    // 与普通消息节点不同，工具消息块是"自管理"的——start 插入占位、end 切换
    // 状态，全程不依赖全局 _streamingWrap，避免与流式文本互相干扰。

    // 把任意值格式化为展示用的字符串（避免 [object Object]）。
    function formatToolArg(v) {
        if (v == null) return '';
        if (typeof v === 'string') return v;
        if (typeof v === 'object') {
            try { return JSON.stringify(v, null, 2); } catch { return String(v); }
        }
        return String(v);
    }

    // 尝试把 input 解析为对象；解析失败时回退到原文。
    function parseInputObject(input) {
        if (input == null || input === '') return null;
        if (typeof input === 'object') return input;
        try { return JSON.parse(input); } catch { return null; }
    }

    // extractToolSummary 从工具参数中提取关键操作摘要，用于在头部行显示。
    // 例如 Bash → 显示 command，ReadFile → 显示 path，Grep → 显示 pattern 等。
    // 返回空字符串表示无摘要（头部不显示额外信息）。
    function extractToolSummary(name, input) {
        const obj = parseInputObject(input);
        let text = '';
        if (obj && typeof obj === 'object') {
            switch (name) {
                case 'Bash':
                    text = obj.command || '';
                    break;
                case 'ReadFile':
                    text = obj.path || obj.file_path || obj.filePath || '';
                    break;
                case 'WriteFile':
                    text = obj.path || obj.file_path || obj.filePath || '';
                    break;
                case 'Grep':
                    text = obj.pattern || obj.query || '';
                    if (obj.path) text += ' in ' + obj.path;
                    break;
                case 'Glob':
                    text = obj.pattern || '';
                    break;
                default:
                    // 未知工具：取第一个有值的字符串字段作为摘要
                    for (const k of Object.keys(obj)) {
                        const v = obj[k];
                        if (typeof v === 'string' && v.length > 0 && v.length < 200) {
                            text = v;
                            break;
                        }
                    }
            }
        } else if (typeof input === 'string' && input) {
            text = input;
        }
        // 截断到 200 字符，CSS 会进一步按可用宽度省略
        if (text.length > 200) text = text.substring(0, 200) + '…';
        return text;
    }

    // appendToolStartNode 插入"正在执行"占位块并返回 DOM 引用。
    // 参数 input 接受 string（已压缩 JSON）或 object；StartedAtIso 为 ISO 字符串
    // 或 null（null 时不显示开始时间，仅显示 running）。
    function appendToolStartNode(toolUseId, name, input, startedAtIso) {
        const empty = dom.messages.querySelector('.messages-empty');
        if (empty) empty.remove();
        hideThinking();

        const wrap = document.createElement('div');
        wrap.className = 'message-tool';
        wrap.dataset.toolUseId = toolUseId;
        wrap.dataset.status = 'running';
        wrap.dataset.expanded = 'false';

        // 头部：图标 + 工具名 + 状态徽章 + 耗时（运行中显示开始时间） + 折叠箭头
        const header = document.createElement('div');
        header.className = 'message-tool-header';
        header.addEventListener('click', () => {
            wrap.dataset.expanded = (wrap.dataset.expanded === 'true') ? 'false' : 'true';
        });

        const icon = document.createElement('span');
        icon.className = 'message-tool-icon';
        icon.textContent = TOOL_ICON[name] || TOOL_ICON_FALLBACK;
        header.appendChild(icon);

        const nameEl = document.createElement('span');
        nameEl.className = 'message-tool-name';
        nameEl.textContent = name;
        header.appendChild(nameEl);

        // 操作摘要：在头部行显示工具正在执行的具体命令/路径等关键信息
        const summary = document.createElement('span');
        summary.className = 'message-tool-summary';
        summary.textContent = extractToolSummary(name, input);
        header.appendChild(summary);

        const status = document.createElement('span');
        status.className = 'message-tool-status';
        status.textContent = TOOL_STATUS_LABEL.running;
        header.appendChild(status);

        const dur = document.createElement('span');
        dur.className = 'message-tool-duration';
        dur.textContent = startedAtIso ? formatStartedAt(startedAtIso) : '';
        header.appendChild(dur);

        const toggle = document.createElement('span');
        toggle.className = 'message-tool-toggle';
        toggle.setAttribute('aria-hidden', 'true');
        header.appendChild(toggle);

        // 内容包裹容器：将 header 和 details 纵向排列，避免 flex row 布局挤压
        const content = document.createElement('div');
        content.className = 'message-tool-content';
        content.appendChild(header);

        // 折叠区：参数 + 输出（运行时仅参数填了；end 时再补输出）
        const details = document.createElement('div');
        details.className = 'message-tool-details';

        const paramObj = parseInputObject(input);
        if (paramObj && typeof paramObj === 'object') {
            const sec = document.createElement('div');
            sec.className = 'message-tool-section';
            const label = document.createElement('span');
            label.className = 'message-tool-section-label';
            label.textContent = 'Arguments';
            sec.appendChild(label);
            const pre = document.createElement('pre');
            pre.className = 'message-tool-input';
            try { pre.textContent = JSON.stringify(paramObj, null, 2); }
            catch { pre.textContent = formatToolArg(input); }
            sec.appendChild(pre);
            details.appendChild(sec);
        } else if (typeof input === 'string' && input) {
            const sec = document.createElement('div');
            sec.className = 'message-tool-section';
            const label = document.createElement('span');
            label.className = 'message-tool-section-label';
            label.textContent = 'Arguments';
            sec.appendChild(label);
            const pre = document.createElement('pre');
            pre.className = 'message-tool-input';
            pre.textContent = input;
            sec.appendChild(pre);
            details.appendChild(sec);
        }

        content.appendChild(details);
        wrap.appendChild(content);
        dom.messages.appendChild(wrap);
        return wrap;
    }

    // updateToolEndNode 把工具块从 running 切到 done/failed/aborted/timeout。
    // 同时填充 output、设置耗时徽章。如已有 output 节则替换为最新值。
    function updateToolEndNode(node, endPayload) {
        if (!node) return;
        const status = endPayload.status || (endPayload.is_error ? 'error' : 'completed');
        node.dataset.status = status;
        // 保持折叠：参数和 output 默认不展开，用户可点击 header 手动查看详情
        node.dataset.expanded = 'false';

        const statusEl = node.querySelector('.message-tool-status');
        if (statusEl) {
            statusEl.textContent = TOOL_STATUS_LABEL[status] || status;
        }
        const durEl = node.querySelector('.message-tool-duration');
        if (durEl) {
            durEl.textContent = formatDuration(endPayload.duration_ms);
        }
        const nameEl = node.querySelector('.message-tool-name');
        if (nameEl && endPayload.name) {
            nameEl.textContent = endPayload.name;
        }

        // 找到或创建 output 节
        const details = node.querySelector('.message-tool-details');
        if (!details) return;
        let outSec = details.querySelector('.message-tool-section-output');
        if (!outSec) {
            outSec = document.createElement('div');
            outSec.className = 'message-tool-section message-tool-section-output';
            const label = document.createElement('span');
            label.className = 'message-tool-section-label';
            label.textContent = 'Output';
            outSec.appendChild(label);
            const pre = document.createElement('pre');
            pre.className = 'message-tool-output';
            outSec.appendChild(pre);
            details.appendChild(outSec);
        }
        const pre = outSec.querySelector('pre');
        if (pre) {
            pre.textContent = (endPayload.output == null) ? '' : String(endPayload.output);
            pre.classList.toggle('message-tool-output-error', !!endPayload.is_error);
        }
    }

    // formatDuration 把毫秒数格式化为 "Xms" / "X.Ys"。
    function formatDuration(ms) {
        if (ms == null) return '';
        if (ms < 1000) return `${ms}ms`;
        return `${(ms / 1000).toFixed(2)}s`;
    }

    // formatStartedAt 把 ISO 时间格式化为 HH:MM:SS。
    function formatStartedAt(iso) {
        try {
            const d = new Date(iso);
            if (isNaN(d.getTime())) return '';
            const pad = (n) => String(n).padStart(2, '0');
            return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
        } catch { return ''; }
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
        const isFirstChunk = !state._streamingWrap;
        if (isFirstChunk) {
            // 清空空状态 + thinking 占位
            const empty = dom.messages.querySelector('.messages-empty');
            if (empty) empty.remove();
            hideThinking();
            state._streamingWrap = appendMessageNode('assistant', '', true);
            state._streamingBuffer = '';
            state._revealedLen = 0;
        }
        state._streamingBuffer += delta;

        // 启动打字机动画（仅首个 delta 或动画已停止时触发）
        if (!state._typewriterRafId) {
            state._typewriterRafId = requestAnimationFrame(typewriterTick);
        }
    }

    function finalizeAssistantMessage() {
        if (!state._streamingWrap) return;

        // 1. 停止打字机动画
        if (state._typewriterRafId) {
            cancelAnimationFrame(state._typewriterRafId);
            state._typewriterRafId = null;
        }

        const bubble = state._streamingWrap.querySelector('.message-bubble');
        const text = state._streamingBuffer || '';

        // 2. 最终渲染：使用 renderMarkdown（不带光标），确保最终内容干净
        if (text && bubble) {
            bubble.innerHTML = renderMarkdown(text);
        }

        // 3. 对已渲染的内容执行最终增强：hljs 语法高亮、代码块 header（复制按钮）、JSON 校验
        enhanceCodeBlocks(bubble);

        // 4. 固化为普通消息
        state.messages.push({ role: 'assistant', content: text });
        state._streamingWrap = null;
        state._streamingBuffer = '';
        state._revealedLen = 0;
        scrollToBottomIfNeeded();
    }

    // ---- 流式渲染核心：打字机效果 ----
    // 核心思路：把「收到了多少」和「显示了多少」解耦。
    // appendStreamDelta 只负责往缓冲区追加文本；
    // typewriterTick 每帧匀速推进"揭示光标"，逐步展示缓冲区内容。
    // 无论 LLM token 到达多快，用户看到的都是匀速的打字机输出。
    // 流结束后由 finalizeAssistantMessage 立即显示全部剩余内容。

    /** 每帧推进的字符数（基础速度） */
    const TYPEWRITER_BASE_SPEED = 2;
    /** 自适应加速阈值：积压超过此值时提速追赶 */
    const TYPEWRITER_SPEEDUP_THRESHOLD = 30;
    /** 每帧最大推进字符数（防止长响应结尾时追赶太猛） */
    const TYPEWRITER_MAX_SPEED = 24;

    /**
     * typewriterTick — 打字机动画的每帧回调。
     * 推进"已揭示长度"，渲染可见部分（带光标），如仍有积压则调度下一帧。
     */
    function typewriterTick() {
        state._typewriterRafId = null;
        if (!state._streamingWrap || !state._streamingBuffer) return;

        const bufferLen = state._streamingBuffer.length;
        if (state._revealedLen >= bufferLen) return; // 已追上，等待新 delta 触发

        // 自适应速度：积压越多越快，避免长响应追赶太慢
        const backlog = bufferLen - state._revealedLen;
        const speed = backlog > TYPEWRITER_SPEEDUP_THRESHOLD
            ? Math.min(backlog, TYPEWRITER_MAX_SPEED)
            : TYPEWRITER_BASE_SPEED;
        state._revealedLen = Math.min(state._revealedLen + speed, bufferLen);

        // 取已揭示部分的文本，走 Markdown 解析 + DOMPurify 过滤
        const visibleText = state._streamingBuffer.substring(0, state._revealedLen);
        const bubble = state._streamingWrap.querySelector('.message-bubble');
        if (bubble) {
            const html = streamingRenderMarkdown(visibleText);
            bubble.innerHTML = html + '<span class="cursor" aria-hidden="true"></span>';
        }

        // 直接滚动（已在 rAF 回调内，无需再套一层 rAF）
        if (!state.userScrolledUp) {
            dom.messages.scrollTop = dom.messages.scrollHeight;
        }

        // 还有未显示的内容，调度下一帧
        if (state._revealedLen < bufferLen) {
            state._typewriterRafId = requestAnimationFrame(typewriterTick);
        }
    }

    /**
     * closeOpenFences — 检测文本中未闭合的围栏代码块，自动在末尾补上闭合标记。
     * 确保 marked.parse 对不完整代码块也能生成 <pre><code> 容器，而非当作纯文本。
     *
     * 支持两种围栏标记：```（反引号）和 ~~~（波浪号）。
     * 仅处理位于行首（可选缩进）的围栏标记。
     *
     * @param {string} text - 已累积的流式文本
     * @returns {string} - 处理后的文本（可能追加了闭合标记）
     */
    function closeOpenFences(text) {
        // 按行扫描，追踪每种围栏的开启/闭合状态
        const lines = text.split('\n');
        // 用栈追踪当前打开的围栏：每个元素为围栏标记的字符（` 或 ~）
        const fenceStack = [];
        const fenceRe = /^(\s{0,3})(```+|~~~+)/;

        for (const line of lines) {
            const m = line.match(fenceRe);
            if (m) {
                const fenceChar = m[2][0]; // ` 或 ~
                if (fenceStack.length > 0 && fenceStack[fenceStack.length - 1] === fenceChar) {
                    // 闭合当前围栏
                    fenceStack.pop();
                } else {
                    // 开启新围栏
                    fenceStack.push(fenceChar);
                }
            }
        }

        // 栈中剩余的即为未闭合的围栏，逐个补上闭合标记
        let result = text;
        for (const fenceChar of fenceStack) {
            result += '\n' + fenceChar.repeat(3);
        }
        return result;
    }

    /**
     * htmlDecode — 将 marked 输出中的 HTML 实体还原为原始字符。
     * marked 会将 code 块内的 < > & " ' 转义，传给 hljs.highlight() 前需还原。
     * 使用纯字符串替换，避免创建临时 DOM 元素的额外开销。
     *
     * @param {string} str - 含 HTML 实体的字符串
     * @returns {string} - 还原后的字符串
     */
    function htmlDecode(str) {
        return str
            .replace(/&amp;/g, '&')
            .replace(/&lt;/g, '<')
            .replace(/&gt;/g, '>')
            .replace(/&quot;/g, '"')
            .replace(/&#39;/g, "'");
    }

    /**
     * highlightCodeInHTML — 对 HTML 字符串中的代码块应用 hljs 语法高亮。
     *
     * 在 marked.parse() 之后、DOMPurify.sanitize() 之前调用。
     * 使用 hljs.highlight()（字符串 API）而非 hljs.highlightElement()（DOM API），
     * 避免每帧 innerHTML 全量替换时的 DOM 节点销毁/重建开销。
     *
     * 工作流程：
     *   1. 正则匹配 <code class="language-xxx">content</code>
     *   2. htmlDecode 还原转义字符
     *   3. hljs.highlight(rawCode, {language}) 得到带 <span class="hljs-keyword"> 的高亮 HTML
     *   4. 替换回原位，追加 hljs class 使 CSS 主题生效
     *
     * @param {string} html - marked.parse() 输出的 HTML 字符串
     * @returns {string} - 含高亮 token 的 HTML 字符串（未 sanitize）
     */
    function highlightCodeInHTML(html) {
        if (!window.hljs) return html;
        return html.replace(
            /<code class="language-([\w+-]+)">([\s\S]*?)<\/code>/g,
            (match, lang, codeHtml) => {
                const rawCode = htmlDecode(codeHtml);
                try {
                    const result = window.hljs.highlight(rawCode, { language: lang });
                    return `<code class="language-${lang} hljs">${result.value}</code>`;
                } catch {
                    // 语言不被 hljs 支持时原样返回
                    return match;
                }
            }
        );
    }

    /**
     * streamingRenderMarkdown — 流式渲染专用的 Markdown 解析。
     * 与 renderMarkdown 的区别：
     *   1. 先预处理未闭合代码块（closeOpenFences）
     *   2. marked 解析后对代码块内联 hljs 语法高亮（highlightCodeInHTML）
     *   3. 最后 DOMPurify 过滤
     * XSS 防护规则与 renderMarkdown 完全一致，安全不降级。
     *
     * @param {string} text - 已累积的流式文本（可能含未闭合围栏）
     * @returns {string} - 经高亮 + DOMPurify 过滤的安全 HTML
     */
    function streamingRenderMarkdown(text) {
        if (!text) return '';
        try {
            const preprocessed = closeOpenFences(text);
            const raw = window.marked.parse(preprocessed);
            // 在 DOMPurify 之前注入 hljs 高亮 token，让代码块实时带颜色
            const highlighted = highlightCodeInHTML(raw);
            return window.DOMPurify.sanitize(highlighted, {
                ADD_ATTR: ['class'],
                FORBID_TAGS: ['style', 'iframe', 'script'],
            });
        } catch (err) {
            console.error('流式 Markdown 渲染失败', err);
            return escapeHTML(text);
        }
    }

    // ---- Markdown 渲染 ----
    // 顺序：marked.parse → DOMPurify.sanitize；hljs 不在字符串阶段处理，
    // 避免 DOMPurify 误伤 hljs 的 token span。XSS 防护 + 语法高亮职责分离。
    function renderMarkdown(text) {
        if (!text) return '';
        try {
            const raw = window.marked.parse(text);
            return window.DOMPurify.sanitize(raw, {
                // 保留 class：hljs 依赖 class="hljs-keyword" 等做样式
                ADD_ATTR: ['class'],
                // 显式禁止高危标签，<img onerror> 等通过 on* 过滤默认就拦了
                FORBID_TAGS: ['style', 'iframe', 'script'],
            });
        } catch (err) {
            console.error('Markdown 渲染失败', err);
            return escapeHTML(text);
        }
    }

    // ---- 代码块增强（高亮 + 语言标签 + 复制 + JSON 校验） ----
    // 仅在 bubble.innerHTML 赋值后调用一次。流式响应过程中不调用（避免半截代码闪烁）。

    // 解析 ``` 围栏代码块的语言；无则返回 'plain'。
    function extractCodeLang(codeEl) {
        const m = (codeEl.className || '').match(/language-([\w+-]+)/);
        return m ? m[1].toLowerCase() : 'plain';
    }

    // 给单个 <pre> 节点注入顶部 header（语言标签 + 复制按钮）。
    // 全程 createElement，零字符串拼接，避免二次 XSS。
    function buildCodeHeader(lang, codeEl) {
        const header = document.createElement('div');
        header.className = 'code-block-header';

        const langLabel = document.createElement('span');
        langLabel.className = 'code-lang';
        langLabel.textContent = lang;
        header.appendChild(langLabel);

        const copyBtn = document.createElement('button');
        copyBtn.type = 'button';
        copyBtn.className = 'copy-btn';
        copyBtn.textContent = 'Copy';
        copyBtn.title = '复制代码';
        copyBtn.addEventListener('click', () => copyCode(codeEl, copyBtn));
        header.appendChild(copyBtn);

        return header;
    }

    // 异步复制代码块原文到剪贴板。优先用 navigator.clipboard，
    // http/老浏览器不可用时回退到 execCommand('copy') + 临时 textarea。
    async function copyCode(codeEl, btnEl) {
        // dataset.raw 保留的是 hljs 高亮之前的原文
        const text = codeEl.dataset.raw || codeEl.textContent || '';
        let ok = false;
        try {
            if (navigator.clipboard && navigator.clipboard.writeText) {
                await navigator.clipboard.writeText(text);
                ok = true;
            }
        } catch (err) {
            console.warn('clipboard API 失败，回退 execCommand', err);
        }
        if (!ok) {
            const ta = document.createElement('textarea');
            ta.value = text;
            ta.setAttribute('readonly', '');
            ta.style.position = 'fixed';
            ta.style.top = '0';
            ta.style.left = '0';
            ta.style.opacity = '0';
            document.body.appendChild(ta);
            ta.select();
            try { document.execCommand('copy'); } catch (err) {
                console.error('execCommand copy 失败', err);
            } finally { ta.remove(); }
        }
        btnEl.textContent = 'Copied';
        btnEl.classList.add('is-copied');
        setTimeout(() => {
            btnEl.textContent = 'Copy';
            btnEl.classList.remove('is-copied');
        }, 1500);
    }

    // 对 ```json 块做格式校验。
    // 成功：在 header 末尾追加 .json-valid 角标。
    // 失败：在 pre 后追加 .json-error 条，文字含行/列信息，不替换高亮。
    function validateJsonBlock(codeEl, preEl) {
        const raw = codeEl.dataset.raw || '';
        if (!raw.trim()) return;
        try {
            JSON.parse(raw);
            const badge = document.createElement('span');
            badge.className = 'json-valid';
            badge.textContent = '✓ valid';
            const header = preEl.querySelector('.code-block-header');
            if (header) header.appendChild(badge);
        } catch (err) {
            // V8 / SpiderMonkey 的 JSON.parse 错误信息形如：
            //   "Unexpected token } in JSON at position 22"
            // 从中抽 position 算 row/col。位置不可得时回退到 "未知位置"。
            const posMatch = (err && err.message || '').match(/position\s+(\d+)/i);
            let row = 1, col = 1;
            if (posMatch) {
                const pos = Number(posMatch[1]);
                const before = raw.slice(0, pos);
                row = before.split('\n').length;
                col = pos - (before.lastIndexOf('\n'));
            } else {
                row = 0; col = 0;
            }
            const bar = document.createElement('div');
            bar.className = 'json-error';
            bar.dataset.row = String(row);
            bar.dataset.col = String(col);
            const posText = row > 0 ? `第 ${row} 行 第 ${col} 列` : '';
            bar.textContent = posText
                ? `JSON 错误 · ${posText} · ${err && err.message ? err.message : String(err)}`
                : `JSON 错误 · ${err && err.message ? err.message : String(err)}`;
            preEl.parentNode.insertBefore(bar, preEl.nextSibling);
        }
    }

    // enhanceCodeBlocks 是代码块增强的总入口。
    // 关键时序：先 dataset.raw 存原文（hljs 会改 textContent），再高亮，再 JSON 校验。
    function enhanceCodeBlocks(bubbleEl) {
        if (!bubbleEl) return;
        const blocks = bubbleEl.querySelectorAll('pre > code');
        if (!blocks.length) return;
        for (const codeEl of blocks) {
            const preEl = codeEl.parentElement;
            if (!preEl) continue;
            const lang = extractCodeLang(codeEl);

            // 关键：在 hljs.highlightElement 之前保存原文
            codeEl.dataset.raw = codeEl.textContent;

            preEl.classList.add('code-block');
            preEl.appendChild(buildCodeHeader(lang, codeEl));

            // hljs.highlightElement 找不到 language 时不强行加 hljs 类（fallback plain）
            if (lang !== 'plain' && window.hljs) {
                try {
                    window.hljs.highlightElement(codeEl);
                } catch (err) {
                    console.warn('hljs.highlightElement 失败', err);
                }
            }
            if (lang === 'json') {
                validateJsonBlock(codeEl, preEl);
            }
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
