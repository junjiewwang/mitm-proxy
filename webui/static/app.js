// ========================================
// Proxy Manager — Frontend Application
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
    debouncing: false,
  };

  // ========== API Layer ==========
  const api = {
    async listScripts() {
      const resp = await fetch('/api/rules');
      const data = await resp.json();
      return data.scripts || [];
    },
    async getScriptContent(id) {
      const resp = await fetch(`/api/rules/${id}/content`);
      return resp.json();
    },
    async createScript(name, content) {
      const resp = await fetch('/api/rules', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, content }),
      });
      if (!resp.ok) { const err = await resp.json(); throw new Error(err.error || 'Create failed'); }
      return resp.json();
    },
    async updateScript(id, content) {
      const resp = await fetch(`/api/rules/${id}`, {
        method: 'PUT', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
      });
      if (!resp.ok) { const err = await resp.json(); throw new Error(err.error || 'Update failed'); }
      return resp.json();
    },
    async deleteScript(id) {
      const resp = await fetch(`/api/rules/${id}`, { method: 'DELETE' });
      if (!resp.ok) throw new Error('Delete failed');
    },
    async enableScript(id) {
      const resp = await fetch(`/api/rules/${id}/enable`, { method: 'POST' });
      if (!resp.ok) { const err = await resp.json(); throw new Error(err.error || 'Enable failed'); }
    },
    async disableScript(id) {
      const resp = await fetch(`/api/rules/${id}/disable`, { method: 'POST' });
      if (!resp.ok) { const err = await resp.json(); throw new Error(err.error || 'Disable failed'); }
    },
    async reloadScript(id) {
      await fetch(`/api/rules/${id}/reload`, { method: 'POST' });
    },
    async reloadAll() {
      await fetch('/api/rules/reload-all', { method: 'POST' });
    },
    async validate(content) {
      const resp = await fetch('/api/rules/validate', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ content }),
      });
      return resp.json();
    },
    async debugScript(id, method, path, body, dryRun) {
      const resp = await fetch(`/api/rules/${id}/debug`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ method, path, body, dry_run: dryRun }),
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
    async getDiagnostics() {
      const resp = await fetch('/api/diagnostics');
      return resp.json();
    },
  };

  // ========== Utilities ==========
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
    return [d.getHours(), d.getMinutes(), d.getSeconds()].map(v => String(v).padStart(2, '0')).join(':');
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
    return Math.floor(sec / 3600) + 'h ' + Math.floor((sec % 3600) / 60) + 'm';
  }

  function fmtNumber(n) {
    if (n == null) return '--';
    if (n >= 1000000) return (n / 1000000).toFixed(1) + 'M';
    if (n >= 1000) return (n / 1000).toFixed(1) + 'K';
    return String(n);
  }

  function statusClass(code) {
    if (!code || code === 0) return '';
    return code < 300 ? 'status-ok' : code < 400 ? 'status-redirect' : 'status-error';
  }

  function truncate(str, len) {
    if (!str) return '';
    return str.length > len ? str.substring(0, len) + '...' : str;
  }

  // ========== Tab Navigation ==========
  function initTabs() {
    $$('.tab').forEach(tab => {
      tab.addEventListener('click', function () {
        const target = this.dataset.tab;
        $$('.tab').forEach(t => t.classList.remove('active'));
        $$('.tab-content').forEach(c => c.classList.remove('active'));
        this.classList.add('active');
        $('#tab-' + target).classList.add('active');
        if (target === 'status') { refreshStatus(); refreshDiagnostics(); }
        if (target === 'debug') initDebugTab();
      });
    });
  }

  // ========== Syntax Highlighting ==========
  function highlightRules(text) {
    let inConfig = false, inScript = false;
    const lines = text.split('\n');
    const result = [];

    for (let i = 0; i < lines.length; i++) {
      const raw = lines[i];
      const line = raw.trimStart();
      const indent = raw.length - line.length;
      const sp = ' '.repeat(indent);

      if (!inConfig && !inScript) {
        if (line.startsWith('#') || line === '') {
          result.push(sp + '<span class="hl-comment">' + escapeHtml(line || ' ') + '</span>');
        } else if (line.startsWith('@config')) {
          inConfig = true;
          result.push(sp + '<span class="hl-atsection">' + escapeHtml(line) + '</span>');
        } else if (line.startsWith('@script')) {
          inScript = true;
          result.push(sp + '<span class="hl-atsection">' + escapeHtml(line) + '</span>');
        } else if (line.startsWith('@')) {
          result.push(sp + '<span class="hl-directive">' + escapeHtml(line) + '</span>');
        } else if (line.startsWith('/')) {
          result.push(sp + highlightRegexLine(line));
        } else if (isIPLike(line.split(/\s+/)[0])) {
          result.push(sp + highlightHostsLine(line));
        } else {
          result.push(sp + escapeHtml(line));
        }
      } else if (inConfig) {
        if (line === '@end') {
          inConfig = false;
          result.push(sp + '<span class="hl-atsend">' + escapeHtml(line) + '</span>');
        } else {
          result.push(sp + '<span class="hl-atconfig">' + escapeHtml(line || ' ') + '</span>');
        }
      } else if (inScript) {
        if (line === '@end') {
          inScript = false;
          result.push(sp + '<span class="hl-atsend">' + escapeHtml(line) + '</span>');
        } else {
          result.push(sp + '<span class="hl-js">' + escapeHtml(line || ' ') + '</span>');
        }
      }
    }

    return result.join('\n');
  }

  function highlightRegexLine(line) {
    // /regex/ target [@options]
    const parts = line.split(/\s+/);
    let html = '';
    for (let i = 0; i < parts.length; i++) {
      if (i === 0) html += '<span class="hl-regex">' + escapeHtml(parts[i]) + '</span>';
      else if (parts[i].startsWith('http://') || parts[i].startsWith('https://') || parts[i].startsWith('script://') || parts[i].startsWith('pipeline://'))
        html += ' <span class="hl-target">' + escapeHtml(parts[i]) + '</span>';
      else html += ' ' + escapeHtml(parts[i]);
    }
    return html;
  }

  function highlightHostsLine(line) {
    const parts = line.split(/\s+/);
    let html = '';
    for (let i = 0; i < parts.length; i++) {
      if (i === 0) html += '<span class="hl-host">' + escapeHtml(parts[i]) + '</span>';
      else html += ' <span class="hl-target">' + escapeHtml(parts[i]) + '</span>';
    }
    return html;
  }

  function isIPLike(s) {
    if (!s) return false;
    if (s[0] >= '0' && s[0] <= '9' && s.includes('.')) return true;
    if (s.includes(':') && !s.includes('/')) return true;
    return false;
  }

  // ========== Script List ==========
  async function loadScripts() {
    try {
      state.scripts = await api.listScripts();
      renderScriptList();
      updateScriptFilterOptions();
      updateDebugScriptSelector();
      $('#scriptCount').textContent = state.scripts.length + ' scripts';
    } catch (e) {
      toast('Failed to load scripts: ' + e.message, 'error');
    }
  }

  function renderScriptList() {
    const container = $('#scriptList');
    const empty = $('#emptyScripts');
    container.querySelectorAll('.script-item').forEach(el => el.remove());

    if (state.scripts.length === 0) { empty.style.display = ''; return; }
    empty.style.display = 'none';

    state.scripts.forEach(s => {
      const item = document.createElement('div');
      item.className = 'script-item' + (s.id === state.activeScriptId ? ' active' : '');
      item.dataset.id = s.id;
      const badgeCls = s.has_error ? 'badge-error' : (s.enabled ? 'badge-enabled' : 'badge-disabled');
      const badgeText = s.has_error ? 'Error' : (s.enabled ? 'On' : 'Off');

      item.innerHTML =
        '<div class="script-item-header">' +
          '<span class="script-name' + (s.enabled ? '' : ' disabled') + '">' + escapeHtml(s.name || s.id) + '</span>' +
          '<div class="script-actions">' +
            '<span class="toggle ' + (s.enabled ? 'on' : '') + '" data-action="toggle" data-id="' + s.id + '" title="' + (s.enabled ? 'Disable' : 'Enable') + '"></span>' +
            '<button class="btn-icon" data-action="reload" data-id="' + s.id + '" title="Reload">↻</button>' +
            '<button class="btn-icon" data-action="delete" data-id="' + s.id + '" title="Delete">✕</button>' +
          '</div>' +
        '</div>' +
        '<div class="script-meta"><span class="script-badge ' + badgeCls + '">' + badgeText + '</span>' +
          '<span>P:' + s.priority + '</span><span>' + s.rule_count + ' rules</span>' +
          '<span>' + s.intercept_count + ' intercepts</span></div>' +
        '<div class="script-stats">Hits: ' + fmtNumber(s.total_hits) + '</div>';

      item.addEventListener('click', function (e) {
        if (e.target.dataset.action || e.target.closest('[data-action]')) return;
        selectScript(s.id);
      });
      container.appendChild(item);
    });

    container.querySelectorAll('[data-action]').forEach(el => {
      el.addEventListener('click', function (e) {
        e.stopPropagation();
        handleScriptAction(this.dataset.action, this.dataset.id);
      });
    });
  }

  async function handleScriptAction(action, id) {
    try {
      switch (action) {
        case 'toggle':
          const script = state.scripts.find(s => s.id === id);
          if (script && script.enabled) { await api.disableScript(id); toast('Disabled: ' + id, 'info'); }
          else { await api.enableScript(id); toast('Enabled: ' + id, 'success'); }
          break;
        case 'reload': await api.reloadScript(id); toast('Reloaded: ' + id, 'success'); break;
        case 'delete':
          if (!confirm('Delete script "' + id + '"?')) return;
          await api.deleteScript(id);
          if (state.activeScriptId === id) { state.activeScriptId = null; showEditorPlaceholder(); }
          toast('Deleted: ' + id, 'info'); break;
      }
      await loadScripts();
    } catch (e) { toast('Action failed: ' + e.message, 'error'); }
  }

  // ========== Editor ==========
  async function selectScript(id) {
    if (state.editorDirty && state.activeScriptId) {
      if (!confirm('You have unsaved changes. Discard?')) return;
    }
    state.activeScriptId = id;
    state.editorDirty = false;
    $$('.script-item').forEach(el => el.classList.toggle('active', el.dataset.id === id));
    try {
      const data = await api.getScriptContent(id);
      showEditor(id, data.content);
    } catch (e) { toast('Failed to load script: ' + e.message, 'error'); }
  }

  function showEditor(id, content) {
    $('#editorPlaceholder').style.display = 'none';
    $('#editorContainer').style.display = 'flex';
    $('#editorActions').style.display = 'flex';
    $('#editorTitle').textContent = id;
    $('#editorTextarea').value = content || '';
    updateLineNumbers();
    updateHighlightOverlay(content);
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
    const lines = $('#editorTextarea').value.split('\n').length;
    let nums = '';
    for (let i = 1; i <= lines; i++) nums += i + '\n';
    $('#lineNumbers').textContent = nums;
  }

  function updateHighlightOverlay(text) {
    // Update line numbers with context coloring
    const lines = text.split('\n');
    const lineNumbers = $('#lineNumbers');
    if (!lineNumbers) return;

    let inConfig = false, inScript = false;
    let numsHtml = '';
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i].trim();
      if (!inConfig && !inScript) {
        if (line.startsWith('@config')) inConfig = true;
        else if (line.startsWith('@script')) inScript = true;
      } else if (inConfig && line === '@end') inConfig = false;
      else if (inScript && line === '@end') inScript = false;

      let cls = '';
      if (inConfig) cls = 'ln-config';
      else if (inScript) cls = 'ln-script';
      numsHtml += '<span class="' + cls + '">' + (i + 1) + '</span>\n';
    }
    lineNumbers.innerHTML = numsHtml;
  }

  function hideValidation() {
    const bar = $('#validationBar');
    bar.style.display = 'none';
    bar.className = 'validation-bar';
    bar.innerHTML = '';
  }

  function showValidation(result) {
    const bar = $('#validationBar');
    bar.style.display = '';
    if (result.valid) {
      bar.className = 'validation-bar valid';
      bar.innerHTML = '✓ Validation passed';
    } else {
      bar.className = 'validation-bar invalid';
      let html = '';
      (result.errors || []).forEach(err => {
        html += '<div class="validation-error">Line ' + (err.Line || '?') + ': ' + escapeHtml(err.Message || '') + '</div>';
      });
      bar.innerHTML = html || 'Validation failed';
    }
  }

  async function saveScript() {
    if (!state.activeScriptId) return;
    const content = $('#editorTextarea').value;
    try {
      await api.updateScript(state.activeScriptId, content);
      state.editorDirty = false;
      updateHighlightOverlay(content);
      toast('Saved: ' + state.activeScriptId, 'success');
      await loadScripts();
    } catch (e) { toast('Save failed: ' + e.message, 'error'); }
  }

  async function validateScript() {
    const content = $('#editorTextarea').value;
    try { const res = await api.validate(content); showValidation(res); }
    catch (e) { toast('Validate failed: ' + e.message, 'error'); }
  }

  function initEditor() {
    const ta = $('#editorTextarea');
    ta.addEventListener('input', function () {
      state.editorDirty = true;
      updateLineNumbers();
      if (!state.debouncing) {
        state.debouncing = true;
        setTimeout(() => { updateHighlightOverlay(ta.value); state.debouncing = false; }, 300);
      }
    });
    ta.addEventListener('scroll', () => { $('#lineNumbers').scrollTop = ta.scrollTop; });
    ta.addEventListener('keydown', function (e) {
      if (e.key === 'Tab') {
        e.preventDefault();
        const s = this.selectionStart;
        this.value = this.value.substring(0, s) + '  ' + this.value.substring(this.selectionEnd);
        this.selectionStart = this.selectionEnd = s + 2;
        state.editorDirty = true;
        updateLineNumbers();
      }
    });
    document.addEventListener('keydown', function (e) {
      if ((e.ctrlKey || e.metaKey) && e.key === 's') { e.preventDefault(); saveScript(); }
    });
    $('#btnSave').addEventListener('click', saveScript);
    $('#btnValidate').addEventListener('click', validateScript);
  }

  // ========== New Script Dialog ==========
  function initNewScriptDialog() {
    $('#btnNewRule').addEventListener('click', () => {
      $('#modalOverlay').style.display = 'flex';
      $('#newScriptName').value = '';
      $('#newScriptContent').value = '@name ""\n@priority 50\n\n# Add rules here\n';
      $('#newScriptName').focus();
    });
    $('#btnCancelNew').addEventListener('click', () => { $('#modalOverlay').style.display = 'none'; });
    $('#modalOverlay').addEventListener('click', function (e) { if (e.target === this) this.style.display = 'none'; });
    $('#btnConfirmNew').addEventListener('click', async () => {
      let name = $('#newScriptName').value.trim();
      let content = $('#newScriptContent').value;
      if (!name) { toast('Enter a script name', 'error'); return; }
      if (!content.includes('@name')) content = '@name "' + name + '"\n' + content;
      else content = content.replace(/@name\s+"[^"]*"/, '@name "' + name + '"');
      try {
        await api.createScript(name, content);
        $('#modalOverlay').style.display = 'none';
        toast('Created: ' + name, 'success');
        await loadScripts();
      } catch (e) { toast('Create failed: ' + e.message, 'error'); }
    });
  }

  function initReloadAll() {
    $('#btnReloadAll').addEventListener('click', async () => {
      try { await api.reloadAll(); toast('All reloaded', 'success'); await loadScripts(); }
      catch (e) { toast('Reload failed: ' + e.message, 'error'); }
    });
  }

  // ========== SSE Live Logs ==========
  function connectSSE() {
    if (state.sse) state.sse.close();
    state.sse = new EventSource('/api/events');
    state.sse.onopen = () => { state.sseConnected = true; updateSSEStatus(); };
    state.sse.onmessage = function (e) {
      try { addLogEvent(JSON.parse(e.data)); } catch (err) {}
    };
    state.sse.onerror = () => { state.sseConnected = false; updateSSEStatus(); };
  }

  function updateSSEStatus() {
    const dot = $('#sseStatus .dot');
    const label = $('#sseStatus .label');
    if (state.sseConnected) { dot.className = 'dot connected'; label.textContent = 'Live'; }
    else { dot.className = 'dot disconnected'; label.textContent = 'DC'; }
  }

  function addLogEvent(evt) {
    if (state.logsPaused) return;
    state.logs.push(evt);
    if (state.logs.length > 500) state.logs = state.logs.slice(-400);
    if (matchesFilter(evt)) appendLogRow(evt);
    updateLogCount();
  }

  function matchesFilter(evt) {
    const tf = $('#logFilterType').value, sf = $('#logFilterScript').value, uf = $('#logFilterURL').value.toLowerCase();
    if (tf && evt.type !== tf) return false;
    if (sf && evt.script !== sf) return false;
    if (uf && (!evt.url || evt.url.toLowerCase().indexOf(uf) === -1)) return false;
    return true;
  }

  function appendLogRow(evt) {
    const tbody = $('#logBody');
    const tr = document.createElement('tr');
    tr.innerHTML =
      '<td class="col-time">' + fmtTime(evt.time) + '</td>' +
      '<td class="col-type"><span class="type-' + (evt.type || 'forward') + '">' + escapeHtml(evt.type || '--') + '</span></td>' +
      '<td class="col-method">' + escapeHtml(evt.method || '--') + '</td>' +
      '<td class="col-url" title="' + escapeHtml(evt.url || '') + '">' + escapeHtml(truncate(evt.url || '', 80)) + '</td>' +
      '<td class="col-status ' + statusClass(evt.status) + '">' + (evt.status || '--') + '</td>' +
      '<td class="col-latency">' + fmtLatency(evt.latency_ms) + '</td>' +
      '<td class="col-script">' + escapeHtml(truncate(evt.script || '', 16)) + '</td>' +
      '<td class="col-target" title="' + escapeHtml(evt.target || '') + '">' + escapeHtml(truncate(evt.target || '', 24)) + '</td>';
    tbody.appendChild(tr);
    const wrap = tbody.closest('.log-table-wrap');
    if (wrap && (wrap.scrollHeight - wrap.scrollTop - wrap.clientHeight < 100)) wrap.scrollTop = wrap.scrollHeight;
    while (tbody.children.length > 300) tbody.removeChild(tbody.firstChild);
  }

  function updateLogCount() { $('#logCount').textContent = state.logs.length + ' events'; }

  function rerenderLogs() {
    $('#logBody').innerHTML = '';
    state.logs.forEach(evt => { if (matchesFilter(evt)) appendLogRow(evt); });
    updateLogCount();
  }

  function initLogs() {
    $('#btnTogglePause').addEventListener('click', function () {
      state.logsPaused = !state.logsPaused;
      this.textContent = state.logsPaused ? '▶ Resume' : '⏸ Pause';
      this.classList.toggle('btn-primary', state.logsPaused);
    });
    $('#btnClearLogs').addEventListener('click', () => { state.logs = []; $('#logBody').innerHTML = ''; updateLogCount(); });
    ['logFilterType', 'logFilterScript', 'logFilterURL'].forEach(id => {
      const el = $('#' + id);
      el.addEventListener('change', rerenderLogs);
      el.addEventListener('input', rerenderLogs);
    });
  }

  function updateScriptFilterOptions() {
    const sel = $('#logFilterScript'), cur = sel.value;
    while (sel.options.length > 1) sel.remove(1);
    state.scripts.forEach(s => sel.appendChild(new Option(s.name || s.id, s.name || s.id)));
    sel.value = cur;
  }

  // ========== Diagnostics ==========
  async function refreshDiagnostics() {
    try {
      const diag = await api.getDiagnostics();
      renderDiagnostics(diag);
    } catch (e) {
      console.error('Diagnostics failed:', e);
      const tbody = $('#diagMatchBody');
      if (tbody) tbody.innerHTML = '<tr><td colspan="7" style="color:var(--red);text-align:center">Error: ' + escapeHtml(e.message) + '</td></tr>';
    }
  }

  function renderDiagnostics(diag) {
    // Script cards
    const cardsEl = $('#diagScriptCards');
    if (!cardsEl) { console.warn('diagScriptCards not found'); return; }
    cardsEl.innerHTML = '';
    (diag.scripts || []).forEach(s => {
      const card = document.createElement('div');
      card.className = 'diag-script-card ' + (s.enabled ? 'enabled' : 'disabled');
      card.innerHTML = '<div class="sc-name">' + (s.enabled ? '🟢 ' : '🔴 ') + escapeHtml(s.name || s.id) + '</div>' +
        '<div class="sc-meta">P:' + s.priority + ' | ' + s.rules + ' rules | ' + s.intercepts + ' int' +
        (s.has_js ? ' | JS' : '') + '</div>' +
        '<div class="sc-hits">Hits: ' + fmtNumber(s.hits) + '</div>';
      cardsEl.appendChild(card);
    });

    // Summary
    const s = diag.summary || {};
    const proxyCount = (diag.proxy_events || []).length;
    const scriptCount = (diag.script_events || []).length;
    $('#diagMatchCount').textContent = proxyCount + ' proxy + ' + scriptCount + ' script';

    // Match table — 显示 proxy 类事件（CDN 静态资源等）
    const tbody = $('#diagMatchBody');
    tbody.innerHTML = '';
    const proxyEvents = (diag.proxy_events || []).reverse();
    if (proxyEvents.length === 0) {
      tbody.innerHTML = '<tr><td colspan="7" class="diag-empty">No proxy matches yet (静态资源 / 路径代理)。Make a request to see results.</td></tr>';
      return;
    }
    proxyEvents.forEach(m => {
      const tr = document.createElement('tr');
      const statCls = m.status < 300 ? 'stat-ok' : 'stat-error';
      tr.innerHTML =
        '<td>' + fmtTime(m.time) + '</td>' +
        '<td>' + escapeHtml(m.method || '--') + '</td>' +
        '<td title="' + escapeHtml(m.url || '') + '">' + escapeHtml(truncate(m.url || '', 40)) + '</td>' +
        '<td class="diag-target" title="' + escapeHtml(m.target || '') + '">→ ' + escapeHtml(truncate(m.target || '', 30)) + '</td>' +
        '<td>' + escapeHtml(m.script || '--') + '</td>' +
        '<td class="' + statCls + '">' + (m.status || '--') + '</td>' +
        '<td>' + fmtLatency(m.latency_ms) + '</td>';
      tbody.appendChild(tr);
    });
  }

// ========== Status Tab ==========
  async function refreshStatus() {
    try {
      const status = await api.getStatus();
      const debug = await api.getDebugStatus();
      $('#statUptime').textContent = fmtUptime(debug.uptime_sec);
      $('#statTotalReqs').textContent = fmtNumber(debug.total_requests);
      $('#statTotalErrs').textContent = fmtNumber(debug.total_errors);
      if (debug.conn_pool) { $('#statMitmActive').textContent = debug.conn_pool.active_mitm; $('#statMitmEvicted').textContent = debug.conn_pool.total_evicted; }
      if (status.rules) { $('#statScripts').textContent = status.rules.enabled_scripts + '/' + status.rules.total_scripts; $('#statRules').textContent = status.rules.total_rules; $('#statHits').textContent = fmtNumber(status.rules.total_hits); }
      $('#uptimeValue').textContent = '⏱ ' + fmtUptime(debug.uptime_sec);
      const tbody = $('#topPathsBody');
      tbody.innerHTML = '';
      (debug.top_paths || []).slice(0, 20).forEach(p => {
        const tr = document.createElement('tr');
        tr.innerHTML = '<td>' + escapeHtml(p.path || '') + '</td><td>' + p.count + '</td><td>' + (p.avg_ms != null ? p.avg_ms.toFixed(1) : '--') + '</td><td class="' + (p.max_ms > 3000 ? 'stat-warn' : '') + '">' + (p.max_ms != null ? p.max_ms.toFixed(0) : '--') + '</td><td class="' + (p.errors > 0 ? 'stat-error' : 'stat-ok') + '">' + p.errors + '</td>';
        tbody.appendChild(tr);
      });
      if (!debug.top_paths || debug.top_paths.length === 0) tbody.innerHTML = '<tr><td colspan="5" style="color:var(--text-muted);text-align:center;">No data</td></tr>';
    } catch (e) {}
  }

  // ========== Debug Tab ==========
  function updateDebugScriptSelector() {
    const sel = $('#debugScriptId');
    if (!sel) return;
    const cur = sel.value;
    sel.innerHTML = '';
    state.scripts.forEach(s => {
      sel.appendChild(new Option(s.name || s.id, s.id));
    });
    if (cur) sel.value = cur;
  }

  function initDebugTab() {
    if (!state.scripts.length) {
      loadScripts();
    }
  }

  async function runDebug() {
    const scriptId = $('#debugScriptId').value;
    if (!scriptId) { toast('Select a script first', 'error'); return; }
    const method = $('#debugMethod').value;
    const path = $('#debugPath').value.trim();
    const body = $('#debugBody').value;
    const dryRun = $('#debugDryRun').checked;
    if (!path) { toast('Enter a request path', 'error'); return; }

    const stepsEl = $('#debugSteps');
    stepsEl.innerHTML = '<div class="debug-empty">⏳ Running debug...</div>';

    try {
      const result = await api.debugScript(scriptId, method, path, body, dryRun);
      renderDebugSteps(result);
    } catch (e) {
      stepsEl.innerHTML = '<div class="debug-empty" style="color:var(--red)">' + escapeHtml('Error: ' + e.message) + '</div>';
    }
  }

  function renderDebugSteps(result) {
    const stepsEl = $('#debugSteps');
    stepsEl.innerHTML = '';

    if (result.total_elapsed_ms != null) {
      const header = document.createElement('div');
      header.style.cssText = 'font-size:11px;color:var(--text-muted);margin-bottom:8px;text-align:right;';
      header.textContent = 'Total: ' + result.total_elapsed_ms.toFixed(1) + 'ms';
      stepsEl.appendChild(header);
    }

    (result.steps || []).forEach((step, i) => {
      const div = document.createElement('div');
      div.className = 'debug-step';

      let badgeCls = 'ok', badgeText = 'OK';
      if (step.error) { badgeCls = 'error'; badgeText = 'ERROR'; }
      else if (step.skipped) { badgeCls = 'skip'; badgeText = 'SKIP'; }

      div.innerHTML =
        '<div class="debug-step-header">' +
          '<span class="stage-badge ' + badgeCls + '">' + badgeText + '</span>' +
          '<span class="stage-name">' + escapeHtml(step.stage) + '</span>' +
          '<span class="stage-time">' + (step.elapsed_ms != null ? step.elapsed_ms.toFixed(2) + 'ms' : '') + '</span>' +
        '</div>';

      const body = document.createElement('div');
      body.className = 'debug-step-body';

      if (step.error) {
        body.innerHTML = '<div class="error-msg">' + escapeHtml(step.error) + '</div>';
      } else if (step.output && typeof step.output === 'object' && !Array.isArray(step.output)) {
        let html = '';
        if (step.output.script_id) html += '<div style="font-size:11px;color:var(--accent)">Script: ' + escapeHtml(step.output.script_id) + '</div>';
        if (step.output.rule) html += '<div style="font-size:11px;color:var(--text-secondary)">Rule: ' + escapeHtml(step.output.rule) + '</div>';
        if (step.output.backendURL) html += '<div style="font-size:11px;color:var(--green)">Backend: ' + escapeHtml(step.output.backendURL) + '</div>';
        if (step.output.would_send_to) html += '<div style="font-size:11px;color:var(--yellow)">Would POST → ' + escapeHtml(step.output.would_send_to) + '</div>';
        if (step.output.body_preview) html += '<pre>' + escapeHtml(step.output.body_preview) + '</pre>';
        if (step.output.finalRespPreview) html += '<pre>' + escapeHtml(step.output.finalRespPreview) + '</pre>';
        if (step.output.backendRequest) html += '<pre>' + escapeHtml(JSON.stringify(step.output.backendRequest, null, 2)) + '</pre>';
        if (step.output.status_code) html += '<div style="font-size:11px;color:var(--text-secondary)">Status: ' + step.output.status_code + '</div>';
        if (step.output.note) html += '<div style="font-size:11px;color:var(--text-muted)">' + escapeHtml(step.output.note) + '</div>';
        body.innerHTML = html;
      } else if (step.output) {
        body.innerHTML = '<pre>' + escapeHtml(typeof step.output === 'string' ? step.output : JSON.stringify(step.output, null, 2)) + '</pre>';
      }

      // Console logs
      if (step.console_logs && step.console_logs.length > 0) {
        const logsDiv = document.createElement('div');
        logsDiv.style.cssText = 'margin-top:6px;border-top:1px solid var(--border-light);padding-top:6px;';
        step.console_logs.forEach(log => {
          const cls = log.startsWith('[warn]') ? 'warn' : log.startsWith('[error]') ? 'error' : 'log';
          logsDiv.innerHTML += '<div class="console-log ' + cls + '">' + escapeHtml(log) + '</div>';
        });
        body.appendChild(logsDiv);
      }

      div.appendChild(body);
      stepsEl.appendChild(div);
    });
  }

  function initDebugActions() {
    $('#btnDebugRun').addEventListener('click', runDebug);
  }

  // ========== Initialization ==========
  function init() {
    initTabs();
    initEditor();
    initNewScriptDialog();
    initReloadAll();
    initLogs();
    initDebugActions();

    loadScripts();
    connectSSE();

    setInterval(() => loadScripts(), 10000);
    setInterval(() => { if ($('#tab-status').classList.contains('active')) { refreshStatus(); refreshDiagnostics(); } }, 5000);
    const btnDiag = $('#btnRefreshDiag');
    if (btnDiag) btnDiag.addEventListener('click', refreshDiagnostics);
    refreshDiagnostics();
    refreshStatus();
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
