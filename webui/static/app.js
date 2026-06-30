// ========================================
// Proxy Manager — Frontend Application
// 原生 JS，零依赖，ES Modules 风格组织
// ========================================

(function () {
  'use strict';

  // ========== State ==========
  const state = {
    scripts: [],
    activeScriptId: null,
    editorDirty: false,
    logs: [],
    logsPaused: false,
    sseConnected: false,
    sse: null,
  };

  // ========== API Layer ==========
  const api = {
    async listScripts() {
      const resp = await fetch('/api/rules');
      const data = await resp.json();
      return data.scripts || [];
    },
    async getScript(id) {
      const resp = await fetch(`/api/rules/${id}`);
      return resp.json();
    },
    async getScriptContent(id) {
      const resp = await fetch(`/api/rules/${id}/content`);
      return resp.json();
    },
    async createScript(name, content) {
      const resp = await fetch('/api/rules', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, content }),
      });
      if (!resp.ok) {
        const err = await resp.json();
        throw new Error(err.error || 'Create failed');
      }
      return resp.json();
    },
    async updateScript(id, content) {
      const resp = await fetch(`/api/rules/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
      });
      if (!resp.ok) {
        const err = await resp.json();
        throw new Error(err.error || 'Update failed');
      }
      return resp.json();
    },
    async deleteScript(id) {
      const resp = await fetch(`/api/rules/${id}`, { method: 'DELETE' });
      if (!resp.ok) throw new Error('Delete failed');
    },
    async enableScript(id) {
      const resp = await fetch(`/api/rules/${id}/enable`, { method: 'POST' });
      if (!resp.ok) throw new Error('Enable failed');
    },
    async disableScript(id) {
      const resp = await fetch(`/api/rules/${id}/disable`, { method: 'POST' });
      if (!resp.ok) throw new Error('Disable failed');
    },
    async reloadScript(id) {
      const resp = await fetch(`/api/rules/${id}/reload`, { method: 'POST' });
      if (!resp.ok) throw new Error('Reload failed');
    },
    async reloadAll() {
      const resp = await fetch('/api/rules/reload-all', { method: 'POST' });
      if (!resp.ok) throw new Error('Reload all failed');
    },
    async validate(content) {
      const resp = await fetch('/api/rules/validate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
      });
      return resp.json();
    },
    async getStatus() {
      const resp = await fetch('/api/status');
      return resp.json();
    },
    async getDebugStatus() {
      const resp = await fetch('/debug/status');
      return resp.json();
    },
  };

  // ========== UI Utilities ==========
  const $ = (sel) => document.querySelector(sel);
  const $$ = (sel) => document.querySelectorAll(sel);

  function escapeHtml(str) {
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
  }

  function toast(msg, type) {
    type = type || 'info';
    const el = document.createElement('div');
    el.className = 'toast toast-' + type;
    el.textContent = msg;
    $('#toastContainer').appendChild(el);
    setTimeout(() => el.remove(), 3000);
  }

  function fmtTime(iso) {
    if (!iso) return '--';
    const d = new Date(iso);
    const h = String(d.getHours()).padStart(2, '0');
    const m = String(d.getMinutes()).padStart(2, '0');
    const s = String(d.getSeconds()).padStart(2, '0');
    return h + ':' + m + ':' + s;
  }

  function fmtLatency(ms) {
    if (ms == null) return '--';
    if (ms < 1) return '<1ms';
    if (ms < 1000) return Math.round(ms) + 'ms';
    return (ms / 1000).toFixed(1) + 's';
  }

  function fmtUptime(sec) {
    if (!sec) return '--';
    if (sec < 60) return sec + 's';
    if (sec < 3600) return Math.floor(sec / 60) + 'm ' + (sec % 60) + 's';
    var h = Math.floor(sec / 3600);
    var m = Math.floor((sec % 3600) / 60);
    return h + 'h ' + m + 'm';
  }

  function fmtNumber(n) {
    if (n == null) return '--';
    if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
    if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
    return String(n);
  }

  function statusClass(code) {
    if (!code || code === 0) return '';
    if (code < 300) return 'status-ok';
    if (code < 400) return 'status-redirect';
    return 'status-error';
  }

  function truncate(str, len) {
    if (!str) return '';
    return str.length > len ? str.substring(0, len) + '...' : str;
  }

  // ========== Tab Navigation ==========
  function initTabs() {
    $$('.tab').forEach(function (tab) {
      tab.addEventListener('click', function () {
        var target = this.dataset.tab;
        $$('.tab').forEach(function (t) { t.classList.remove('active'); });
        $$('.tab-content').forEach(function (c) { c.classList.remove('active'); });
        this.classList.add('active');
        $('#tab-' + target).classList.add('active');

        if (target === 'status') refreshStatus();
      });
    });
  }

  // ========== Script List ==========
  async function loadScripts() {
    try {
      state.scripts = await api.listScripts();
      renderScriptList();
      updateScriptFilterOptions();
      $('#scriptCount').textContent = state.scripts.length + ' scripts';
    } catch (e) {
      toast('Failed to load scripts: ' + e.message, 'error');
    }
  }

  function renderScriptList() {
    var container = $('#scriptList');
    var empty = $('#emptyScripts');

    // 移除旧的脚本项（保留 empty-state）
    container.querySelectorAll('.script-item').forEach(function (el) { el.remove(); });

    if (state.scripts.length === 0) {
      empty.style.display = '';
      return;
    }
    empty.style.display = 'none';

    state.scripts.forEach(function (s) {
      var item = document.createElement('div');
      item.className = 'script-item' + (s.id === state.activeScriptId ? ' active' : '');
      item.dataset.id = s.id;

      var badgeCls = s.has_error ? 'badge-error' : (s.enabled ? 'badge-enabled' : 'badge-disabled');
      var badgeText = s.has_error ? 'Error' : (s.enabled ? 'On' : 'Off');
      var nameCls = s.enabled ? '' : ' disabled';

      item.innerHTML =
        '<div class="script-item-header">' +
          '<span class="script-name' + nameCls + '">' + escapeHtml(s.name || s.id) + '</span>' +
          '<div class="script-actions">' +
            '<span class="toggle ' + (s.enabled ? 'on' : '') + '" data-action="toggle" data-id="' + s.id + '" title="' + (s.enabled ? 'Disable' : 'Enable') + '"></span>' +
            '<button class="btn-icon" data-action="reload" data-id="' + s.id + '" title="Reload">↻</button>' +
            '<button class="btn-icon" data-action="delete" data-id="' + s.id + '" title="Delete">✕</button>' +
          '</div>' +
        '</div>' +
        '<div class="script-meta">' +
          '<span class="script-badge ' + badgeCls + '">' + badgeText + '</span>' +
          '<span>P:' + s.priority + '</span>' +
          '<span>' + s.rule_count + ' rules</span>' +
          '<span>' + s.intercept_count + ' intercepts</span>' +
        '</div>' +
        '<div class="script-stats">Hits: ' + fmtNumber(s.total_hits) + '</div>';

      // 点击选中脚本
      item.addEventListener('click', function (e) {
        // 忽略 action 按钮点击
        if (e.target.dataset.action || e.target.closest('[data-action]')) return;
        selectScript(s.id);
      });

      container.appendChild(item);
    });

    // 绑定 action 事件
    container.querySelectorAll('[data-action]').forEach(function (el) {
      el.addEventListener('click', function (e) {
        e.stopPropagation();
        var action = this.dataset.action;
        var id = this.dataset.id;
        handleScriptAction(action, id);
      });
    });
  }

  async function handleScriptAction(action, id) {
    try {
      switch (action) {
        case 'toggle':
          var script = state.scripts.find(function (s) { return s.id === id; });
          if (script && script.enabled) {
            await api.disableScript(id);
            toast('Disabled: ' + id, 'info');
          } else {
            await api.enableScript(id);
            toast('Enabled: ' + id, 'success');
          }
          break;
        case 'reload':
          await api.reloadScript(id);
          toast('Reloaded: ' + id, 'success');
          break;
        case 'delete':
          if (!confirm('Delete script "' + id + '"?')) return;
          await api.deleteScript(id);
          if (state.activeScriptId === id) {
            state.activeScriptId = null;
            showEditorPlaceholder();
          }
          toast('Deleted: ' + id, 'info');
          break;
      }
      await loadScripts();
    } catch (e) {
      toast('Action failed: ' + e.message, 'error');
    }
  }

  // ========== Script Editor ==========
  async function selectScript(id) {
    if (state.editorDirty && state.activeScriptId) {
      if (!confirm('You have unsaved changes. Discard?')) return;
    }

    state.activeScriptId = id;
    state.editorDirty = false;

    // 更新列表高亮
    $$('.script-item').forEach(function (el) {
      el.classList.toggle('active', el.dataset.id === id);
    });

    try {
      var data = await api.getScriptContent(id);
      showEditor(id, data.content);
    } catch (e) {
      toast('Failed to load script content: ' + e.message, 'error');
    }
  }

  function showEditor(id, content) {
    $('#editorPlaceholder').style.display = 'none';
    $('#editorContainer').style.display = 'flex';
    $('#editorActions').style.display = 'flex';
    $('#editorTitle').textContent = id;

    var textarea = $('#editorTextarea');
    textarea.value = content || '';
    updateLineNumbers();
    hideValidation();
  }

  function showEditorPlaceholder() {
    $('#editorPlaceholder').style.display = '';
    $('#editorContainer').style.display = 'none';
    $('#editorActions').style.display = 'none';
    $('#editorTitle').textContent = 'Select a script to edit';
    hideValidation();
  }

  function updateLineNumbers() {
    var textarea = $('#editorTextarea');
    var lines = textarea.value.split('\n').length;
    var nums = '';
    for (var i = 1; i <= lines; i++) {
      nums += i + '\n';
    }
    $('#lineNumbers').textContent = nums;
  }

  function hideValidation() {
    var bar = $('#validationBar');
    bar.style.display = 'none';
    bar.className = 'validation-bar';
    bar.innerHTML = '';
  }

  function showValidation(result) {
    var bar = $('#validationBar');
    bar.style.display = '';

    if (result.valid) {
      bar.className = 'validation-bar valid';
      bar.innerHTML = '✓ Validation passed';
    } else {
      bar.className = 'validation-bar invalid';
      var html = '';
      (result.errors || []).forEach(function (err) {
        html += '<div class="validation-error">Line ' + (err.Line || '?') + ': ' + escapeHtml(err.Message || err.message || '') + '</div>';
      });
      bar.innerHTML = html || 'Validation failed';
    }
  }

  async function saveScript() {
    if (!state.activeScriptId) return;
    var content = $('#editorTextarea').value;

    try {
      await api.updateScript(state.activeScriptId, content);
      state.editorDirty = false;
      toast('Saved: ' + state.activeScriptId, 'success');
      await loadScripts();
    } catch (e) {
      toast('Save failed: ' + e.message, 'error');
    }
  }

  async function validateScript() {
    var content = $('#editorTextarea').value;
    try {
      var result = await api.validate(content);
      showValidation(result);
    } catch (e) {
      toast('Validate failed: ' + e.message, 'error');
    }
  }

  function initEditor() {
    var textarea = $('#editorTextarea');

    textarea.addEventListener('input', function () {
      state.editorDirty = true;
      updateLineNumbers();
    });

    textarea.addEventListener('scroll', function () {
      $('#lineNumbers').scrollTop = this.scrollTop;
    });

    // Tab key 支持
    textarea.addEventListener('keydown', function (e) {
      if (e.key === 'Tab') {
        e.preventDefault();
        var start = this.selectionStart;
        var end = this.selectionEnd;
        this.value = this.value.substring(0, start) + '  ' + this.value.substring(end);
        this.selectionStart = this.selectionEnd = start + 2;
        state.editorDirty = true;
        updateLineNumbers();
      }
    });

    // Ctrl+S 快捷键
    document.addEventListener('keydown', function (e) {
      if ((e.ctrlKey || e.metaKey) && e.key === 's') {
        e.preventDefault();
        saveScript();
      }
    });

    $('#btnSave').addEventListener('click', saveScript);
    $('#btnValidate').addEventListener('click', validateScript);
  }

  // ========== New Script Dialog ==========
  function initNewScriptDialog() {
    $('#btnNewRule').addEventListener('click', function () {
      $('#modalOverlay').style.display = 'flex';
      $('#newScriptName').value = '';
      $('#newScriptContent').value = '@name ""\n@priority 50\n\n# Add rules here\n';
      $('#newScriptName').focus();
    });

    $('#btnCancelNew').addEventListener('click', function () {
      $('#modalOverlay').style.display = 'none';
    });

    $('#modalOverlay').addEventListener('click', function (e) {
      if (e.target === this) this.style.display = 'none';
    });

    $('#btnConfirmNew').addEventListener('click', async function () {
      var name = $('#newScriptName').value.trim();
      var content = $('#newScriptContent').value;

      if (!name) {
        toast('Please enter a script name', 'error');
        return;
      }

      // 自动填入 @name
      if (content.indexOf('@name') === -1) {
        content = '@name "' + name + '"\n' + content;
      } else {
        content = content.replace(/@name\s+"[^"]*"/, '@name "' + name + '"');
      }

      try {
        await api.createScript(name, content);
        $('#modalOverlay').style.display = 'none';
        toast('Created: ' + name, 'success');
        await loadScripts();
      } catch (e) {
        toast('Create failed: ' + e.message, 'error');
      }
    });
  }

  // ========== Reload All ==========
  function initReloadAll() {
    $('#btnReloadAll').addEventListener('click', async function () {
      try {
        await api.reloadAll();
        toast('All scripts reloaded', 'success');
        await loadScripts();
      } catch (e) {
        toast('Reload all failed: ' + e.message, 'error');
      }
    });
  }

  // ========== SSE Live Logs ==========
  function connectSSE() {
    if (state.sse) {
      state.sse.close();
    }

    state.sse = new EventSource('/api/events');

    state.sse.onopen = function () {
      state.sseConnected = true;
      updateSSEStatus();
    };

    state.sse.onmessage = function (e) {
      try {
        var evt = JSON.parse(e.data);
        addLogEvent(evt);
      } catch (err) {
        // ignore parse errors
      }
    };

    state.sse.onerror = function () {
      state.sseConnected = false;
      updateSSEStatus();
      // EventSource 会自动重连
    };
  }

  function updateSSEStatus() {
    var dot = $('#sseStatus .dot');
    var label = $('#sseStatus .label');
    if (state.sseConnected) {
      dot.className = 'dot connected';
      label.textContent = 'Live';
    } else {
      dot.className = 'dot disconnected';
      label.textContent = 'Disconnected';
    }
  }

  function addLogEvent(evt) {
    if (state.logsPaused) return;

    state.logs.push(evt);
    // 限制日志数量
    if (state.logs.length > 500) {
      state.logs = state.logs.slice(-400);
    }

    if (matchesFilter(evt)) {
      appendLogRow(evt);
    }
    updateLogCount();
  }

  function matchesFilter(evt) {
    var typeFilter = $('#logFilterType').value;
    var scriptFilter = $('#logFilterScript').value;
    var urlFilter = $('#logFilterURL').value.toLowerCase();

    if (typeFilter && evt.type !== typeFilter) return false;
    if (scriptFilter && evt.script !== scriptFilter) return false;
    if (urlFilter && (!evt.url || evt.url.toLowerCase().indexOf(urlFilter) === -1)) return false;
    return true;
  }

  function appendLogRow(evt) {
    var body = $('#logBody');
    var tr = document.createElement('tr');

    var typeClass = 'type-' + (evt.type || 'forward');
    var statClass = statusClass(evt.status);

    tr.innerHTML =
      '<td class="col-time">' + fmtTime(evt.time) + '</td>' +
      '<td class="col-type"><span class="' + typeClass + '">' + escapeHtml(evt.type || '--') + '</span></td>' +
      '<td class="col-method">' + escapeHtml(evt.method || '--') + '</td>' +
      '<td class="col-url" title="' + escapeHtml(evt.url || '') + '">' + escapeHtml(truncate(evt.url || '', 80)) + '</td>' +
      '<td class="col-status ' + statClass + '">' + (evt.status || '--') + '</td>' +
      '<td class="col-latency">' + fmtLatency(evt.latency_ms) + '</td>' +
      '<td class="col-script">' + escapeHtml(truncate(evt.script || '', 16)) + '</td>' +
      '<td class="col-target" title="' + escapeHtml(evt.target || '') + '">' + escapeHtml(truncate(evt.target || '', 24)) + '</td>';

    body.appendChild(tr);

    // 自动滚动到底部
    var wrap = body.closest('.log-table-wrap');
    if (wrap) {
      var isAtBottom = wrap.scrollHeight - wrap.scrollTop - wrap.clientHeight < 100;
      if (isAtBottom) {
        wrap.scrollTop = wrap.scrollHeight;
      }
    }

    // 限制 DOM 行数
    while (body.children.length > 300) {
      body.removeChild(body.firstChild);
    }
  }

  function updateLogCount() {
    $('#logCount').textContent = state.logs.length + ' events';
  }

  function rerenderLogs() {
    var body = $('#logBody');
    body.innerHTML = '';
    state.logs.forEach(function (evt) {
      if (matchesFilter(evt)) appendLogRow(evt);
    });
    updateLogCount();
  }

  function initLogs() {
    $('#btnTogglePause').addEventListener('click', function () {
      state.logsPaused = !state.logsPaused;
      this.textContent = state.logsPaused ? '▶ Resume' : '⏸ Pause';
      this.classList.toggle('btn-primary', state.logsPaused);
    });

    $('#btnClearLogs').addEventListener('click', function () {
      state.logs = [];
      $('#logBody').innerHTML = '';
      updateLogCount();
    });

    // 过滤器变化时重新渲染
    ['logFilterType', 'logFilterScript', 'logFilterURL'].forEach(function (id) {
      var el = $('#' + id);
      el.addEventListener('change', rerenderLogs);
      el.addEventListener('input', rerenderLogs);
    });
  }

  function updateScriptFilterOptions() {
    var select = $('#logFilterScript');
    var current = select.value;
    // 清除旧选项（保留 "All Scripts"）
    while (select.options.length > 1) select.remove(1);
    state.scripts.forEach(function (s) {
      var opt = document.createElement('option');
      opt.value = s.name || s.id;
      opt.textContent = s.name || s.id;
      select.appendChild(opt);
    });
    select.value = current;
  }

  // ========== Status Tab ==========
  async function refreshStatus() {
    try {
      var status = await api.getStatus();
      var debug = await api.getDebugStatus();

      $('#statUptime').textContent = fmtUptime(debug.uptime_sec);
      $('#statTotalReqs').textContent = fmtNumber(debug.total_requests);
      $('#statTotalErrs').textContent = fmtNumber(debug.total_errors);

      if (debug.conn_pool) {
        $('#statMitmActive').textContent = debug.conn_pool.active_mitm;
        $('#statMitmEvicted').textContent = debug.conn_pool.total_evicted;
      }

      if (status.rules) {
        $('#statScripts').textContent = status.rules.enabled_scripts + '/' + status.rules.total_scripts;
        $('#statRules').textContent = status.rules.total_rules;
        $('#statHits').textContent = fmtNumber(status.rules.total_hits);
      }

      // Update uptime in header
      $('#uptimeValue').textContent = '⏱ ' + fmtUptime(debug.uptime_sec);

      // Top Paths
      var tbody = $('#topPathsBody');
      tbody.innerHTML = '';
      (debug.top_paths || []).slice(0, 20).forEach(function (p) {
        var tr = document.createElement('tr');
        tr.innerHTML =
          '<td>' + escapeHtml(p.path || '') + '</td>' +
          '<td>' + p.count + '</td>' +
          '<td>' + (p.avg_ms != null ? p.avg_ms.toFixed(1) : '--') + '</td>' +
          '<td class="' + (p.max_ms > 3000 ? 'stat-warn' : '') + '">' + (p.max_ms != null ? p.max_ms.toFixed(0) : '--') + '</td>' +
          '<td class="' + (p.errors > 0 ? 'stat-error' : 'stat-ok') + '">' + p.errors + '</td>';
        tbody.appendChild(tr);
      });
      if (!debug.top_paths || debug.top_paths.length === 0) {
        tbody.innerHTML = '<tr><td colspan="5" style="color:var(--text-muted);text-align:center;">No data</td></tr>';
      }
    } catch (e) {
      // Silently handle status fetch errors
    }
  }

  // ========== Initialization ==========
  function init() {
    initTabs();
    initEditor();
    initNewScriptDialog();
    initReloadAll();
    initLogs();

    // Load data
    loadScripts();
    connectSSE();

    // Periodic refresh
    setInterval(function () {
      loadScripts();
    }, 10000);

    setInterval(function () {
      if ($('#tab-status').classList.contains('active')) {
        refreshStatus();
      }
    }, 5000);

    // Initial status load for header uptime
    refreshStatus();
  }

  // Start app when DOM ready
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
