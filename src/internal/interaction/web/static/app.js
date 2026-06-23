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
        // Step 5：权限模式状态栏展示 + 切换下拉
        permMode:       $('perm-mode'),
        permStat:       $('perm-stat'),
        permDropdown:   $('perm-mode-dropdown'),
        // Step 4：System Prompt 可观测性
        spTokens:       $('sp-tokens'),
        spBreakdown:    $('sp-breakdown'),
        // Step 8：MCP 健康状态
        mcpStat:        $('mcp-stat'),
        mcpSummary:     $('mcp-summary'),
        mcpDots:        $('mcp-dots'),
        mcpTooltip:     $('mcp-tooltip'),
        // Step 7：压缩按钮 + 计数 + toast
        compactBtn:     $('compact-btn'),
        compactValue:   $('compact-stat-value'),
        toastContainer: $('toast-container'),
        // Step 4：开发者模式面板
        devPanel:       $('dev-panel'),
        devExportBtn:   $('dev-export-sp-btn'),
        devPanelClose:  $('dev-panel-close'),
        // Step 4：SP 导出模态框
        spModal:        $('sp-modal'),
        spModalSummary: $('sp-modal-summary'),
        spModalSystem:  $('sp-modal-system'),
        spModalLead:    $('sp-modal-lead'),
        spModalStats:   $('sp-modal-stats'),
        // 亮色 / 暗色 主题切换按钮：紧贴就绪状态右侧，点击翻转 data-theme
        themeToggle:    $('theme-toggle'),
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
        _memoryReviewById: {},         // review_id -> DOM 节点，用于自动记忆事件定位
        // Step 1.4：file_diff 单次回包按 tool_use_id 路由到对应弹窗回调。
        // 每个 callback 自行处理「找到/没找到/超时」，处理完即从 map 移除。
        _fileDiffCallbacks: {},        // tool_use_id -> { resolve, timer, modal }
        // Step 5：HITL 权限确认弹窗状态
        _permModal: null,              // 当前打开的权限确认弹窗 DOM 元素
        _permQueue: [],                // 排队等待的权限请求队列（FIFO）
        _permCountdownTimer: null,     // 倒计时定时器
        // Step 7：累计第一层轻量替换的工具结果数（状态栏小标记，切换会话重置）
        compactLightCount: 0,
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
        { cmd: '/compact',       desc: '手动压缩上下文（历史摘要化）',
          exec: () => sendWS(MsgType.Compact, {}) },
        { cmd: '/dump',          desc: '导出当前会话上下文与 System Prompt 到本地文件（dump.json/dump.md）',
          exec: () => sendWS(MsgType.Dump, {}) },
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
        DevExportSP:       'dev_export_sp',
        // Step 1.4：查看改动弹窗用
        GetFileDiff:       'get_file_diff',
        FileDiff:          'file_diff',
        // Step 5：权限确认 HITL
        PermissionRequest: 'permission_request',
        PermissionResponse: 'permission_response',
        PermissionMode:    'permission_mode',
        // Step 5 增强：用户主动切换权限模式（状态栏下拉）
        SetPermissionMode: 'set_permission_mode',
        // Step 8：MCP server 健康状态推送
        MCPStatus:        'mcp_status',
        // Step 7：上下文压缩（手动触发 + 事件推送）
        Compact:          'compact',
        CompactionEvent:  'compaction_event',
        MemoryReviewEvent: 'memory_review_event',
        // /dump：导出当前会话上下文 + System Prompt 到本地文件（dump.json/dump.md）
        Dump:             'dump',
        DumpResult:       'dump_result',
    };

    // abortMarker 与后端 agent_loop.go 中的 abortMarker 常量保持一致，
    // 用于用户主动取消回复时的视觉标记文本
    const abortMarker = '[用户取消了回复]';

    // escapeHtml 把字符串中的 & < > " ' 转义为 HTML 实体，避免
    // 把后端返回的 Source 名称当作 HTML 解释（XSS 防护）。
    function escapeHtml(s) {
        return String(s)
            .replace(/&/g, '&amp;')
            .replace(/</g, '&lt;')
            .replace(/>/g, '&gt;')
            .replace(/"/g, '&quot;')
            .replace(/'/g, '&#39;');
    }

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
            case MsgType.DevExportSP:     return onDevExportSP(msg.payload);
            case MsgType.FileDiff:          return onFileDiff(msg.payload);
            case MsgType.PermissionRequest: return onPermissionRequest(msg.payload);
            case MsgType.PermissionMode:    return onPermissionMode(msg.payload);
            case MsgType.MCPStatus:         return onMCPStatus(msg.payload);
            case MsgType.CompactionEvent:   return onCompactionEvent(msg.payload);
            case MsgType.MemoryReviewEvent: return onMemoryReviewEvent(msg.payload);
            case MsgType.DumpResult:        return onDumpResult(msg.payload);
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
        // 用户主动取消时，为最后一条 assistant 消息添加取消视觉标记
        if (p?.reason === 'aborted') {
            const lastMsg = state.messages[state.messages.length - 1];
            if (lastMsg && lastMsg.role === 'assistant') {
                const chatArea = document.getElementById('chat-area');
                const lastBubble = chatArea?.querySelector('.message-wrap:last-child .message-bubble');
                if (lastBubble) {
                    lastBubble.innerHTML = renderMarkdown(abortMarker);
                    lastBubble.classList.add('message-aborted');
                    enhanceCodeBlocks(lastBubble);
                }
            }
        }
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
        // Step 7：切换会话重置轻量压缩计数（属于上一会话的统计）
        state.compactLightCount = 0;
        renderCompactStat();
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
        // Step 4：System Prompt token 可观测性
        renderSPInfo(p);
    }

    // ---- Step 4: System Prompt 可观测性 ----

    // renderSPInfo 渲染状态栏 SP 区域 + 4 层小计 tooltip。
    //
    // 行为：
    //   1. 显示 SP 总 token 数（sp_total_tokens），0 时显示 "--"
    //   2. 鼠标悬停时弹出 4 行小计（按 Source 顺序展示）
    function renderSPInfo(p) {
        if (!dom.spTokens) return;
        const total = p.sp_total_tokens || 0;
        dom.spTokens.textContent = total > 0 ? formatTokenCount(total) : '--';

        if (!dom.spBreakdown) return;
        const breakdown = Array.isArray(p.sp_breakdown) ? p.sp_breakdown : [];
        if (breakdown.length === 0) {
            dom.spBreakdown.innerHTML = '<div class="sp-breakdown-row"><span class="sp-breakdown-name">（无）</span></div>';
            return;
        }
        const rows = breakdown.map(s => {
            const name = escapeHtml(s.name || '');
            const tokens = s.tokens || 0;
            return `<div class="sp-breakdown-row"><span class="sp-breakdown-name">${name}</span><span class="sp-breakdown-tokens">${formatTokenCount(tokens)}</span></div>`;
        }).join('');
        dom.spBreakdown.innerHTML = rows +
            `<div class="sp-breakdown-row sp-breakdown-total"><span class="sp-breakdown-name">total</span><span class="sp-breakdown-tokens">${formatTokenCount(total)}</span></div>`;
    }

    // formatTokenCount 把 1500 显示为 "1.5k"，15000 显示为 "15k"；
    // 1000 以下原样输出。状态栏空间有限，需要紧凑表示。
    function formatTokenCount(n) {
        if (typeof n !== 'number' || n <= 0) return '0';
        if (n < 1000) return String(n);
        if (n < 10000) return (n / 1000).toFixed(1) + 'k';
        return Math.round(n / 1000) + 'k';
    }

    // onDevExportSP 处理后端推送的 dev_export_sp 响应，渲染到模态框中。
    function onDevExportSP(p) {
        if (!p) return;
        const total = p.total_tokens || 0;
        const blocks = Array.isArray(p.system_blocks) ? p.system_blocks : [];
        const stats = Array.isArray(p.stats) ? p.stats : [];
        const lead = p.lead_user_message || '';

        if (dom.spModalSummary) {
            dom.spModalSummary.innerHTML = `共 <strong>${blocks.length}</strong> 段 system 字段 · <strong>${total}</strong> tokens${lead ? ' · 含首条 user 消息' : ''}`;
        }
        if (dom.spModalSystem) {
            dom.spModalSystem.textContent = blocks.length > 0
                ? blocks.map((b, i) => `--- [${i + 1}] ---\n${b}`).join('\n\n')
                : '（空）';
        }
        if (dom.spModalLead) {
            dom.spModalLead.textContent = lead || '（空）';
        }
        if (dom.spModalStats) {
            dom.spModalStats.textContent = stats.length > 0
                ? stats.map(s => `${s.name}: ${s.tokens}`).join('\n')
                : '（空）';
        }
        if (dom.spModal) {
            dom.spModal.hidden = false;
        }
    }

    // 关闭 SP 模态框（点击 backdrop 或关闭按钮触发）
    function closeSPModal() {
        if (dom.spModal) {
            dom.spModal.hidden = true;
        }
    }

    // toggleDevPanel 切换开发者面板显示。
    function toggleDevPanel(force) {
        if (!dom.devPanel) return;
        const next = (force === undefined) ? !dom.devPanel.hidden : force;
        dom.devPanel.hidden = !next;
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
        const node = appendToolStartNode(p.tool_use_id, p.name, p.input, p.started_at, p.server);
        state._toolById[p.tool_use_id] = node;
        scrollToBottomIfNeeded();
    }

    function onToolCallEnd(p) {
        if (!p || !p.tool_use_id) return;
        let node = state._toolById[p.tool_use_id];
        if (!node) {
            // 异常路径：end 先到 start 后到（或 start 丢失），按已完成态直接插入
            node = appendToolStartNode(p.tool_use_id, p.name, '', p.started_at, p.server);
            state._toolById[p.tool_use_id] = node;
        }
        updateToolEndNode(node, p);
        scrollToBottomIfNeeded();
    }

    // =========================================================================
    // Step 1.4：文件改动预览弹窗
    // =========================================================================
    // 触发链路：「查看改动」按钮 → openFileDiffModal(toolUseId)
    //   → ws 发 get_file_diff → 收到 file_diff → onFileDiff 路由到对应回调
    //   → 渲染双栏 diff（或显示 reason 文案）
    //
    // 设计要点：
    //   1. 每个弹窗独立 callback map（state._fileDiffCallbacks）→ 互不串扰
    //   2. 10 秒超时：定时器到点未回包 → 显示"请求超时"
    //   3. 关闭路径统一：closeFileDiffModal 负责清定时器 + 移 DOM + 解绑全局 Esc
    //   4. DOM 全程 createElement（XSS 防护），只有 hljs 高亮后的 innerHTML 走 DOMPurify

    // 拉取文件 diff 的超时时间（10s）。进程内 FileDiffStore 是内存数据，
    // 拉取为本地查表动作，正常应在毫秒级返回；10s 已经是非常宽裕的兜底。
    const FILE_DIFF_REQUEST_TIMEOUT_MS = 10000;

    // reason 字段 → 用户可读文案。统一收口便于文案调整。
    const FILE_DIFF_REASON_TEXT = {
        not_found: '暂无改动预览（可能进程已重启，或该调用为旧会话）',
        too_large: '文件改动过大（> 2 MB），已放弃预览',
    };

    // onFileDiff 收到后端的 file_diff 响应，路由到对应 tool_use_id 的回调。
    // 找不到回调时（已超时/已关闭）静默丢弃，避免回包顺序错乱时的控制台噪音。
    function onFileDiff(p) {
        if (!p || !p.tool_use_id) return;
        const cb = state._fileDiffCallbacks[p.tool_use_id];
        if (!cb) return;
        // 清理 10s 定时器（必须在 delete 之前取消，否则回调里再 close 时会重复清理）
        if (cb.timer) {
            clearTimeout(cb.timer);
        }
        delete state._fileDiffCallbacks[p.tool_use_id];
        try {
            cb.resolve(p);
        } catch (err) {
            console.error('file_diff 回调执行失败', err);
        }
    }

    // openFileDiffModal 打开指定 tool_use_id 的文件改动预览弹窗。
    // 流程：
    //   1. 校验 + 构造 modal DOM（loading 态）插入 body
    //   2. 注册 Esc 关闭、点击 backdrop 关闭、关闭按钮关闭
    //   3. ws 发 get_file_diff；10s 超时显示错误文案
    //   4. 收到回包后渲染双栏 diff 或显示 reason
    function openFileDiffModal(toolUseId) {
        if (!toolUseId) {
            console.warn('openFileDiffModal: toolUseId 为空');
            return;
        }
        // 避免同一 tool_use_id 并发打开多个弹窗
        if (state._fileDiffCallbacks[toolUseId]) {
            console.warn('openFileDiffModal: 已存在同 ID 的弹窗，忽略重复请求', toolUseId);
            return;
        }

        const modal = buildDiffModalSkeleton(toolUseId);
        document.body.appendChild(modal);
        const body = modal.querySelector('.diff-modal-body');

        // 显示 loading 态
        renderDiffMessage(body, '正在加载改动预览…', /*error=*/false);

        // 绑定关闭路径
        const escHandler = (ev) => {
            if (ev.key === 'Escape') {
                closeFileDiffModal(modal, toolUseId);
            }
        };
        document.addEventListener('keydown', escHandler);
        modal._escHandler = escHandler;

        modal.querySelectorAll('[data-diff-modal-close]').forEach(el => {
            el.addEventListener('click', () => closeFileDiffModal(modal, toolUseId));
        });

        // 注册回调 + 定时器
        let resolveCb;
        const promise = new Promise((resolve) => { resolveCb = resolve; });
        const timer = setTimeout(() => {
            const cb = state._fileDiffCallbacks[toolUseId];
            if (!cb) return;
            delete state._fileDiffCallbacks[toolUseId];
            renderDiffMessage(body, '请求超时（> 10s）未收到响应', /*error=*/true);
        }, FILE_DIFF_REQUEST_TIMEOUT_MS);
        state._fileDiffCallbacks[toolUseId] = { resolve: resolveCb, timer, modal };

        // 发送查询
        const sent = sendWS(MsgType.GetFileDiff, { tool_use_id: toolUseId });
        if (!sent) {
            // WS 未连接：立刻清理
            clearTimeout(timer);
            delete state._fileDiffCallbacks[toolUseId];
            renderDiffMessage(body, '与 CodePilot 的连接已断开，请稍后重试', /*error=*/true);
            return;
        }

        // 异步渲染：resolve 后根据 payload 渲染
        promise.then(payload => {
            // 弹窗可能在等待期间被关闭（用户按 Esc / 点击遮罩）
            if (!modal.isConnected) return;
            if (payload.tool_use_id !== toolUseId) {
                // 极小概率：服务端回串了别的 ID（理论上不会发生），按兜底处理
                renderDiffMessage(body, '响应与请求不匹配，请稍后重试', /*error=*/true);
                return;
            }
            if (!payload.found) {
                const reason = payload.reason || 'not_found';
                const text = FILE_DIFF_REASON_TEXT[reason] || `未找到改动预览（reason=${reason}）`;
                renderDiffMessage(body, text, /*error=*/true);
                return;
            }
            renderDiffGrid(body, payload);
        });
    }

    // closeFileDiffModal 关闭弹窗：清回调 + 定时器 + DOM + 全局 Esc。
    // 不重复清理：clearTimeout / delete 重复调用是 no-op。
    function closeFileDiffModal(modal, toolUseId) {
        if (!modal) return;
        if (modal._escHandler) {
            document.removeEventListener('keydown', modal._escHandler);
            modal._escHandler = null;
        }
        const cb = state._fileDiffCallbacks[toolUseId];
        if (cb) {
            if (cb.timer) clearTimeout(cb.timer);
            delete state._fileDiffCallbacks[toolUseId];
        }
        if (modal.parentNode) modal.parentNode.removeChild(modal);
    }

    // buildDiffModalSkeleton 构造弹窗骨架。文件路径/工具名从工具块头部取。
    function buildDiffModalSkeleton(toolUseId) {
        const modal = document.createElement('div');
        modal.className = 'diff-modal';
        modal.dataset.toolUseId = toolUseId;
        modal.setAttribute('role', 'dialog');
        modal.setAttribute('aria-modal', 'true');
        modal.setAttribute('aria-label', '文件改动预览');

        // 从工具块头部取文件名 / 工具名（让用户一眼知道是哪次调用的改动）
        const toolNode = state._toolById[toolUseId];
        let filePath = '';
        let toolName = '';
        if (toolNode) {
            const summaryEl = toolNode.querySelector('.message-tool-summary');
            const nameEl = toolNode.querySelector('.message-tool-name');
            filePath = (summaryEl?.textContent || '').trim();
            toolName = (nameEl?.textContent || '').trim();
        }

        // backdrop 与 inner 都在同一节点下：inner 上的点击不能冒泡到 modal 关闭
        modal.innerHTML = `
            <div class="diff-modal-inner" role="document">
                <div class="diff-modal-header">
                    <span class="diff-modal-filename" title="${escapeHTML(filePath || toolUseId)}">${escapeHTML(filePath || toolUseId)}</span>
                    <span class="diff-modal-toolname">${escapeHTML(toolName || 'Diff')}</span>
                    <span class="diff-modal-spacer"></span>
                    <button class="diff-modal-close" type="button" data-diff-modal-close title="关闭 (Esc)">×</button>
                </div>
                <div class="diff-modal-body"></div>
            </div>
        `;
        // 点击 .diff-modal 自身（即遮罩空白）关闭；inner 内部点击不触发。
        // 通过 capture 阶段拦截，点击 inner 时不冒泡到 modal 自身
        // （DOM 上 modal 是 inner 的父，inner 事件不会冒泡到 modal？会的）
        // 正确做法：在 inner 上加 stopPropagation
        const inner = modal.querySelector('.diff-modal-inner');
        if (inner) {
            inner.addEventListener('click', (ev) => ev.stopPropagation());
        }
        modal.addEventListener('click', (ev) => {
            if (ev.target === modal) {
                closeFileDiffModal(modal, toolUseId);
            }
        });
        return modal;
    }

    // renderDiffMessage 在 body 区域显示一行消息（loading / 错误 / 空态）。
    function renderDiffMessage(body, text, isError) {
        body.innerHTML = '';
        const msg = document.createElement('div');
        msg.className = 'diff-modal-message' + (isError ? ' error' : '');
        msg.textContent = text;
        body.appendChild(msg);
    }

    // renderDiffGrid 在 body 区域构建双栏 diff。
    // 输入为 FileDiffPayload（含 before / after / language）。
    //
    // 渲染策略（自上而下三层）：
    //   1. 行级 diff：dmp.diff_linesToChars_ + diff_main + diff_cleanupEfficiency
    //      （不调 diff_cleanupSemantic，它在小改动上倾向过度合并，会把"明显不同"
    //      的多行强行包成一个 op，导致上下文错位；Efficiency 仅合并不经济的碎片，
    //      行边界更稳定）
    //   2. 双栏排版：把 [ctx|add|del] 块拆成单行，按行号递增左右两栏同步
    //      - ctx 行 → 双栏都画
    //      - del 行 → 仅左栏（红底）
    //      - add 行 → 仅右栏（绿底）
    //   3. 行内 inline word diff：对成对 (del, add) 行做字符级 diff_main，
    //      在 del 行内包裹 <span class="diff-word-del">…</span> 标红删除线，
    //      在 add 行内包裹 <span class="diff-word-add">…</span> 标绿
    //      - changed 行（del/add）使用纯文本 + 词级高亮（避免 hljs 跨 span 干扰 diff 标记）
    //      - unchanged 行（ctx）保留完整 hljs 语法高亮（赏心悦目）
    //      配对策略：连续 del 块与紧随其后的连续 add 块按行数两两配对 min(dels, adds)，
    //      剩余单边行保持原状
    function renderDiffGrid(body, payload) {
        body.innerHTML = '';
        const grid = document.createElement('div');
        grid.className = 'diff-grid';

        const left = document.createElement('div');
        left.className = 'diff-side';
        const right = document.createElement('div');
        right.className = 'diff-side';

        const leftLabel = document.createElement('div');
        leftLabel.className = 'diff-side-label';
        leftLabel.textContent = 'Before';
        left.appendChild(leftLabel);

        const rightLabel = document.createElement('div');
        rightLabel.className = 'diff-side-label';
        rightLabel.textContent = 'After';
        right.appendChild(rightLabel);

        // 行级 diff：dmp 在 window 全局上（vendor/diff-match-patch.min.js UMD 暴露）
        const dmp = window.diff_match_patch;
        const before = payload.before || '';
        const after = payload.after || '';
        const language = payload.language || '';

        // 原文按行切分（pop 末尾空行：和 rows 拆行策略保持一致）
        // 关键：rows 拆行时也 pop 末尾空，所以这里必须同步 pop，否则 cursor 索引错位
        const beforeRawLines = splitLines(before);
        const afterRawLines = splitLines(after);

        // 整体高亮 Before/After 全文（一次调用，O(n)），仅 ctx 行使用
        // 高亮失败时回退纯文本，由 escapeHTML 注入保证 XSS 安全。
        const beforeHL = splitHighlightLines(highlightCode(before, language));
        const afterHL = splitHighlightLines(highlightCode(after, language));

        // 计算行级 diff
        let rows = []; // 每项 { op: 'eq'|'add'|'del', text }
        try {
            if (!dmp) {
                // 极端情况：vendor 没加载到；按整段当作 equal 行处理
                rows = [{ op: 'eq', text: before }];
                if (after && after !== before) {
                    rows.push({ op: 'del', text: before });
                    rows.push({ op: 'add', text: after });
                }
            } else {
                const d = new dmp();
                // 用 diff_linesToChars_ 把多行文本先按行压缩为单字符，
                // 再走 diff_main，这是官方推荐的大文本做法（速度远好于直接对长字符串做 diff）。
                const a = d.diff_linesToChars_(before, after);
                const diffs = d.diff_main(a.chars1, a.chars2, false);
                d.diff_charsToLines_(diffs, a.lineArray);
                // 改用 diff_cleanupEfficiency（仅合并不经济碎片），不用 diff_cleanupSemantic。
                // Semantic 在小改动上倾向过度合并，会把多行不相干的修改包成同一个 op，
                // 反而让 del/add 块边界错位。Efficiency 保守且行边界稳定。
                d.diff_cleanupEfficiency(diffs);
                // 注意：diff-match-patch 的 Diff 类型是普通对象 {0:op, 1:text}，
                // 不是数组，不可迭代，不能用 [op, text] 解构。必须用 d[0]/d[1] 访问。
                rows = diffs.map(d => ({
                    op: d[0] === 0 ? 'eq' : (d[0] > 0 ? 'add' : 'del'),
                    text: d[1],
                }));
            }
        } catch (err) {
            console.error('diff 计算失败', err);
            rows = [{ op: 'eq', text: before }];
        }

        // 拆行渲染：保持左右栏视觉上的"行对齐"
        let leftLine = 0;   // Before 栏行号
        let rightLine = 0;  // After 栏行号
        const leftRows = [];   // Before 栏行
        const rightRows = [];  // After 栏行

        for (const r of rows) {
            // split('\n') 切完后末尾会多一个空字符串（"a\nb\n" → ["a","b",""]），
            // 需要去掉最后一个以避免每段都多出一行空行
            const lines = r.text.split('\n');
            if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop();
            if (lines.length === 0) continue;

            for (const line of lines) {
                if (r.op === 'eq') {
                    leftLine += 1;
                    rightLine += 1;
                    leftRows.push({ lineNo: leftLine, cls: 'ctx' });
                    rightRows.push({ lineNo: rightLine, cls: 'ctx' });
                } else if (r.op === 'del') {
                    leftLine += 1;
                    leftRows.push({ lineNo: leftLine, cls: 'del' });
                    rightRows.push({ lineNo: 0, cls: 'empty' });
                } else { // 'add'
                    rightLine += 1;
                    leftRows.push({ lineNo: 0, cls: 'empty' });
                    rightRows.push({ lineNo: rightLine, cls: 'add' });
                }
            }
        }

        // 把原文行号 → 高亮 HTML 的对应行做映射
        // 关键：beforeHL 的每一项对应 beforeRawLines 同一索引（已 pop 末尾空）
        // 即：第 N 个非 empty 行 → beforeHL[N-1]（0-based 索引）
        const leftHL = pickHighlightByIndex(leftRows, beforeHL, /*hasContent=*/r => r.cls !== 'empty');
        const rightHL = pickHighlightByIndex(rightRows, afterHL, /*hasContent=*/r => r.cls !== 'empty');

        // 行内 inline word diff：对成对 (del, add) 行做字符级 diff。
        // 算法：
        //   - 扫 leftRows / rightRows，连续的 del 块和紧随其后的连续 add 块视为"成对修改段"
        //   - 段内按行数两两配对（min(del 数, add 数)）
        //   - 配对的行用 dmp.diff_main(text1, text2, false) 做字符级 diff
        //   - 把 [op, text] 渲染为：op=-1 标 .diff-word-del；op=1 标 .diff-word-add；op=0 原样
        //   - changed 行用纯文本（escapeHTML）渲染，避免 hljs 跨 span 与 inline diff 嵌套
        //   - 剩余单边行（多出 del 或多出 add）保持原样
        applyInlineWordDiff(leftRows, rightRows, leftHL, rightHL, dmp);

        // 渲染
        for (let i = 0; i < leftRows.length; i++) {
            const row = leftRows[i];
            left.appendChild(buildDiffLine(row.lineNo, row.cls, leftHL[i] || '', row.hasInline));
        }
        for (let i = 0; i < rightRows.length; i++) {
            const row = rightRows[i];
            right.appendChild(buildDiffLine(row.lineNo, row.cls, rightHL[i] || '', row.hasInline));
        }

        grid.appendChild(left);
        grid.appendChild(right);
        body.appendChild(grid);
    }

    // applyInlineWordDiff 就地修改 leftHL / rightHL 数组：
    //   - 对成对 (del, add) 行所在位置，原纯文本 / hljs 字符串替换为带 .diff-word-*
    //     包裹的 HTML 片段（行内 inline diff）
    //   - 配对行同时修改 row 标记（添加 .has-inline-diff 类），CSS 可据此加左侧色条
    //
    // 入参说明：
    //   - leftRows / rightRows：renderDiffGrid 内部已算好的行元数据
    //   - leftHL / rightHL：与 leftRows/rightRows 等长的"内容 HTML"数组（ctx 行用 hljs，初始全用 hljs）
    //   - dmp：window.diff_match_patch 构造器；为 null 时跳过 inline diff（保留原 hljs 染色）
    function applyInlineWordDiff(leftRows, rightRows, leftHL, rightHL, dmp) {
        if (!dmp) return;
        const n = leftRows.length;
        let i = 0;
        while (i < n) {
            // 跳过 ctx / empty 行
            if (leftRows[i].cls !== 'del') { i++; continue; }
            // 找连续的 del 块结束位置
            const delStart = i;
            let delEnd = i;
            while (delEnd < n && leftRows[delEnd].cls === 'del') delEnd++;
            // 找紧随其后的连续 add 块
            let addStart = delEnd;
            let addEnd = addStart;
            while (addEnd < n && rightRows[addEnd].cls === 'add') addEnd++;
            // delEnd 之前都在左栏 del 段；addStart..addEnd-1 是右栏 add 段
            const delCount = delEnd - delStart;
            const addCount = addEnd - addStart;
            const pairCount = Math.min(delCount, addCount);
            // 配对的行做 inline diff
            for (let k = 0; k < pairCount; k++) {
                const lIdx = delStart + k;
                const rIdx = addStart + k;
                // 行内词级 diff 用原文（去 hljs 染色）做：先 escape 再 diff 再包 span
                const beforeText = stripHTML(leftHL[lIdx] || '');
                const afterText = stripHTML(rightHL[rIdx] || '');
                // 内容相同的纯 del/add 行不浪费 inline 计算
                if (beforeText === afterText) continue;
                const d = new dmp();
                const charDiffs = d.diff_main(beforeText, afterText, false);
                // diff_cleanupSemantic 会按"标点/空白"切分，让 diff 块贴近单词边界
                d.diff_cleanupSemantic(charDiffs);
                leftHL[lIdx] = renderInlineDiff(charDiffs, /*side=*/'del');
                rightHL[rIdx] = renderInlineDiff(charDiffs, /*side=*/'add');
                // 给对应 row 加 has-inline-diff 类，CSS 可选地用色条强调
                leftRows[lIdx].hasInline = true;
                rightRows[rIdx].hasInline = true;
            }
            // 跳到 add 块末尾继续
            i = Math.max(delEnd, addEnd);
        }
    }

    // stripHTML 去除 HTML 标签，仅保留文本内容。用于把 hljs 高亮后的 HTML 转回原文做 inline diff。
    // 实现：单个正则匹配 <...> 非贪婪替换为空；HTML 实体原样保留（与 inline diff 字符串内容一致即可）。
    // 注意：hljs 高亮的 HTML 不含 <script> 等危险标签（vendor 是 trusted），这里只是去标签不做安全转义。
    function stripHTML(html) {
        if (!html) return '';
        return html.replace(/<[^>]*>/g, '');
    }

    // renderInlineDiff 把字符级 dmp diff 渲染为带 .diff-word-* 包裹的 HTML。
    // side='del'：保留 DEL 与 EQUAL，INS 丢弃（Before 不显示新增部分）
    // side='add'：保留 INS 与 EQUAL，DEL 丢弃（After 不显示删除部分）
    // 输出字符串可直接 innerHTML 注入：所有内容都经过 escapeHTML 处理，无脚本风险。
    function renderInlineDiff(charDiffs, side) {
        const parts = [];
        for (let i = 0; i < charDiffs.length; i++) {
            const op = charDiffs[i][0];
            const text = charDiffs[i][1];
            if (op === 0) {
                // 公共部分：原样
                parts.push(escapeHTML(text));
            } else if (op === -1) {
                // DEL：仅在 Before 侧保留
                if (side === 'del') {
                    parts.push('<span class="diff-word-del">' + escapeHTML(text) + '</span>');
                }
            } else { // op === 1
                // INS：仅在 After 侧保留
                if (side === 'add') {
                    parts.push('<span class="diff-word-add">' + escapeHTML(text) + '</span>');
                }
            }
        }
        return parts.join('');
    }

    // splitLines 把字符串按 \n 切分并 pop 末尾空字符串，与 renderDiffGrid
    // 内部拆行策略保持一致；为外部组件（如单元测试）提供稳定接口。
    function splitLines(text) {
        if (!text) return [];
        const lines = text.split('\n');
        if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop();
        return lines;
    }

    // splitHighlightLines 对高亮后的 HTML 字符串按 \n 切分。
    // 简化策略：hljs 对代码块几乎都是逐行 token 化，跨行 span 极少；
    // 即便出现跨行 tag，视觉上只会"掉色"一行，不影响 diff 行级准确性。
    function splitHighlightLines(html) {
        if (!html) return [];
        const lines = html.split('\n');
        if (lines.length > 0 && lines[lines.length - 1] === '') lines.pop();
        return lines;
    }

    // pickHighlightByIndex 按"非 empty 行序号"取 hljsLines 的对应行。
    // 同步 pop 末尾空后，rows 非 empty 数量 === hljsLines 数量，按 cursor 取即可。
    function pickHighlightByIndex(rows, hljsLines, hasContent) {
        const result = new Array(rows.length).fill('');
        let cursor = 0;
        for (let i = 0; i < rows.length; i++) {
            if (!hasContent(rows[i])) {
                result[i] = '';
                continue;
            }
            if (cursor < hljsLines.length) {
                result[i] = hljsLines[cursor];
            } else {
                // 越界兜底：rows 多了 hljsLines 少了（极端），用空字符串
                result[i] = '';
            }
            cursor += 1;
        }
        return result;
    }

    // buildDiffLine 构造单行 DOM：行号 + 内容（内容为已高亮 HTML）。
    // 内容直接用 innerHTML 注入：来自 highlightCode（hljs / escapeHTML），无脚本风险。
    // 兼容 hasInline 标记：applyInlineWordDiff 设置后，给行加 .diff-line-has-inline 类，
    // CSS 可据此加深左侧色条，视觉上强调"这行有行内词级高亮"。
    function buildDiffLine(lineNo, cls, contentHTML, hasInline) {
        const row = document.createElement('div');
        let className = 'diff-line diff-line-' + cls;
        if (hasInline) className += ' diff-line-has-inline';
        row.className = className;
        const num = document.createElement('span');
        num.className = 'diff-line-num';
        num.textContent = lineNo > 0 ? String(lineNo) : '';
        const content = document.createElement('span');
        content.className = 'diff-line-content';
        content.innerHTML = contentHTML || '';
        row.appendChild(num);
        row.appendChild(content);
        return row;
    }

    // highlightCode 对一段文本做 hljs 高亮。未识别语言（空）走 escapeHTML，
    // 返回纯文本，调用方通过 innerHTML 注入仍安全。
    function highlightCode(text, language) {
        if (!text) return '';
        if (language && window.hljs) {
            try {
                const result = window.hljs.highlight(text, { language, ignoreIllegals: true });
                return result.value;
            } catch (err) {
                console.warn('hljs 高亮失败，回退纯文本', err);
            }
        }
        return escapeHTML(text);
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
            compacting: '压缩中',
        };
        dom.statusText.textContent = map[status] || status;
        dom.statusDot.dataset.status = status;
        // 输入框禁用态：思考中 / 工具执行中 / 压缩中 都不可输入
        dom.input.disabled = (status === 'thinking' || status === 'tool_running' || status === 'compacting');
        // 若用户刚发了消息而 thinking 节点尚未渲染（后端 status_update 抢先到达），
        // 兜底补一个；正常情况下 onSendClicked 已主动插入。
        if (status === 'thinking' && state.expectingAssistant) {
            showThinking();
        }
        renderSendButton();
    }

    function renderSendButton() {
        if (state.streaming || state.agentStatus === 'thinking' || state.agentStatus === 'tool_running' || state.agentStatus === 'compacting') {
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
        state._memoryReviewById = {};
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
                    m.tool_call.server, // Step 8:历史会话中的 MCP 远端工具也带 server 来源
                );
                updateToolEndNode(node, {
                    tool_use_id: m.tool_call.id,
                    name:        m.tool_call.name,
                    output:      m.tool_call.output,
                    is_error:    m.tool_call.is_error,
                    duration_ms: m.tool_call.duration_ms,
                    status:      m.tool_call.status,
                    server:      m.tool_call.server,
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
    // server 可选：远端 MCP 工具时填入 server 名，会在工具块头部展示 mcp:<server> 徽标。
    function appendToolStartNode(toolUseId, name, input, startedAtIso, server) {
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

        // Step 8:远端 MCP 工具时,在 name 后追加 server 来源徽标
        if (server) {
            const mcpBadge = document.createElement('span');
            mcpBadge.className = 'mcp-server-badge';
            mcpBadge.dataset.mcpServer = server;
            mcpBadge.title = 'MCP 远端工具,来源 server=' + server;
            mcpBadge.textContent = 'mcp: ' + server;
            header.appendChild(mcpBadge);
        }

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

        // Step 1.4：操作按钮容器。初始为空，由 updateToolEndNode 根据
        // 工具名 + status 决定是否注入「查看改动」等动作按钮。
        // 用 margin-left:auto 推到右侧（toggle 紧跟其后在最右）。
        const actions = document.createElement('span');
        actions.className = 'message-tool-actions';
        actions.dataset.toolActions = '1';
        header.appendChild(actions);

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

        // Step 1.4：按工具名 + status 注入头部动作按钮。
        // 仅 WriteFile/EditFile 且 status==='completed' 时显示「查看改动」。
        // 失败 / 超时 / 中断不显示（无 diff 可看）。
        const toolName = endPayload.name || (node.querySelector('.message-tool-name')?.textContent || '');
        if (status === 'completed' && isFileEditingTool(toolName)) {
            const actions = node.querySelector('.message-tool-actions');
            if (actions) {
                attachViewDiffButton(actions, node.dataset.toolUseId);
            }
        }

        // Step 8:同步/更新 server 来源徽标。end 消息可能携带 server 字段(用于 start 未带 server 的兜底)
        if (endPayload.server) {
            ensureMCPServerBadge(node, endPayload.server);
        }
    }

    function onMemoryReviewEvent(p) {
        if (!p || !p.review_id || !p.status) return;
        let node = state._memoryReviewById[p.review_id];
        if (!node) {
            node = appendMemoryReviewNode(p);
            state._memoryReviewById[p.review_id] = node;
        } else {
            updateMemoryReviewNode(node, p);
        }
        scrollToBottomIfNeeded();
    }

    function appendMemoryReviewNode(p) {
        const empty = dom.messages.querySelector('.messages-empty');
        if (empty) empty.remove();

        const wrap = document.createElement('div');
        wrap.className = 'message-memory';
        wrap.dataset.reviewId = p.review_id;
        wrap.dataset.status = p.status || 'started';

        const content = document.createElement('div');
        content.className = 'message-memory-content';

        const header = document.createElement('div');
        header.className = 'message-memory-header';

        const icon = document.createElement('span');
        icon.className = 'message-memory-icon';
        icon.textContent = 'M';
        header.appendChild(icon);

        const name = document.createElement('span');
        name.className = 'message-memory-name';
        name.textContent = '自动记忆';
        header.appendChild(name);

        const summary = document.createElement('span');
        summary.className = 'message-memory-summary';
        header.appendChild(summary);

        const status = document.createElement('span');
        status.className = 'message-memory-status';
        header.appendChild(status);

        const dur = document.createElement('span');
        dur.className = 'message-memory-duration';
        header.appendChild(dur);

        content.appendChild(header);
        wrap.appendChild(content);
        dom.messages.appendChild(wrap);
        updateMemoryReviewNode(wrap, p);
        return wrap;
    }

    function updateMemoryReviewNode(node, p) {
        if (!node || !p) return;
        const status = p.status || node.dataset.status || 'started';
        node.dataset.status = status;
        const summary = node.querySelector('.message-memory-summary');
        if (summary) summary.textContent = memoryReviewSummary(p);
        const statusEl = node.querySelector('.message-memory-status');
        if (statusEl) statusEl.textContent = memoryReviewStatusLabel(status);
        const dur = node.querySelector('.message-memory-duration');
        if (dur) dur.textContent = p.duration_ms ? formatDuration(p.duration_ms) : '';
    }

    function memoryReviewStatusLabel(status) {
        switch (status) {
            case 'started': return 'running';
            case 'completed': return 'saved';
            case 'no_decision': return 'checked';
            case 'error': return 'failed';
            default: return status || 'review';
        }
    }

    function memoryReviewSummary(p) {
        switch (p.status) {
            case 'started':
                return '正在回顾本轮对话，判断是否需要沉淀长期记忆';
            case 'completed':
                return (p.applied || 0) > 0
                    ? `已沉淀 ${p.applied} 条长期记忆`
                    : '回顾完成，未写入新的记忆';
            case 'no_decision':
                return '已回顾，本轮没有需要沉淀的长期记忆';
            case 'error':
                return p.err ? ('自动记忆回顾失败：' + p.err) : '自动记忆回顾失败';
            default:
                return '自动记忆状态更新';
        }
    }

    // ensureMCPServerBadge 注入或更新工具块头部的 MCP server 徽标。
    //
    // 行为:
    //   - 已有徽标(可能与新 server 不同,如网络抖动)→ 替换 text
    //   - 无徽标 → 在 name 元素后插入一个 <span class="mcp-server-badge">
    //   - server 为空时不操作(内置工具不应展示徽标)
    //
    // DOM 复用:通过 data-mcp-server 属性去重,避免重复插入。
    function ensureMCPServerBadge(node, server) {
        if (!node || !server) return;
        const header = node.querySelector('.message-tool-header');
        if (!header) return;
        const nameEl = header.querySelector('.message-tool-name');
        if (!nameEl) return;
        // 找现有徽标
        let badge = header.querySelector('.mcp-server-badge');
        if (badge) {
            badge.textContent = 'mcp: ' + server;
            badge.dataset.mcpServer = server;
            return;
        }
        // 在 name 元素后插入(视觉上紧跟工具名)
        badge = document.createElement('span');
        badge.className = 'mcp-server-badge';
        badge.dataset.mcpServer = server;
        badge.title = 'MCP 远端工具,来源 server=' + server;
        badge.textContent = 'mcp: ' + server;
        if (nameEl.nextSibling) {
            header.insertBefore(badge, nameEl.nextSibling);
        } else {
            header.appendChild(badge);
        }
    }

    // isFileEditingTool 判断工具是否为「文件编辑类」——只有 WriteFile/EditFile
    // 会向 FileDiffStore 写入记录，其他工具（Bash/Glob/Grep/ReadFile）无 diff。
    function isFileEditingTool(name) {
        return name === 'WriteFile' || name === 'EditFile';
    }

    // attachViewDiffButton 向 actions 容器插入「查看改动」按钮。
    // 幂等：重复调用不会重复插入（已存在则替换文本；此处一般只调一次）。
    // stopPropagation 防止点击冒泡触发 header 的折叠 toggle。
    function attachViewDiffButton(actions, toolUseId) {
        // 幂等：避免重复插入
        let btn = actions.querySelector('[data-action="view-diff"]');
        if (!btn) {
            btn = document.createElement('button');
            btn.type = 'button';
            btn.className = 'message-tool-action-btn';
            btn.dataset.action = 'view-diff';
            btn.title = '查看本次调用的文件改动（左右双栏 diff）';
            btn.innerHTML = `
                <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                    <path d="M2 4h7"></path>
                    <path d="M2 8h10"></path>
                    <path d="M2 12h5"></path>
                    <path d="M11 12l3-3-3-3"></path>
                </svg>
                <span>查看改动</span>
            `;
            actions.appendChild(btn);
        }
        // 每次重新绑定（replace node 后旧 handler 失效），使用 cloneNode 简化逻辑
        const fresh = btn.cloneNode(true);
        btn.parentNode.replaceChild(fresh, btn);
        fresh.addEventListener('click', (ev) => {
            ev.stopPropagation();   // 防止冒泡触发 header 折叠
            ev.preventDefault();
            openFileDiffModal(toolUseId);
        });
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

    // bindDevPanel 绑定开发者面板按钮事件。
    //
    // 触发方式：
    //   1. SP 状态栏双击（dblclick）→ 切换 dev 面板
    //   2. dev 面板内「Export SP」→ 发送 dev_export_sp 请求
    //   3. dev 面板关闭按钮 → 隐藏面板
    //   4. SP 模态框 backdrop / 关闭按钮 → 关闭模态
    function bindDevPanel() {
        // 双击 SP 区域打开/关闭开发者面板
        if (dom.spTokens && dom.spTokens.parentElement) {
            dom.spTokens.parentElement.addEventListener('dblclick', () => {
                toggleDevPanel();
            });
        }
        if (dom.devPanelClose) {
            dom.devPanelClose.addEventListener('click', () => toggleDevPanel(false));
        }
        if (dom.devExportBtn) {
            dom.devExportBtn.addEventListener('click', () => {
                // 触发服务端导出；响应通过 onDevExportSP 接收
                sendWS(MsgType.DevExportSP, {});
            });
        }
        if (dom.spModal) {
            dom.spModal.querySelectorAll('[data-sp-modal-close]').forEach(el => {
                el.addEventListener('click', closeSPModal);
            });
        }
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
        bindDevPanel();
        bindCompactBtn();
        renderCompactStat();
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

    // =========================================================================
    // Step 5：权限确认对话框（HITL — Human-in-the-Loop）
    // =========================================================================
    // 权限确认倒计时总秒数，与服务端 hitlTimeout (60s) 保持一致。
    const PERM_COUNTDOWN_SECONDS = 60;

    // onPermissionRequest 收到后端 permission_request 消息后的入口。
    // 若当前已有确认对话框在展示，则将新请求排队等待（FIFO）。
    // 否则直接弹出对话框。
    function onPermissionRequest(p) {
        if (!p || !p.id) {
            console.warn('onPermissionRequest: 无效的请求 payload', p);
            return;
        }
        // 如果当前有弹窗打开，排队等待
        if (state._permModal) {
            state._permQueue.push(p);
            return;
        }
        openPermModal(p);
    }

    // openPermModal 构造并展示权限确认对话框。
    // 流程：
    //   1. 构造 modal DOM 插入 body
    //   2. 绑定四个按钮的点击事件
    //   3. 启动 60s 倒计时，到期自动发送拒绝
    //   4. 不绑定 Esc / 遮罩点击关闭（防止误操作绕过确认）
    //   5. 状态栏切换为"等待用户确认..."
    function openPermModal(payload) {
        const modal = buildPermModalSkeleton(payload);
        document.body.appendChild(modal);
        state._permModal = modal;

        // 状态栏切换为等待确认
        dom.statusText.textContent = '等待用户确认...';
        dom.statusDot.dataset.status = 'thinking';

        // 启动倒计时
        startPermCountdown(payload.id, PERM_COUNTDOWN_SECONDS);
    }

    // closePermModal 关闭当前权限确认弹窗，清理倒计时定时器，
    // 并检查队列中是否有等待的请求，有则弹出下一个。
    function closePermModal() {
        const modal = state._permModal;
        if (!modal) return;

        // 清理倒计时
        if (state._permCountdownTimer) {
            clearInterval(state._permCountdownTimer);
            state._permCountdownTimer = null;
        }

        // 移除 DOM
        if (modal.parentNode) modal.parentNode.removeChild(modal);
        state._permModal = null;

        // 恢复状态栏（回到之前的状态，由后续的 status_update 消息覆盖）
        setAgentStatus(state.agentStatus);

        // 检查队列
        if (state._permQueue.length > 0) {
            const next = state._permQueue.shift();
            // 使用 requestAnimationFrame 确保 DOM 清理完成后再弹出下一个
            requestAnimationFrame(() => openPermModal(next));
        }
    }

    // sendPermResponse 发送权限确认响应给后端，并关闭对话框。
    function sendPermResponse(id, allowed, scope) {
        sendWS(MsgType.PermissionResponse, { id, allowed, scope });
        closePermModal();
    }

    // permTargetDirHint 计算"永久允许"将放行的目录 glob（与后端 BuildPathPattern 语义一致）。
    //
    // 行为：
    //   - 绝对路径 → 取父目录 + "/*"
    //   - 相对路径 → 若 workdir 给出，拼接 workdir 后取父目录；否则取父目录
    //   - 空路径 → "*"（工具级豁免占位）
    //
    // 本函数只用于前端 UI 提示，实际写入 setting.json 的 Pattern 由后端
    // security.BuildPathPattern 计算——保持单一事实来源。
    function permTargetDirHint(targetPath, workdir) {
        if (!targetPath) return '*';
        let abs = targetPath;
        if (!isAbsolutePath(targetPath) && workdir) {
            abs = joinPath(workdir, targetPath);
        }
        const dir = dirname(abs);
        if (!dir || dir === '.' || dir === '/') {
            return joinPath(abs, '*');
        }
        return joinPath(dir, '*');
    }

    // isAbsolutePath / dirname / joinPath：跨平台路径处理
    // 简化版：仅作 UI 提示用，健壮性由后端兜底
    function isAbsolutePath(p) {
        if (!p) return false;
        // Windows: C:\... 或 \\server\...; Unix: /...
        if (/^[a-zA-Z]:[\\/]/.test(p)) return true;
        if (p.startsWith('/') || p.startsWith('\\')) return true;
        return false;
    }

    function dirname(p) {
        if (!p) return '';
        const idx = Math.max(p.lastIndexOf('/'), p.lastIndexOf('\\'));
        if (idx <= 0) return '';
        return p.substring(0, idx);
    }

    function joinPath(dir, name) {
        if (!dir) return name;
        if (dir.endsWith('/') || dir.endsWith('\\')) return dir + name;
        return dir + '/' + name;
    }

    // buildPermModalSkeleton 构造权限确认弹窗的 DOM 骨架。
    // 不使用 innerHTML 拼接用户数据（XSS 防护），改用 textContent。
    function buildPermModalSkeleton(payload) {
        const modal = document.createElement('div');
        modal.className = 'perm-modal';
        modal.setAttribute('role', 'dialog');
        modal.setAttribute('aria-modal', 'true');
        modal.setAttribute('aria-label', '权限确认');

        const card = document.createElement('div');
        card.className = 'perm-modal-card';
        // 阻止 card 内点击冒泡到 modal（虽然 modal 不关闭，但保持一致的 DOM 模式）
        card.addEventListener('click', (ev) => ev.stopPropagation());

        // ---- 头部 ----
        const header = document.createElement('div');
        header.className = 'perm-modal-header';

        const iconWrap = document.createElement('div');
        iconWrap.className = 'perm-modal-icon';
        // 盾牌图标（与权限主题一致）
        iconWrap.innerHTML = '<svg viewBox="0 0 16 16" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><path d="M8 1.5L2.5 4v4c0 3.5 2.5 6.5 5.5 7.5 3-1 5.5-4 5.5-7.5V4L8 1.5z"/><path d="M8 5.5v3M8 10.5v.5"/></svg>';

        const title = document.createElement('span');
        title.className = 'perm-modal-title';
        title.textContent = '权限确认';

        header.appendChild(iconWrap);
        header.appendChild(title);

        // ---- 内容区 ----
        const body = document.createElement('div');
        body.className = 'perm-modal-body';

        // 工具名
        const toolField = document.createElement('div');
        toolField.className = 'perm-modal-field';
        const toolLabel = document.createElement('span');
        toolLabel.className = 'perm-modal-label';
        toolLabel.textContent = '工具';
        const toolValue = document.createElement('span');
        toolValue.className = 'perm-modal-tool-name';
        toolValue.textContent = payload.tool_name || '未知工具';
        toolField.appendChild(toolLabel);
        toolField.appendChild(toolValue);

        // 参数摘要
        const paramField = document.createElement('div');
        paramField.className = 'perm-modal-field';
        const paramLabel = document.createElement('span');
        paramLabel.className = 'perm-modal-label';
        paramLabel.textContent = '参数';
        const paramValue = document.createElement('span');
        paramValue.className = 'perm-modal-value';
        paramValue.textContent = payload.params_summary || '(无参数信息)';
        paramField.appendChild(paramLabel);
        paramField.appendChild(paramValue);

        // 触发原因
        const reasonField = document.createElement('div');
        reasonField.className = 'perm-modal-field';
        const reasonLabel = document.createElement('span');
        reasonLabel.className = 'perm-modal-label';
        reasonLabel.textContent = '原因';
        const reasonValue = document.createElement('span');
        reasonValue.className = 'perm-modal-reason';
        reasonValue.textContent = payload.reason || '需要用户确认';
        reasonField.appendChild(reasonLabel);
        reasonField.appendChild(reasonValue);

        body.appendChild(toolField);
        body.appendChild(paramField);
        body.appendChild(reasonField);

        // ---- Step 5 增强：目标路径 + 工作目录（仅路径类工具有意义）----
        if (payload.target_path) {
            // 工作目录（让用户一眼看出"目标在工作目录外"）
            if (payload.workdir) {
                const workdirField = document.createElement('div');
                workdirField.className = 'perm-modal-field';
                const workdirLabel = document.createElement('span');
                workdirLabel.className = 'perm-modal-label';
                workdirLabel.textContent = '工作目录';
                const workdirValue = document.createElement('span');
                workdirValue.className = 'perm-modal-value perm-modal-workdir';
                workdirValue.textContent = payload.workdir;
                workdirField.appendChild(workdirLabel);
                workdirField.appendChild(workdirValue);
                body.appendChild(workdirField);
            }

            // 目标路径（高亮：突出"这是路径类操作"）
            const pathField = document.createElement('div');
            pathField.className = 'perm-modal-field perm-modal-path';
            const pathLabel = document.createElement('span');
            pathLabel.className = 'perm-modal-label';
            pathLabel.textContent = '目标路径';
            const pathValue = document.createElement('span');
            pathValue.className = 'perm-modal-value perm-modal-target-path';
            pathValue.textContent = payload.target_path;
            pathField.appendChild(pathLabel);
            pathField.appendChild(pathValue);
            body.appendChild(pathField);
        }

        // ---- Step 5 增强：matched_rule.pattern 展示（如果存在）----
        if (payload.matched_rule && payload.matched_rule.pattern) {
            const matchedField = document.createElement('div');
            matchedField.className = 'perm-modal-field perm-modal-matched';
            const matchedLabel = document.createElement('span');
            matchedLabel.className = 'perm-modal-label';
            matchedLabel.textContent = '命中规则';
            const matchedValue = document.createElement('span');
            matchedValue.className = 'perm-modal-value';
            const tool = payload.matched_rule.tool || '*';
            const pattern = payload.matched_rule.pattern;
            const action = payload.matched_rule.action || '';
            matchedValue.textContent = `${tool}  ${pattern}  ${action}`;
            matchedField.appendChild(matchedLabel);
            matchedField.appendChild(matchedValue);
            body.appendChild(matchedField);
        }

        // ---- 按钮区 ----
        const actions = document.createElement('div');
        actions.className = 'perm-modal-actions';

        const btnDeny = document.createElement('button');
        btnDeny.className = 'perm-btn perm-btn-deny';
        btnDeny.textContent = '拒绝';
        btnDeny.type = 'button';
        btnDeny.addEventListener('click', () => sendPermResponse(payload.id, false, 'once'));

        const btnOnce = document.createElement('button');
        btnOnce.className = 'perm-btn perm-btn-once';
        btnOnce.textContent = '本次允许';
        btnOnce.type = 'button';
        btnOnce.addEventListener('click', () => sendPermResponse(payload.id, true, 'once'));

        const btnSession = document.createElement('button');
        btnSession.className = 'perm-btn perm-btn-session';
        btnSession.textContent = '本会话允许';
        btnSession.type = 'button';
        btnSession.addEventListener('click', () => sendPermResponse(payload.id, true, 'session'));

        const btnPermanent = document.createElement('button');
        btnPermanent.className = 'perm-btn perm-btn-permanent';
        btnPermanent.textContent = '永久允许';
        btnPermanent.type = 'button';
        btnPermanent.addEventListener('click', () => sendPermResponse(payload.id, true, 'permanent'));

        actions.appendChild(btnDeny);
        actions.appendChild(btnOnce);
        actions.appendChild(btnSession);
        actions.appendChild(btnPermanent);

        // ---- 永久放行范围提示（仅路径类工具 + 有 target_path 时）----
        // 提示后端"永久允许"将放行的实际目录 glob（与后端 BuildPathPattern 语义一致）
        let hint = null;
        if (payload.target_path) {
            hint = document.createElement('div');
            hint.className = 'perm-modal-hint';
            const targetDir = permTargetDirHint(payload.target_path, payload.workdir);
            hint.textContent = '选择"永久允许"将放行该目录（' + targetDir + '）下所有读操作';
        }

        // ---- 倒计时区域 ----
        const countdown = document.createElement('div');
        countdown.className = 'perm-modal-countdown';
        const countdownText = document.createElement('span');
        countdownText.className = 'perm-modal-countdown-text';
        countdownText.id = 'perm-countdown-text';
        countdownText.textContent = PERM_COUNTDOWN_SECONDS + ' 秒后自动拒绝';
        countdown.appendChild(countdownText);

        // ---- 组装 ----
        card.appendChild(header);
        card.appendChild(body);
        card.appendChild(actions);
        if (hint) {
            card.appendChild(hint);
        }
        card.appendChild(countdown);
        modal.appendChild(card);

        return modal;
    }

    // startPermCountdown 启动倒计时。
    // 每秒更新文案，最后 10 秒进入紧急样式（红色闪烁）。
    // 倒计时归零时自动发送拒绝并关闭对话框。
    function startPermCountdown(id, total) {
        let remaining = total;
        const textEl = document.getElementById('perm-countdown-text');
        if (!textEl) return;

        state._permCountdownTimer = setInterval(() => {
            remaining--;
            if (remaining <= 0) {
                // 倒计时归零：自动拒绝
                clearInterval(state._permCountdownTimer);
                state._permCountdownTimer = null;
                sendPermResponse(id, false, 'once');
                return;
            }
            textEl.textContent = remaining + ' 秒后自动拒绝';
            // 最后 10 秒进入紧急样式
            if (remaining <= 10) {
                textEl.classList.add('urgent');
            }
        }, 1000);
    }

    // =========================================================================
    // Step 5：权限模式状态栏展示
    // =========================================================================

    // 权限模式配置映射：每种模式对应的图标、文案、颜色。
    // icon 用真正的 Unicode 字符（🔒 🛡 🔓），label 用汉字「严格 / 默认 / 放行」，
    // 不要写成 "u{1F6E1}" / "u9ED8u8BA4" 这种字面字符串——JavaScript 不会解析，
    // 会原样显示在 UI 上。
    const PERM_MODE_CONFIG = {
        strict:     { icon: "\u{1F512}", label: "严格", color: "var(--error, #e06c75)" },
        default:    { icon: "\u{1F6E1}", label: "默认", color: "var(--thinking, #61afef)" },
        permissive: { icon: "\u{1F513}", label: "放行", color: "var(--success, #98c379)" },
    };

    // 状态：当前档位（后端最后一次推送为准），用于下拉高亮「当前档位」
    state.permMode = 'default';

    // onPermissionMode 处理后端推送的 permission_mode 消息，
    // 更新状态栏中的权限模式标识 + 下拉中「当前档位」高亮。
    //
    // 调用时机：
    //   1. 会话加载完成（sendPermissionMode 在 session_loaded 之后推送）
    //   2. handleSetPermissionMode 切换档位成功后回推
    function onPermissionMode(p) {
        if (!p || !dom.permMode) return;
        const mode = p.mode || "default";
        const config = PERM_MODE_CONFIG[mode] || PERM_MODE_CONFIG.default;
        state.permMode = mode;   // 同步全局状态，供下拉高亮使用

        // 更新文案和颜色
        dom.permMode.textContent = config.icon + " " + config.label;
        dom.permMode.style.color = config.color;

        // 更新 tooltip：显示模式 + 规则数 + 临时规则数
        const ruleCount = p.rule_count || 0;
        const sessionRuleCount = p.session_rule_count || 0;
        const parent = dom.permMode.closest(".inputbar-stat");
        if (parent) {
            let title = config.label + "模式";
            if (ruleCount > 0) title += " | " + ruleCount + " 条规则";
            if (sessionRuleCount > 0) title += " | " + sessionRuleCount + " 条临时规则";
            parent.title = title;
        }

        // 同步下拉中「当前档位」高亮（点击不关闭下拉时，外部状态变更也要即时反映）
        highlightCurrentPermOption(mode);
    }

    // -------------------------------------------------------------------------
    // MCP 健康状态（Step 8）
    // -------------------------------------------------------------------------
    //
    // 后端 mcp_status payload 结构:
    //   { servers: [{ name, state, tools, reason? }], healthy_count, unhealthy_count, total_tools }
    //
    // 渲染策略：
    //   1. status 文本：healthy N / unhealthy M / tools K
    //      - 全空（未启用 MCP）：显示 "off"
    //      - 全部 healthy：显示 "N ●"（绿色数字 + 圆点）
    //      - 有 unhealthy：显示 "N/M ●●"（红黄圆点）
    //   2. 圆点列：每个 server 一个圆点，按 server 状态着色
    //   3. tooltip：hover 时显示 server 名 + 工具数 + 失败原因

    // onMCPStatus 处理 mcp_status 推送。
    function onMCPStatus(p) {
        if (!p) return;
        const servers = Array.isArray(p.servers) ? p.servers : [];
        const healthyCount = (typeof p.healthy_count === 'number') ? p.healthy_count : 0;
        const unhealthyCount = (typeof p.unhealthy_count === 'number') ? p.unhealthy_count : 0;
        const totalTools = (typeof p.total_tools === 'number') ? p.total_tools : 0;
        const loading = p.loading === true; // MCP 后台初始化中（握手/工具拉取未完成）

        if (!dom.mcpSummary || !dom.mcpDots || !dom.mcpTooltip) return;

        // MCP 后台初始化中：servers 通常为空，偶有个别 server 已就绪。
        // 统一展示"连接中…"脉冲态，避免被下面的 off 分支误判为未启用 MCP。
        // 后台就绪后会再次推送 loading=false 覆盖本态。
        if (loading) {
            dom.mcpSummary.textContent = '连接中…';
            dom.mcpDots.innerHTML = '';
            // 渲染一个琥珀色脉冲圆点表达"连接中"，复用现有 .mcp-dot-reconnecting 样式
            const dot = document.createElement('span');
            dot.className = 'mcp-dot mcp-dot-reconnecting';
            dom.mcpDots.appendChild(dot);
            dom.mcpTooltip.innerHTML = '';
            const heading = document.createElement('div');
            heading.className = 'mcp-tooltip-heading';
            heading.textContent = 'MCP 正在后台连接…';
            dom.mcpTooltip.appendChild(heading);
            if (dom.mcpStat) dom.mcpStat.title = 'MCP 正在后台连接 server';
            return;
        }

        // 未启用 MCP:servers 为空数组
        if (servers.length === 0) {
            dom.mcpSummary.textContent = 'off';
            dom.mcpDots.innerHTML = '';
            dom.mcpTooltip.innerHTML = '';
            if (dom.mcpStat) dom.mcpStat.title = 'MCP 未启用（在 setting.json 中配置 mcp.servers）';
            return;
        }

        // 主文案
        const unhealthySuffix = unhealthyCount > 0 ? (' / ' + unhealthyCount + '❌') : '';
        dom.mcpSummary.textContent = healthyCount + unhealthySuffix + ' • ' + totalTools + ' 工具';

        // 圆点列
        dom.mcpDots.innerHTML = '';
        for (const s of servers) {
            const dot = document.createElement('span');
            dot.className = 'mcp-dot mcp-dot-' + (s.state || 'unknown');
            dot.dataset.server = s.name || '';
            dot.title = (s.name || '') + ': ' + (s.state || 'unknown') +
                (typeof s.tools === 'number' ? ' (' + s.tools + ' 工具)' : '') +
                (s.reason ? ' — ' + s.reason : '');
            dom.mcpDots.appendChild(dot);
        }

        // tooltip 列表
        dom.mcpTooltip.innerHTML = '';
        const heading = document.createElement('div');
        heading.className = 'mcp-tooltip-heading';
        heading.textContent = 'MCP servers (' + healthyCount + ' healthy)';
        dom.mcpTooltip.appendChild(heading);
        for (const s of servers) {
            const row = document.createElement('div');
            row.className = 'mcp-tooltip-row';
            const dot = document.createElement('span');
            dot.className = 'mcp-dot mcp-dot-' + (s.state || 'unknown');
            row.appendChild(dot);
            const nameEl = document.createElement('span');
            nameEl.className = 'mcp-tooltip-name';
            nameEl.textContent = s.name || '(unnamed)';
            row.appendChild(nameEl);
            const meta = document.createElement('span');
            meta.className = 'mcp-tooltip-meta';
            meta.textContent = (s.state || 'unknown') +
                (typeof s.tools === 'number' ? ' • ' + s.tools + ' 工具' : '');
            row.appendChild(meta);
            if (s.reason) {
                const reason = document.createElement('div');
                reason.className = 'mcp-tooltip-reason';
                reason.textContent = s.reason;
                row.appendChild(reason);
            }
            dom.mcpTooltip.appendChild(row);
        }

        // tooltip 显示控制：hover/leave 切换
        if (dom.mcpStat) {
            dom.mcpStat.onmouseenter = () => { dom.mcpTooltip.hidden = false; };
            dom.mcpStat.onmouseleave = () => { dom.mcpTooltip.hidden = true; };
        }
    }

    // -------------------------------------------------------------------------
    // Step 7：上下文压缩事件 + 状态栏压缩按钮
    // -------------------------------------------------------------------------
    //
    // 后端 compaction_event payload 结构（与 protocol.go CompactionEventPayload 对齐）：
    //   { level, light_changed, summary_changed, replaced_blocks,
    //     before_tokens, after_tokens, tripped, manual, err }
    //
    // 提示强度按 Level 分级（spec 要求「summary 强提示 / light 轻量感知」）：
    //   - summary：顶部 toast 强提示（重量级，用户须感知历史被摘要化）；
    //   - light：仅更新状态栏压缩计数小标记，不弹 toast（每轮都可能跑，避免打扰）；
    //   - none：仅 manual 时弹 toast 反馈「无需压缩」。

    // onCompactionEvent 处理后端推送的 compaction_event。
    function onCompactionEvent(p) {
        if (!p) return;
        const level = p.level || 'none';
        const manual = p.manual === true;

        // 第一层 light：累计替换数到状态栏小标记（轻量感知，不打扰）
        if (level === 'light' && (p.replaced_blocks || 0) > 0) {
            state.compactLightCount = (state.compactLightCount || 0) + p.replaced_blocks;
            renderCompactStat();
        }

        // 第二层 summary：强提示 toast（重量级压缩，用户须明确感知）
        if (level === 'summary') {
            const before = p.before_tokens || 0;
            const after = p.after_tokens || 0;
            const saved = Math.max(0, before - after);
            const msg = manual
                ? `已手动压缩：历史摘要化（${formatTokenCount(before)} → ${formatTokenCount(after)}，释放 ${formatTokenCount(saved)})`
                : `上下文接近上限，已自动将历史压缩为摘要（释放 ${formatTokenCount(saved)} token）`;
            showCompactionToast(msg, manual ? 'summary-manual' : 'summary');
            // 摘要化后历史已重组，旧轻量计数不再有意义，重置
            state.compactLightCount = 0;
            renderCompactStat();
        }

        // 手动触发但未实际压缩（Level=none）：反馈「无需压缩」
        if (manual && level === 'none' && !p.err) {
            showCompactionToast('当前上下文无需压缩', 'info');
        }

        // 熔断警告（自动第二层被禁用，用户可手动重试）
        if (p.tripped) {
            showCompactionToast('压缩已熔断：摘要连续失败，自动压缩暂停（可再次手动重试）', 'warn');
        }
        // 错误反馈
        if (p.err) {
            showCompactionToast('压缩失败：' + p.err, 'error');
        }
    }

    // renderCompactStat 渲染状态栏压缩计数小标记（第一层轻量感知）。
    function renderCompactStat() {
        if (!dom.compactValue) return;
        const n = state.compactLightCount || 0;
        dom.compactValue.textContent = n > 0 ? ('⚡' + n) : '–';
    }

    // onDumpResult 处理后端 /dump 导出结果推送。
    // 成功：toast 提示两个文件的绝对路径（会话目录下 dump.json / dump.md）；
    // 失败：toast 提示根因（busy / no_active_session / dump_failed）。
    function onDumpResult(p) {
        if (p && p.ok) {
            showCompactionToast('已导出到：' + (p.json_path || '') + ' · ' + (p.md_path || ''), 'summary');
        } else {
            const reason = (p && p.err) ? p.err : '未知原因';
            showCompactionToast('导出失败：' + reason, 'error');
        }
    }

    // showCompactionToast 显示一个顶部 toast，type 决定配色与停留时长。
    // 自动消失；点击或超时后移除。
    function showCompactionToast(text, type) {
        if (!dom.toastContainer) return;
        const toast = document.createElement('div');
        toast.className = 'toast toast-' + (type || 'info');
        toast.setAttribute('role', 'status');
        toast.textContent = text;
        toast.addEventListener('click', () => dismissToast(toast));
        dom.toastContainer.appendChild(toast);
        // 入场动画：下一帧加 is-visible 触发过渡
        requestAnimationFrame(() => toast.classList.add('is-visible'));
        const dwell = (type === 'summary-manual') ? 6500
            : (type === 'summary' ? 5000
                : (type === 'error' || type === 'warn' ? 5000 : 3500));
        setTimeout(() => dismissToast(toast), dwell);
    }

    function dismissToast(toast) {
        if (!toast || !toast.parentNode) return;
        toast.classList.remove('is-visible');
        toast.classList.add('is-leaving');
        setTimeout(() => { if (toast.parentNode) toast.parentNode.removeChild(toast); }, 250);
    }

    // bindCompactBtn 绑定状态栏压缩按钮点击 → 发送 compact 请求（手动压缩）。
    function bindCompactBtn() {
        if (dom.compactBtn) {
            dom.compactBtn.addEventListener('click', () => {
                sendWS(MsgType.Compact, {});
            });
        }
    }

    // -------------------------------------------------------------------------
    // 权限模式下拉（点击 permission 区域展开 / 选中后关闭）
    // -------------------------------------------------------------------------

    // togglePermDropdown 切换下拉显示状态。
    // 打开时：第一次打开注册全局 click + ESC 关闭监听；关闭时移除监听。
    function togglePermDropdown(force) {
        if (!dom.permDropdown || !dom.permStat) return;
        const willOpen = force === undefined ? dom.permDropdown.hidden : !!force;
        if (willOpen) {
            dom.permDropdown.hidden = false;
            dom.permStat.classList.add('is-open');
            // 立即同步「当前档位」高亮
            highlightCurrentPermOption(state.permMode);
            // 注册全局监听：点击其他位置 / ESC 时关闭
            // 用 setTimeout 延后绑定，避免本次点击事件冒泡到 document 立即触发关闭
            setTimeout(() => {
                document.addEventListener('click', onDocClickClosePerm);
                document.addEventListener('keydown', onEscClosePerm);
            }, 0);
        } else {
            dom.permDropdown.hidden = true;
            dom.permStat.classList.remove('is-open');
            document.removeEventListener('click', onDocClickClosePerm);
            document.removeEventListener('keydown', onEscClosePerm);
        }
    }

    // onDocClickClosePerm 点击下拉外区域时关闭下拉。
    // 用 closest() 检查事件源是否在 permStat 子树内；不在则关闭。
    function onDocClickClosePerm(e) {
        if (dom.permStat && !dom.permStat.contains(e.target)) {
            togglePermDropdown(false);
        }
    }

    // onEscClosePerm ESC 键关闭下拉。
    function onEscClosePerm(e) {
        if (e.key === 'Escape') {
            togglePermDropdown(false);
        }
    }

    // highlightCurrentPermOption 同步下拉选项中的「当前档位」高亮。
    // data-mode 与档位字符串对应；给匹配的 option 加 is-current。
    function highlightCurrentPermOption(mode) {
        if (!dom.permDropdown) return;
        const opts = dom.permDropdown.querySelectorAll('.perm-mode-option');
        opts.forEach(btn => {
            if (btn.dataset.mode === mode) {
                btn.classList.add('is-current');
            } else {
                btn.classList.remove('is-current');
            }
        });
    }

    // onPermOptionPicked 下拉选项点击入口。
    // 1. 与当前档位相同则仅关闭下拉（不发送无意义请求）
    // 2. 不同则发送 set_permission_mode，后端回推 permission_mode 后 UI 自动同步
    function onPermOptionPicked(mode) {
        // 总是先关闭下拉（视觉即时反馈，避免等待网络）
        togglePermDropdown(false);
        if (mode === state.permMode) return;

        // 防御：mode 必须是合法档位
        if (!PERM_MODE_CONFIG[mode]) return;

        sendWS(MsgType.SetPermissionMode, { mode: mode });
    }

    // 初始化：注册 perm-stat 点击 + 下拉选项点击事件。
    // 这些 listener 只注册一次，在 IIFE 启动时执行。
    function initPermDropdown() {
        if (dom.permStat) {
            dom.permStat.addEventListener('click', (e) => {
                // 阻止冒泡，避免 onDocClickClosePerm 误关闭
                e.stopPropagation();
                togglePermDropdown();
            });
        }
        if (dom.permDropdown) {
            // 事件委托：3 个 .perm-mode-option 共用一个 listener
            dom.permDropdown.addEventListener('click', (e) => {
                const btn = e.target.closest('.perm-mode-option');
                if (!btn) return;
                // 阻止冒泡，避免冒泡到 permStat 触发 toggle
                e.stopPropagation();
                const mode = btn.dataset.mode;
                if (mode) onPermOptionPicked(mode);
            });
        }
    }

    // =========================================================================
    // 主题切换（亮色 / 暗色）
    // -------------------------------------------------------------------------
    // 触发链路：
    //   1. index.html <head> 内联脚本同步读 localStorage 并写 <html data-theme>，
    //      在 CSS 应用前完成，避免首屏闪烁（FOUC）。
    //   2. 本模块的 bindThemeToggle() 接管交互：点击 #theme-toggle 翻转主题，
    //      并把新值持久化到 localStorage。
    // 主题值集合严格白名单（'light' | 'dark'），未识别值统一回退到 dark。
    // =========================================================================

    // 主题键名与可选取值集中常量，避免散落字符串字面量
    const THEME_STORAGE_KEY = 'codepilot-theme';
    const THEME_LIGHT = 'light';
    const THEME_DARK  = 'dark';

    // 从 <html data-theme> 读取当前主题。
    // 未设置时（首访 / localStorage 不可用）回退到暗色——与全站默认基调一致。
    // [Why] 不再回读 localStorage：FOUC 内联脚本已把持久值同步到 <html> 属性，
    // 直接读属性比读 storage 少一次 IO，也避免了 storage 与属性短暂不一致的边缘态。
    function getCurrentTheme() {
        const t = document.documentElement.getAttribute('data-theme');
        return t === THEME_LIGHT ? THEME_LIGHT : THEME_DARK;
    }

    // 应用主题：只写 <html data-theme>，不写 localStorage。
    // [Why] <html> 是 :root 所在节点，CSS 变量在这里定义，写这里使所有后代元素
    // （含 app.js 尚未初始化的 DOM）能立即感知主题变更。
    function applyTheme(theme) {
        document.documentElement.setAttribute('data-theme', theme);
    }

    // 持久化：仅在用户主动点击切换时调用，首访不写。
    // 避免「首次打开页面就修改 storage」的隐私打扰。
    function persistTheme(theme) {
        try { localStorage.setItem(THEME_STORAGE_KEY, theme); } catch (_) { /* 隐私模式等静默 */ }
    }

    // 同步按钮的 title / aria-label：反映当前主题 + 提示点击后去向。
    // 工具提示动态更新比静态文案更友好，能让用户随时知道「再点一次会变什么」。
    function updateThemeToggleTitle() {
        if (!dom.themeToggle) return;
        const current = getCurrentTheme();
        const isLight = current === THEME_LIGHT;
        const cur = isLight ? '亮色' : '暗色';
        const toOther = isLight ? '暗色' : '亮色';
        const label = `切换主题（当前：${cur}，点击切到${toOther}）`;
        dom.themeToggle.title = label;
        dom.themeToggle.setAttribute('aria-label', label);
    }

    // 绑定主题切换按钮：仅注册一次，在 IIFE 启动时执行。
    function bindThemeToggle() {
        if (!dom.themeToggle) return;
        // 启动时同步按钮提示，标题反映当前真实主题
        updateThemeToggleTitle();
        dom.themeToggle.addEventListener('click', () => {
            const next = getCurrentTheme() === THEME_LIGHT ? THEME_DARK : THEME_LIGHT;
            applyTheme(next);
            persistTheme(next);
            updateThemeToggleTitle();
        });
    }

    // 在 IIFE 末尾的主入口调用
    initPermDropdown();
    bindThemeToggle();
})();
