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
    sseConnected: false,
    sse: null,
    debouncing: false,
    requests: [],
    reqPaused: false,
    reqDetailId: null,
    clearedAt: null,
    treeView: true,           // 树状分组视图
    treeGroups: {},           // {hostname: {rows: [], expanded: true}}
    lastErrors: [],
    lastWarnings: [],
  };

  // ========== API Layer ==========
  const api = {
    async listScripts() {
      const resp = await fetch('/api/rules');
      return resp.json();
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
    async debugScript(id, method, path, body, dryRun, headers, cookie) {
      const resp = await fetch(`/api/rules/${id}/debug`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ method, path, body, dry_run: dryRun, headers, cookie }),
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
    async getRequests(params) {
      const qs = new URLSearchParams();
      if (params.type) qs.set('type', params.type);
      if (params.script) qs.set('script', params.script);
      if (params.rule_type) qs.set('rule_type', params.rule_type);
      if (params.url) qs.set('url', params.url);
      if (params.status) qs.set('status', params.status);
      qs.set('limit', params.limit || 100);
      qs.set('offset', params.offset || 0);
      const resp = await fetch('/api/requests?' + qs.toString());
      return resp.json();
    },
    async getScripts() {
      const resp = await fetch('/api/scripts');
      return resp.json();
    },
    async replayRequest(method, url, headers, body) {
      const resp = await fetch('/api/replay', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ method, url, headers, body }),
      });
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

  function shortContentType(ct) {
    if (!ct) return '--';
    var m = ct.match(/application\/json/i); if (m) return 'json';
    m = ct.match(/text\/html/i); if (m) return 'html';
    m = ct.match(/text\/plain/i); if (m) return 'text';
    m = ct.match(/text\/css/i); if (m) return 'css';
    m = ct.match(/(application|text)\/javascript/i); if (m) return 'js';
    m = ct.match(/image\/(\w+)/i); if (m) return 'img';
    m = ct.match(/application\/xml/i); if (m) return 'xml';
    m = ct.match(/application\/x-www-form-urlencoded/i); if (m) return 'form';
    var parts = ct.split('/');
    return parts.length > 1 ? parts[1].substring(0, 8) : ct.substring(0, 8);
  }

  function fmtSize(bytes) {
    if (bytes == null || bytes === 0) return '--';
    if (bytes < 1024) return bytes + 'B';
    if (bytes < 1048576) return (bytes / 1024).toFixed(1) + 'K';
    return (bytes / 1048576).toFixed(1) + 'M';
  }

  function tryFormatJSON(str) {
    try { return JSON.stringify(JSON.parse(str), null, 2); } catch(e) { return str; }
  }

  function extractHostname(url) {
    if (!url) return '(unknown)';
    try {
      var a = document.createElement('a');
      a.href = url;
      return a.hostname || '(unknown)';
    } catch(e) { return '(unknown)'; }
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

  function fmtBytes(bytes) {
    if (bytes == null) return '--';
    if (bytes >= 1024 * 1024 * 1024) return (bytes / (1024 * 1024 * 1024)).toFixed(1) + ' GB';
    if (bytes >= 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
    if (bytes >= 1024) return (bytes / 1024).toFixed(1) + ' KB';
    return bytes + ' B';
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
        if (target === 'status') { refreshStatus(); refreshDiagnostics(); refreshCAStatus(); }
        if (target === 'debug') initDebugTab();
        if (target === 'requests') { initRequestsTab(); loadRequests(); }
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
      const data = await api.listScripts();
      state.scripts = data.scripts || [];
      renderScriptList();
      updateReqScriptFilter();
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
          '<span>' + s.intercept_count + ' int</span>' +
          (s.warning_count > 0 ? '<span class="script-warn">⚠' + s.warning_count + '</span>' : '') +
          '</div>' +
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
      showEditor(id, data.content, data.parse_errors || [], data.parse_warnings || []);
    } catch (e) { toast('Failed to load script: ' + e.message, 'error'); }
  }

  function showEditor(id, content, errors, warnings) {
    $('#editorPlaceholder').style.display = 'none';
    $('#editorContainer').style.display = 'flex';
    $('#editorActions').style.display = 'flex';
    $('#editorTitle').textContent = id;
    $('#editorTextarea').value = content || '';
    state.lastErrors = errors || [];
    state.lastWarnings = warnings || [];
    updateLineNumbers();
    updateHighlightOverlay(content);
    // 自动显示脚本的解析错误/警告（如重复 hosts 映射）
    if ((errors && errors.length > 0) || (warnings && warnings.length > 0)) {
      showValidation({
        valid: (errors || []).length === 0,
        errors: errors || [],
        warnings: warnings || [],
        error_count: (errors || []).length,
        warning_count: (warnings || []).length
      });
    } else {
      hideValidation();
    }
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
    const lines = text.split('\n');
    const lineNumbers = $('#lineNumbers');
    if (!lineNumbers) return;

    // 构建错误/警告行号集合（保存完整 issue 信息用于 tooltip）
    var errorLines = {};
    var warningLines = {};
    (state.lastErrors || []).forEach(function(e) {
      if (e.line) errorLines[e.line] = e;
    });
    (state.lastWarnings || []).forEach(function(w) {
      if (w.line) warningLines[w.line] = w;
    });

    let inConfig = false, inScript = false;
    let numsHtml = '';
    for (let i = 0; i < lines.length; i++) {
      const lineNum = i + 1;
      const line = lines[i].trim();
      const openingConfig = !inScript && line.startsWith('@config');
      const openingScript = !inConfig && line.startsWith('@script');
      const endConfig = inConfig && line === '@end';
      const endScript = inScript && line === '@end';
      // 着色
      let cls = '';
      if (inConfig || openingConfig || endConfig) cls = 'ln-config';
      else if (inScript || openingScript || endScript) cls = 'ln-script';
      else if (line === '' || line.startsWith('#')) cls = 'ln-comment';
      else if (line.startsWith('@intercept')) cls = 'ln-intercept';
      else if (line.startsWith('@name') || line.startsWith('@priority')) cls = 'ln-directive';
      else if (line.startsWith('/')) cls = 'ln-regex';
      else if (isIPLike(line.split(/\s+/)[0])) cls = 'ln-hosts';

      // 错误/警告标注优先级最高
      var issueHtml = '';
      if (errorLines[lineNum]) {
        cls += ' ln-error';
        issueHtml = '<span class="ln-icon ln-icon-err" data-line="' + lineNum + '" data-issue="error" title="">⨯</span>';
      } else if (warningLines[lineNum]) {
        cls += ' ln-warning';
        issueHtml = '<span class="ln-icon ln-icon-warn" data-line="' + lineNum + '" data-issue="warning" title="">⚠</span>';
      }

      numsHtml += '<div class="ln-row ' + cls + '" data-line="' + lineNum + '">' + issueHtml + '<span class="ln-num">' + lineNum + '</span></div>';
      // 切换状态
      if (openingConfig) inConfig = true;
      else if (openingScript) inScript = true;
      else if (endConfig) inConfig = false;
      else if (endScript) inScript = false;
    }
    lineNumbers.innerHTML = numsHtml;

    // 为带图标的行号绑定 hover tooltip
    bindLineNumberTooltips();
  }

  // bindLineNumberTooltips 绑定行号图标的 hover tooltip
  function bindLineNumberTooltips() {
    var lineNumbers = $('#lineNumbers');
    if (!lineNumbers) return;

    // 移除旧 tooltip
    var oldTip = document.getElementById('lnTooltip');
    if (oldTip) oldTip.remove();

    // 创建共享 tooltip 元素
    var tip = document.createElement('div');
    tip.id = 'lnTooltip';
    tip.className = 'ln-tooltip';
    tip.style.display = 'none';
    document.body.appendChild(tip);

    var hideTimer = null;

    function showTip(el) {
      var lineNum = parseInt(el.dataset.line);
      var issueType = el.dataset.issue;
      var issue = issueType === 'error'
        ? (state.lastErrors || []).find(function(e) { return e.line === lineNum; })
        : (state.lastWarnings || []).find(function(w) { return w.line === lineNum; });
      if (!issue) return;

      var sevIcon = issueType === 'error' ? '⨯' : '⚠';
      var sevCls = issueType === 'error' ? 'tip-error' : 'tip-warn';
      var html = '<div class="tip-header ' + sevCls + '">';
      html += '<span class="tip-sev">' + sevIcon + '</span>';
      html += '<span>Line ' + lineNum + '</span>';
      html += '</div>';
      html += '<div class="tip-body">' + escapeHtml(issue.message) + '</div>';

      if (issue.related_line) {
        html += '<div class="tip-related">';
        html += '<div class="tip-related-label">↪ conflicts with Line ' + issue.related_line + ':</div>';
        if (issue.related_text) {
          html += '<code class="tip-related-code">' + escapeHtml(issue.related_text) + '</code>';
        }
        html += '<button class="tip-jump-btn" data-jump-line="' + issue.related_line + '">Jump to Line ' + issue.related_line + ' →</button>';
        html += '</div>';
      }

      if (issue.suggestion) {
        html += '<div class="tip-sug">💡 ' + escapeHtml(issue.suggestion) + '</div>';
      }

      tip.innerHTML = html;
      tip.style.display = 'block';

      // 定位
      var rect = el.getBoundingClientRect();
      var tipRect = tip.getBoundingClientRect();
      var top = rect.top - tipRect.height - 6;
      if (top < 8) top = rect.bottom + 6; // 下方放不下就放下方
      var left = rect.right + 8;
      if (left + tipRect.width > window.innerWidth - 8) left = window.innerWidth - tipRect.width - 8;
      tip.style.top = top + 'px';
      tip.style.left = left + 'px';
    }

    function hideTip() {
      if (hideTimer) clearTimeout(hideTimer);
      hideTimer = setTimeout(function() {
        tip.style.display = 'none';
      }, 120);
    }

    lineNumbers.querySelectorAll('.ln-icon').forEach(function(icon) {
      icon.addEventListener('mouseenter', function() { showTip(this); });
      icon.addEventListener('mouseleave', hideTip);
      // 点击图标直接跳转
      icon.addEventListener('click', function(e) {
        e.stopPropagation();
        var lineNum = parseInt(this.dataset.line);
        if (!isNaN(lineNum)) {
          jumpToLine(lineNum);
          // 如果有 related_line，也跳过去
          var issue = (this.dataset.issue === 'error'
            ? (state.lastErrors || [])
            : (state.lastWarnings || [])).find(function(i) { return i.line === lineNum; });
          if (issue && issue.related_line) {
            setTimeout(function() { jumpToLine(issue.related_line); }, 100);
          }
        }
      });
      // 鼠标移到 tooltip 上保持显示
    });

    // tooltip 自身 hover 不消失
    tip.addEventListener('mouseenter', function() {
      if (hideTimer) { clearTimeout(hideTimer); hideTimer = null; }
    });
    tip.addEventListener('mouseleave', hideTip);
    // tooltip 内的 jump 按钮
    tip.addEventListener('click', function(e) {
      var btn = e.target.closest('.tip-jump-btn');
      if (btn) {
        var ln = parseInt(btn.dataset.jumpLine);
        if (!isNaN(ln)) {
          jumpToLine(ln);
          tip.style.display = 'none';
        }
      }
    });
  }

  function hideValidation() {
    const bar = $('#validationBar');
    bar.style.display = 'none';
    bar.className = 'validation-bar';
    bar.innerHTML = '';
  }

  function showValidation(result) {
    var bar = $('#validationBar');
    if (!bar) return;
    bar.style.display = '';

    var errorCount = result.error_count != null ? result.error_count : (result.errors || []).length;
    var warningCount = result.warning_count != null ? result.warning_count : (result.warnings || []).length;
    var errors = result.errors || [];
    var warnings = result.warnings || [];

    // 没有任何问题：显示纯成功态
    if (errorCount === 0 && warningCount === 0) {
      bar.className = 'validation-bar clean';
      bar.innerHTML = '<span class="v-icon">✓</span> Rules valid — no issues';
      var btnValidate = $('#btnValidate');
      if (btnValidate) btnValidate.textContent = '✓ Validate';
      return;
    }

    // 有问题：始终显示完整列表（不再折叠）
    bar.className = 'validation-bar issues';

    var headerCls = errorCount > 0 ? 'v-header-error' : 'v-header-warn';
    var icon = errorCount > 0 ? '⨯' : '⚠';
    var summary = errorCount > 0
      ? icon + ' ' + errorCount + ' error(s), ' + warningCount + ' warning(s)'
      : icon + ' ' + warningCount + ' warning(s) — rules still valid but check duplicates';

    var html = '<div class="v-header ' + headerCls + '">' + escapeHtml(summary) + '</div>';
    html += '<div class="v-list">';

    // 合并所有 issue 并按行号排序
    var issues = [];
    errors.forEach(function(e) { issues.push({severity: 'error', line: e.line, msg: e.message, sug: e.suggestion, related_line: e.related_line, related_text: e.related_text}); });
    warnings.forEach(function(w) { issues.push({severity: 'warning', line: w.line, msg: w.message, sug: w.suggestion, related_line: w.related_line, related_text: w.related_text}); });
    issues.sort(function(a, b) { return a.line - b.line; });

    issues.forEach(function(iss) {
      var cls = iss.severity === 'error' ? 'v-item-error' : 'v-item-warn';
      var sevIcon = iss.severity === 'error' ? '⨯' : '⚠';
      html += '<div class="v-item ' + cls + '" data-line="' + iss.line + '">';
      html += '<span class="v-sev">' + sevIcon + '</span>';
      html += '<span class="v-line">Line ' + iss.line + '</span>';
      html += '<span class="v-msg">' + escapeHtml(iss.msg) + '</span>';

      // 关联行：显示冲突行号 + 内容 + 跳转链接
      if (iss.related_line) {
        html += '<div class="v-related" data-jump-line="' + iss.related_line + '">';
        html += '<span class="v-related-arrow">↪</span>';
        html += '<span class="v-related-label">conflicts with Line ' + iss.related_line + ':</span>';
        if (iss.related_text) {
          html += '<code class="v-related-code">' + escapeHtml(iss.related_text) + '</code>';
        }
        html += '<button class="v-jump-btn" data-line="' + iss.related_line + '" title="跳转到 Line ' + iss.related_line + '">Jump →</button>';
        html += '</div>';
      }

      if (iss.sug) {
        html += '<div class="v-sug">💡 ' + escapeHtml(iss.sug) + '</div>';
      }
      html += '</div>';
    });

    html += '</div>';
    bar.innerHTML = html;

    // 绑定跳转事件（点击行号条目或 Jump 按钮）
    bar.querySelectorAll('.v-item').forEach(function(el) {
      el.addEventListener('click', function(e) {
        if (e.target.closest('.v-jump-btn')) return; // Jump 按钮自己处理
        jumpToLine(parseInt(this.dataset.line));
      });
    });
    bar.querySelectorAll('.v-jump-btn').forEach(function(btn) {
      btn.addEventListener('click', function(e) {
        e.stopPropagation();
        jumpToLine(parseInt(this.dataset.line));
      });
    });

    // 更新 Validate 按钮 badge
    var btnValidate = $('#btnValidate');
    if (btnValidate) {
      btnValidate.textContent = (errorCount > 0 ? '⨯ ' + errorCount : '⚠ ' + warningCount) + ' Validate';
    }
  }

  // jumpToLine 通用跳转函数（编辑器聚焦 + 滚动 + 行号 gutter 高亮）
  function jumpToLine(lineNum) {
    var ta = $('#editorTextarea');
    if (!ta) return;
    var lines = ta.value.split('\n');
    if (lineNum < 1 || lineNum > lines.length) return;
    var pos = 0;
    for (var i = 0; i < lineNum - 1 && i < lines.length; i++) {
      pos += lines[i].length + 1;
    }
    ta.focus();
    ta.setSelectionRange(pos, pos);
    // 动态读取 textarea 实际行高（与 gutter 保持一致）
    var lh = parseFloat(getComputedStyle(ta).lineHeight);
    if (isNaN(lh) || lh <= 0) lh = 20.8; // fallback
    // scrollTop 用同一行高：gutter 用 JS 同步 scrollTop 即可自然对齐
    ta.scrollTop = Math.max(0, (lineNum - 3) * lh);
    $('#lineNumbers').scrollTop = ta.scrollTop;
    // 临时高亮行号 gutter
    $$('#lineNumbers .ln-jump-flash').forEach(function(n) { n.classList.remove('ln-jump-flash'); });
    var lineEl = $$('#lineNumbers .ln-row').find(function(n) { return parseInt(n.dataset.line) === lineNum; });
    if (lineEl) {
      lineEl.classList.add('ln-jump-flash');
      setTimeout(function() { lineEl.classList.remove('ln-jump-flash'); }, 1500);
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
    try {
      const res = await api.validate(content);
      state.lastErrors = res.errors || [];
      state.lastWarnings = res.warnings || [];
      showValidation(res);
      updateHighlightOverlay(content); // 刷新行号标注（包含错误/警告着色）
    }
    catch (e) { toast('Validate failed: ' + e.message, 'error'); }
  }

  function toggleComment(ta) {
    var text = ta.value, start = ta.selectionStart, end = ta.selectionEnd;
    // 确定受影响的行范围
    var before = text.substring(0, start);
    var sel = text.substring(start, end);
    var after = text.substring(end);
    var lineStart = text.lastIndexOf('\n', start - 1) + 1;
    var lineEnd = text.indexOf('\n', end - 1);
    if (lineEnd === -1) lineEnd = text.length;
    var block = text.substring(lineStart, lineEnd);
    var lines = block.split('\n');
    // 按行为单位操作，保持选区内的行级完整性
    var selStartLine = before.split('\n').length - 1;
    var selEndLine = selStartLine + Math.max(0, (end > lineStart ? text.substring(lineStart, end).split('\n').length - 1 : 0));
    // 获取完整受影响行
    var allLines = text.split('\n');
    var targetLines = [];
    for (var i = selStartLine; i <= selEndLine && i < allLines.length; i++) {
      targetLines.push(i);
    }
    if (targetLines.length === 0) return;
    // 判断是否全部已注释
    var allCommented = targetLines.every(function(li) {
      var ln = allLines[li];
      return ln.trimStart().startsWith('#');
    });
    var newLines = allLines.slice();
    if (allCommented) {
      // 全部取消注释：移除 '# ' 前缀（2字符），精确恢复原始内容
      targetLines.forEach(function(li) {
        var ln = newLines[li];
        var idx = ln.indexOf('# ');
        if (idx >= 0) {
          newLines[li] = ln.substring(0, idx) + ln.substring(idx + 2);
        } else if (ln.trimStart().startsWith('#')) {
          // 备用：仅有 # 无空格
          var i = ln.indexOf('#');
          newLines[li] = ln.substring(0, i) + ln.substring(i + 1);
        }
      });
    } else {
      // 全部注释：每行前添加 '# '（2字符），不修改原始内容
      targetLines.forEach(function(li) {
        var ln = newLines[li];
        if (ln.trim() !== '') {
          newLines[li] = '# ' + ln;
        }
      });
    }
    ta.value = newLines.join('\n');
    // 恢复光标位置
    ta.selectionStart = start;
    ta.selectionEnd = end;
  }

  function initEditor() {
    const ta = $('#editorTextarea');
    let validateTimer;
    ta.addEventListener('input', function () {
      state.editorDirty = true;
      updateLineNumbers();
      if (!state.debouncing) {
        state.debouncing = true;
        setTimeout(() => { updateHighlightOverlay(ta.value); state.debouncing = false; }, 300);
      }
      // 实时验证（debounce 500ms）
      clearTimeout(validateTimer);
      validateTimer = setTimeout(function() {
        if (ta.value.trim()) validateScript();
      }, 500);
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
      // Ctrl+/ / Cmd+/ 切换注释
      if ((e.ctrlKey || e.metaKey) && e.key === '/') {
        e.preventDefault();
        toggleComment(this);
        state.editorDirty = true;
        updateLineNumbers();
        updateHighlightOverlay(this.value);
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

  // ========== SSE Live Events ==========
  function connectSSE() {
    if (state.sse) state.sse.close();
    state.sse = new EventSource('/api/events');
    state.sse.onopen = () => { state.sseConnected = true; updateSSEStatus(); };
    state.sse.onmessage = function (e) {
      try { onReqEvent(JSON.parse(e.data)); } catch (err) {}
    };
    state.sse.onerror = () => { state.sseConnected = false; updateSSEStatus(); };
  }

  function updateSSEStatus() {
    const dot = $('#sseStatus .dot');
    const label = $('#sseStatus .label');
    if (state.sseConnected) { dot.className = 'dot connected'; label.textContent = 'Live'; }
    else { dot.className = 'dot disconnected'; label.textContent = 'DC'; }
  }

  function onReqEvent(evt) {
    if (state.reqPaused) return;
    // Clear 后的旧事件不显示
    if (state.clearedAt && evt.time <= state.clearedAt) return;
    // 仅在 Requests Tab 激活时渲染
    if (!$('#tab-requests') || !$('#tab-requests').classList.contains('active')) return;
    if (!reqMatchesFilter(evt)) return;

    state.requests.unshift(evt);
    if (state.requests.length > 500) state.requests = state.requests.slice(0, 500);

    if (state.treeView) {
      insertReqRowIntoTree(evt);
    } else {
      appendReqRow(evt, true);
    }
    updateReqCount();
  }

  // 将新请求插入到对应分组的组头之后
  function insertReqRowIntoTree(evt) {
    var tbody = $('#reqBody');
    if (!tbody) return;
    var host = extractHostname(evt.url);
    var header = document.querySelector('.tree-group-header[data-tree-host="' + host + '"]');

    if (header) {
      // 找到该组的最后一行（紧跟 header 的连续 .tree-group-row 们）
      var after = header.nextElementSibling;
      while (after && after.classList.contains('tree-group-row') && after.dataset.treeHost === host) {
        after = after.nextElementSibling;
      }
      // 标记行属于该分组
      appendReqRow(evt, false);
      var tr = tbody.lastChild;
      tr.classList.add('tree-group-row');
      tr.dataset.treeHost = host;
      var group = state.treeGroups[host];
      if (group && group.expanded !== false) tr.classList.add('expanded');
      // 移到正确位置（header 之后、下一组之前）
      tbody.insertBefore(tr, after);
    } else {
      // 新域名：创建新分组放在最前
      var placeholder = tbody.firstChild;
      appendReqRow(evt, false);
      var tr2 = tbody.lastChild;
      tr2.classList.add('tree-group-row');
      tr2.dataset.treeHost = host;
      // 简化：暂不创建 header，避免复杂 DOM 操作；fallback 到 prepend
      tbody.insertBefore(tr2, tbody.firstChild);
    }
  }

  function reqMatchesFilter(evt) {
    var tf = $('#reqFilterType'), sf = $('#reqFilterScript'), rf = $('#reqFilterRuleType');
    var st = $('#reqFilterStatus'), uf = $('#reqFilterURL');
    if (tf && tf.value && evt.type !== tf.value) return false;
    if (sf && sf.value && evt.script !== sf.value) return false;
    if (rf && rf.value && evt.rule_type !== rf.value) return false;
    if (st && st.value) {
      var code = evt.status;
      var sv = st.value;
      if (sv === '2xx' && !(code >= 200 && code < 300)) return false;
      if (sv === '3xx' && !(code >= 300 && code < 400)) return false;
      if (sv === '4xx' && !(code >= 400 && code < 500)) return false;
      if (sv === '5xx' && !(code >= 500 && code < 600)) return false;
    }
    if (uf && uf.value && (!evt.url || evt.url.toLowerCase().indexOf(uf.value.toLowerCase()) === -1)) return false;
    return true;
  }

  function appendReqRow(evt, prepend, beforeNode) {
    var tbody = $('#reqBody');
    if (!tbody) return;
    var tr = document.createElement('tr');
    var searchTerm = ($('#reqFilterURL') ? $('#reqFilterURL').value : '').toLowerCase();
    var urlHtml = escapeHtml(truncate(evt.url || '', 80));
    if (searchTerm && evt.url) {
      var escaped = escapeHtml(evt.url);
      var regex = new RegExp('(' + searchTerm.replace(/[.*+?^{}()|[\]\\]/g, '\\$&') + ')', 'gi');
      urlHtml = escaped.replace(regex, '<mark class="req-highlight">$1</mark>');
      urlHtml = truncateHtml(urlHtml, 80);
    }
    var statCls = statusClass(evt.status);
    tr.innerHTML =
      '<td class="col-time">' + fmtTime(evt.time) + '</td>' +
      '<td class="col-type"><span class="type-' + (evt.type || 'forward') + '">' + escapeHtml(evt.type || '--') + '</span></td>' +
      '<td class="col-method">' + escapeHtml(evt.method || '--') + '</td>' +
      '<td class="col-url" title="' + escapeHtml(evt.url || '') + '">' + urlHtml + '</td>' +
      '<td class="col-status ' + statCls + '">' + (evt.status || '--') + '</td>' +
      '<td class="col-ctype" title="' + escapeHtml(evt.content_type || '') + '">' + escapeHtml(shortContentType(evt.content_type)) + '</td>' +
      '<td class="col-size">' + fmtSize(evt.content_length) + '</td>' +
      '<td class="col-latency">' + fmtLatency(evt.latency_ms) + '</td>' +
      '<td class="col-script">' + escapeHtml(truncate(evt.script || '', 16)) + '</td>' +
      '<td class="col-ruletype">' + escapeHtml(evt.rule_type || '--') + '</td>' +
      '<td class="col-target" title="' + escapeHtml(evt.target || '') + '">' + escapeHtml(truncate(evt.target || '', 30)) + '</td>';
    var idx = state.requests.indexOf(evt);
    tr.dataset.idx = idx;
    tr.addEventListener('click', function () { showReqDetail(idx); });
    tr.style.cursor = 'pointer';
    if (beforeNode) {
      tbody.insertBefore(tr, beforeNode);
    } else if (prepend) {
      tbody.insertBefore(tr, tbody.firstChild);
    } else {
      tbody.appendChild(tr);
    }
    // 保持最多 500 行
    while (tbody.children.length > 500) tbody.removeChild(tbody.lastChild);
  }

  function updateReqCount() {
    var el = $('#reqCount');
    if (el) el.textContent = state.requests.length + ' events';
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
    (diag.scripts || []).sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id)).forEach(s => {
      const card = document.createElement('div');
      card.className = 'diag-script-card ' + (s.enabled ? 'enabled' : 'disabled');
      card.innerHTML = '<div class="sc-name">' + (s.enabled ? '🟢 ' : '🔴 ') + escapeHtml(s.name || s.id) + '</div>' +
        '<div class="sc-meta">P:' + s.priority + ' | ' + s.rules + ' rules | ' + s.intercepts + ' int' +
        (s.has_js ? ' | JS' : '') + '</div>' +
        '<div class="sc-hits">Hits: ' + fmtNumber(s.hits) + '</div>';
      cardsEl.appendChild(card);
    });

    // Tab counts
    const proxyCount = (diag.proxy_events || []).length;
    const scriptCount = (diag.script_events || []).length;
    const totalCount = proxyCount + scriptCount;
    $('#diagMatchCount').textContent = totalCount + ' total (' + proxyCount + ' proxy + ' + scriptCount + ' script)';
    // Update tab labels
    $$('.diag-tab').forEach(tab => {
      const t = tab.dataset.tab;
      const n = t === 'proxy' ? proxyCount : t === 'script' ? scriptCount : 0;
      const base = t === 'proxy' ? 'Proxy 代理' : 'Script 脚本';
      tab.textContent = base + ' (' + n + ')';
    });

    renderMatchTable();
  }

  function renderMatchTable() {
    const tbody = $('#diagMatchBody');
    if (!tbody) return;
    tbody.innerHTML = '';

    const activeTab = document.querySelector('.diag-tab.active');
    const tab = activeTab ? activeTab.dataset.tab : 'proxy';
    fetch('/api/diagnostics').then(r => r.json()).then(diag => {
      const events = tab === 'proxy' ? (diag.proxy_events || []).reverse()
                                     : (diag.script_events || []).reverse();
      if (events.length === 0) {
        tbody.innerHTML = '<tr><td colspan="7" class="diag-empty">No ' + tab + ' matches yet</td></tr>';
        return;
      }
      events.forEach(m => {
        const tr = document.createElement('tr');
        const statCls = (m.status >= 200 && m.status < 400) ? 'stat-ok' : 'stat-error';
        // 显示完整 URL，target 也完整
        tr.innerHTML =
          '<td class="time-cell">' + fmtTime(m.time) + '</td>' +
          '<td>' + escapeHtml(m.method || '--') + '</td>' +
          '<td class="url-cell" title="' + escapeHtml(m.url || '') + '">' + escapeHtml(m.url || '--') + '</td>' +
          '<td class="target-cell" title="' + escapeHtml(m.target || '') + '">' + escapeHtml(m.target || '--') + '</td>' +
          '<td>' + escapeHtml(m.script || '--') + '</td>' +
          '<td class="' + statCls + '">' + (m.status || '--') + (m.error ? ' <span class="diag-target">err</span>' : '') + '</td>' +
          '<td>' + fmtLatency(m.latency_ms) + '</td>';
        tbody.appendChild(tr);
      });
    });
  }

  function initDiagTabs() {
    $$('.diag-tab').forEach(tab => {
      tab.addEventListener('click', function () {
        $$('.diag-tab').forEach(t => t.classList.remove('active'));
        this.classList.add('active');
        renderMatchTable();
      });
    });
  }

// ========== Requests Tab ==========
  function getFilterParams() {
    return {
      type: $('#reqFilterType') ? $('#reqFilterType').value : '',
      script: $('#reqFilterScript') ? $('#reqFilterScript').value : '',
      rule_type: $('#reqFilterRuleType') ? $('#reqFilterRuleType').value : '',
      url: $('#reqFilterURL') ? $('#reqFilterURL').value : '',
      status: $('#reqFilterStatus') ? $('#reqFilterStatus').value : '',
    };
  }

  async function loadRequests() {
    try {
      var params = getFilterParams();
      params.limit = 500;
      params.offset = 0;
      var data = await api.getRequests(params);
      state.requests = data.results || [];
      // Clear 后过滤旧数据
      if (state.clearedAt && state.requests.length > 0) {
        state.requests = state.requests.filter(function(evt) { return evt.time > state.clearedAt; });
      }
      renderRequestTable();
    } catch (e) {
      console.error('Load requests failed:', e);
      var tbody = $('#reqBody');
      if (tbody) tbody.innerHTML = '<tr><td colspan="11" style="color:var(--red);text-align:center">Error: ' + escapeHtml(e.message) + '</td></tr>';
    }
  }

  function renderRequestTable() {
    var tbody = $('#reqBody');
    if (!tbody) return;
    tbody.innerHTML = '';

    if (!state.requests || state.requests.length === 0) {
      tbody.innerHTML = '<tr><td colspan="11" style="color:var(--text-muted);text-align:center;padding:20px">No requests yet — waiting for traffic...</td></tr>';
      updateReqCount();
      return;
    }

    if (state.treeView) {
      renderTreeView(tbody);
    } else {
      state.requests.forEach(function (evt, i) {
        appendReqRow(evt, false);
        if (state.reqDetailId === i) {
          var tr = tbody.lastChild;
          if (tr) tr.classList.add('req-selected');
        }
      });
    }

    updateReqCount();
  }

  function renderTreeView(tbody) {
    // 按 hostname 分组（保持首次出现顺序）
    var groups = {};
    var hostOrder = [];
    state.requests.forEach(function(evt, i) {
      var host = extractHostname(evt.url);
      if (!groups[host]) { groups[host] = []; hostOrder.push(host); }
      groups[host].push(i);
    });

    // 计算并存储分组信息
    state.treeGroups = {};

    hostOrder.forEach(function(host) {
      var indices = groups[host];
      var totalMs = 0, count = indices.length;
      indices.forEach(function(i) { totalMs += state.requests[i].latency_ms || 0; });
      var avgMs = Math.round(totalMs / count);

      // 恢复上次折叠状态（默认全部展开）
      var expanded = !state.treeGroups[host] || state.treeGroups[host].expanded !== false;

      // 渲染组头（用 DOM API 而非 innerHTML 避免销毁后续行的 click handler）
      var headerTr = document.createElement('tr');
      headerTr.className = 'tree-group-header';
      headerTr.dataset.treeHost = host;
      var toggleIcon = document.createElement('span');
      toggleIcon.className = 'tree-toggle';
      toggleIcon.textContent = expanded ? '▼' : '▶';
      var hostSpan = document.createElement('span');
      hostSpan.className = 'tree-host';
      hostSpan.textContent = host;
      var countSpan = document.createElement('span');
      countSpan.className = 'tree-meta';
      countSpan.textContent = '(' + count + ')';
      var avgSpan = document.createElement('span');
      avgSpan.className = 'tree-meta';
      avgSpan.style.marginLeft = '12px';
      avgSpan.textContent = 'avg ' + avgMs + 'ms';
      var td = document.createElement('td');
      td.colSpan = 11;
      td.appendChild(toggleIcon);
      td.appendChild(document.createTextNode(' '));
      td.appendChild(hostSpan);
      td.appendChild(document.createTextNode(' '));
      td.appendChild(countSpan);
      td.appendChild(avgSpan);
      headerTr.appendChild(td);

      // 绑定折叠事件（在 appendChild 之后立即绑定，保留 handler）
      headerTr.addEventListener('click', function() {
        toggleGroup(host);
      });

      tbody.appendChild(headerTr);

      // 渲染组内行（appendReqRow 已用 appendChild，handler 不会丢失）
      indices.forEach(function(idx) {
        appendReqRow(state.requests[idx], false);
        var tr = tbody.lastChild;
        tr.classList.add('tree-group-row');
        tr.dataset.treeHost = host;
        if (expanded) tr.classList.add('expanded');
        if (state.reqDetailId === idx) tr.classList.add('req-selected');
      });

      state.treeGroups[host] = { expanded: expanded };
    });
  }

  function toggleGroup(host) {
    var expanded = !state.treeGroups[host].expanded;
    state.treeGroups[host].expanded = expanded;

    // 更新 header 图标
    var header = document.querySelector('.tree-group-header[data-tree-host="' + host + '"]');
    if (header) {
      var toggle = header.querySelector('.tree-toggle');
      if (toggle) toggle.textContent = expanded ? '▼' : '▶';
    }
    // 更新组内行
    document.querySelectorAll('.tree-group-row[data-tree-host="' + host + '"]').forEach(function(tr) {
      tr.classList.toggle('expanded', expanded);
    });
  }

  function truncateHtml(html, maxLen) {
    var div = document.createElement('div');
    div.innerHTML = html;
    var text = div.textContent || '';
    if (text.length <= maxLen) return html;
    return escapeHtml(text.substring(0, maxLen) + '...');
  }

  function renderHeaders(headers) {
    if (!headers || Object.keys(headers).length === 0) return '<div style="color:var(--text-muted);padding:8px">No headers</div>';
    var html = '<table class="header-table">';
    Object.keys(headers).forEach(function(k) {
      html += '<tr><td class="hk">' + escapeHtml(k) + '</td><td class="hv">' + escapeHtml(headers[k]) + '</td></tr>';
    });
    html += '</table>';
    return html;
  }

  function showReqDetail(idx) {
    var evt = state.requests[idx];
    if (!evt) return;
    state.reqDetailId = idx;

    var panel = $('#reqDetailPanel');
    if (!panel) return;
    panel.style.display = 'flex';

    $('#reqDetailTitle').textContent = (evt.method || '?') + ' ' + truncate(evt.url || '', 50);

    // Overview tab
    $('#dt-Overview').innerHTML =
      '<div class="req-detail-grid">' +
        '<div class="req-detail-row"><span class="rd-label">Time</span><span class="rd-value">' + escapeHtml(evt.time || '--') + '</span></div>' +
        '<div class="req-detail-row"><span class="rd-label">Type</span><span class="rd-value type-' + (evt.type || 'forward') + '">' + escapeHtml(evt.type || '--') + '</span></div>' +
        '<div class="req-detail-row"><span class="rd-label">Method</span><span class="rd-value">' + escapeHtml(evt.method || '--') + '</span></div>' +
        '<div class="req-detail-row"><span class="rd-label">URL</span><span class="rd-value url-full">' + escapeHtml(evt.url || '--') + '</span></div>' +
        '<div class="req-detail-row"><span class="rd-label">Status</span><span class="rd-value ' + statusClass(evt.status) + '">' + (evt.status || '--') + '</span></div>' +
        '<div class="req-detail-row"><span class="rd-label">Content-Type</span><span class="rd-value">' + escapeHtml(evt.content_type || '--') + '</span></div>' +
        '<div class="req-detail-row"><span class="rd-label">Size</span><span class="rd-value">' + fmtSize(evt.content_length) + '</span></div>' +
        '<div class="req-detail-row"><span class="rd-label">Latency</span><span class="rd-value">' + fmtLatency(evt.latency_ms) + '</span></div>' +
        '<div class="req-detail-row"><span class="rd-label">Script</span><span class="rd-value">' + escapeHtml(evt.script || '--') + '</span></div>' +
        '<div class="req-detail-row"><span class="rd-label">Rule Type</span><span class="rd-value">' + escapeHtml(evt.rule_type || '--') + '</span></div>' +
        '<div class="req-detail-row"><span class="rd-label">Target</span><span class="rd-value target-full">' + escapeHtml(evt.target || '--') + '</span></div>' +
        (evt.error ? '<div class="req-detail-row"><span class="rd-label">Error</span><span class="rd-value status-error">' + escapeHtml(evt.error) + '</span></div>' : '') +
      '</div>';

    // Request tab
    var reqBodyHtml = evt.req_body ? '<pre class="body-pre">' + escapeHtml(tryFormatJSON(evt.req_body)) + '</pre>'
                                   : '<div class="empty-body">(no body)</div>';
    $('#dt-Request').innerHTML =
      '<div class="detail-section"><h4>Request Headers</h4>' + renderHeaders(evt.req_headers) + '</div>' +
      '<div class="detail-section"><h4>Request Body</h4>' + reqBodyHtml + '</div>';

    // Backend tab（仅 script 事件且有 backend_url 时显示）
    var backendUrl = evt.backend_url || '';
    var backendReq = evt.backend_request || '';
    var backendTab = $('#reqDtabBackend');
    if (backendTab) {
      backendTab.style.display = backendUrl ? '' : 'none';
    }
    if (backendUrl) {
      var backendReqHtml = backendReq ? '<pre class="body-pre">' + escapeHtml(tryFormatJSON(backendReq)) + '</pre>'
                                       : '<div class="empty-body">(no body)</div>';
      var origReqHtml = evt.req_body ? '<pre class="body-pre">' + escapeHtml(tryFormatJSON(evt.req_body)) + '</pre>'
                                     : '<div class="empty-body">(no body)</div>';
      // 两栏对比布局：顶部 Target URL，下面左右两栏（原请求 vs 脚本重写后）
      var backendHeadersHtml = '<div class="detail-section"><div class="req-detail-grid">' +
        '<div class="req-detail-row"><span class="rd-label">Target URL</span><span class="rd-value url-full">' + escapeHtml(backendUrl) + '</span></div>' +
        '</div></div>';
      $('#dt-Backend').innerHTML =
        backendHeadersHtml +
        '<div class="backend-compare">' +
          '<div class="backend-col">' +
            '<h4>Original Request Body</h4>' + origReqHtml +
          '</div>' +
          '<div class="backend-col">' +
            '<h4>Rewritten Request (by Script)</h4>' + backendReqHtml +
          '</div>' +
        '</div>';
    }

    // Response tab
    var respBodyHtml = evt.resp_body ? '<pre class="body-pre">' + escapeHtml(tryFormatJSON(evt.resp_body)) + '</pre>'
                                     : '<div class="empty-body">(no body)</div>';
    $('#dt-Response').innerHTML =
      '<div class="detail-section"><h4>Response Headers</h4>' + renderHeaders(evt.resp_headers) + '</div>' +
      '<div class="detail-section"><h4>Response Body</h4>' + respBodyHtml + '</div>';

    // Reset tab state to Overview
    $$('.req-dtab').forEach(function(t) { t.classList.remove('active'); });
    var overviewTab = document.querySelector('.req-dtab[data-dtab="overview"]');
    if (overviewTab) overviewTab.classList.add('active');
    $$('.req-dtab-content').forEach(function(c) { c.classList.remove('active'); });
    var overviewContent = $('#dt-Overview');
    if (overviewContent) overviewContent.classList.add('active');

    $$('#reqBody tr').forEach(function (tr) {
      tr.classList.toggle('req-selected', parseInt(tr.dataset.idx) === idx);
    });
  }

  function updateReqScriptFilter() {
    var sel = $('#reqFilterScript');
    if (!sel) return;
    var cur = sel.value;
    while (sel.options.length > 1) sel.remove(1);
    state.scripts.forEach(function (s) {
      sel.appendChild(new Option(s.name || s.id, s.name || s.id));
    });
    if (cur) sel.value = cur;
  }

  function initRequestsTab() {
    updateReqScriptFilter();
  }

  // ========== Resend / Edit & Replay ==========
  function showResendEditor() {
    var evt = state.requests[state.reqDetailId];
    if (!evt) return;
    // 切换到编辑器视图
    $('#reqNormalView').style.display = 'none';
    $('#reqResendView').style.display = '';
    $('#reqDetailTabs').style.display = 'none';
    $('#btnReqResend').style.display = 'none';
    $('#reqDetailTitle').textContent = 'Edit & Resend';

    // 填充原始请求数据
    $('#resendMethod').value = evt.method || 'GET';
    $('#resendURL').value = evt.url || '';
    // 将 headers map 转为 key: value 文本
    var headersText = '';
    if (evt.req_headers) {
      Object.keys(evt.req_headers).forEach(function(k) {
        headersText += k + ': ' + evt.req_headers[k] + '\n';
      });
    }
    $('#resendHeaders').value = headersText;
    // Body（尝试 JSON 格式化）
    var bodyText = evt.req_body || '';
    if (bodyText) {
      try { bodyText = JSON.stringify(JSON.parse(bodyText), null, 2); } catch(e) {}
    }
    $('#resendBody').value = bodyText;
    // 隐藏上次响应
    $('#resendResponse').style.display = 'none';
    $('#resendStatus').textContent = '';
  }

  async function doResend() {
    var method = $('#resendMethod').value;
    var url = $('#resendURL').value.trim();
    if (!url) { $('#resendStatus').textContent = 'URL is required'; return; }

    // 解析 headers 文本
    var headers = {};
    var headerLines = $('#resendHeaders').value.split('\n');
    headerLines.forEach(function(line) {
      var idx = line.indexOf(':');
      if (idx > 0) {
        var k = line.substring(0, idx).trim();
        var v = line.substring(idx + 1).trim();
        if (k) headers[k] = v;
      }
    });

    var body = $('#resendBody').value;
    $('#resendStatus').textContent = 'Sending...';
    $('#resendResponse').style.display = 'none';

    try {
      var result = await api.replayRequest(method, url, headers, body);
      if (result.error) {
        $('#resendStatus').textContent = 'Error: ' + result.error;
        return;
      }
      $('#resendStatus').textContent = 'Done (' + (result.latency_ms || 0) + 'ms)';
      $('#resendRespStatus').textContent = '(' + result.status + ')';
      // 格式化响应体
      var respBodyStr = result.body || '';
      try { respBodyStr = JSON.stringify(JSON.parse(respBodyStr), null, 2); } catch(e) {}
      $('#resendRespBody').textContent = respBodyStr;
      $('#resendResponse').style.display = '';
    } catch (e) {
      $('#resendStatus').textContent = 'Error: ' + e.message;
    }
  }

  function initReqFilters() {
    ['reqFilterType', 'reqFilterScript', 'reqFilterRuleType', 'reqFilterStatus'].forEach(function (id) {
      var el = $('#' + id);
      if (!el) return;
      el.addEventListener('change', function () {
        state.clearedAt = null; // 改过滤器 = 重新查询，取消 Clear 限制
        loadRequests();
      });
    });

    var urlFilter = $('#reqFilterURL');
    if (urlFilter) {
      var debounceTimer;
      urlFilter.addEventListener('input', function () {
        clearTimeout(debounceTimer);
        debounceTimer = setTimeout(function () {
          state.clearedAt = null;
          loadRequests();
        }, 300);
      });
    }

    var btnTree = $('#btnReqTree');
    if (btnTree) {
      btnTree.classList.toggle('active', state.treeView);
      btnTree.addEventListener('click', function () {
        state.treeView = !state.treeView;
        this.classList.toggle('active', state.treeView);
        this.textContent = state.treeView ? '📂 Tree' : '📋 Flat';
        loadRequests();
      });
    }

    var btnPause = $('#btnReqPause');
    if (btnPause) {
      btnPause.addEventListener('click', function () {
        state.reqPaused = !state.reqPaused;
        this.textContent = state.reqPaused ? '▶ Resume' : '⏸ Pause';
        this.classList.toggle('btn-primary', state.reqPaused);
      });
    }

    var btnRefresh = $('#btnReqRefresh');
    if (btnRefresh) btnRefresh.addEventListener('click', loadRequests);

    var btnClear = $('#btnReqClear');
    if (btnClear) {
      btnClear.addEventListener('click', function () {
        state.requests = [];
        state.reqDetailId = null;
        state.clearedAt = new Date().toISOString(); // 标记清除时间，阻止旧数据回流
        var tbody = $('#reqBody');
        if (tbody) tbody.innerHTML = '';
        var panel = $('#reqDetailPanel');
        if (panel) panel.style.display = 'none';
        updateReqCount();
      });
    }

    // Resend 按钮
    var btnResend = $('#btnReqResend');
    if (btnResend) {
      btnResend.addEventListener('click', function () { showResendEditor(); });
    }

    // Resend Cancel
    var btnResendCancel = $('#btnResendCancel');
    if (btnResendCancel) {
      btnResendCancel.addEventListener('click', function () {
        $('#reqResendView').style.display = 'none';
        $('#reqNormalView').style.display = '';
        $('#reqDetailTabs').style.display = '';
        $('#btnReqResend').style.display = '';
      });
    }

    // Resend Send
    var btnResendSend = $('#btnResendSend');
    if (btnResendSend) {
      btnResendSend.addEventListener('click', function () { doResend(); });
    }

    var btnCloseDetail = $('#btnReqDetailClose');
    if (btnCloseDetail) {
      btnCloseDetail.addEventListener('click', function () {
        var panel = $('#reqDetailPanel');
        if (panel) panel.style.display = 'none';
        state.reqDetailId = null;
        // Reset to normal view
        $('#reqResendView').style.display = 'none';
        $('#reqNormalView').style.display = '';
        $('#reqDetailTabs').style.display = '';
        $('#btnReqResend').style.display = '';
        $$('#reqBody tr').forEach(function (tr) { tr.classList.remove('req-selected'); });
      });
    }

    $$('.req-dtab').forEach(function(tab) {
      tab.addEventListener('click', function(e) {
        e.stopPropagation();
        var dt = this.dataset.dtab;
        $$('.req-dtab').forEach(function(t){ t.classList.remove('active'); });
        this.classList.add('active');
        $$('.req-dtab-content').forEach(function(c){ c.classList.remove('active'); });
        var el = $('#dt-' + dt.charAt(0).toUpperCase() + dt.slice(1));
        if (el) el.classList.add('active');
      });
    });
  }

// ========== Status Tab ==========
  async function refreshCAStatus() {
    try {
      var resp = await fetch('/api/ca/status');
      var data = await resp.json();
      if (data.error) {
        $('#caStatusBody').innerHTML = '<span style="color:var(--text-muted)">CA not configured</span>';
        return;
      }
      var html = '<div class="ca-fp">Fingerprint: ' + escapeHtml(data.fingerprint) + '</div>';
      var allTrusted = true;
      var someTrusted = false;
      (data.stores || []).forEach(function(s) {
        var icon = s.trusted ? '🟢' : '🔴';
        var action = '';
        if (!s.trusted) {
          allTrusted = false;
          action = ' <a class="ca-link" href="/api/ca/download?format=mobileconfig">📥 mobileconfig</a>';
          action += ' <a class="ca-link" href="/api/ca/download?format=crt">📄 .crt</a>';
        } else {
          someTrusted = true;
        }
        html += '<div class="ca-store">' + icon + ' ' + escapeHtml(s.name) + ': ' +
          (s.trusted ? '✓ Trusted' : '✗ ' + escapeHtml(s.error || 'Not installed')) +
          action + '</div>';
      });
      if (allTrusted && someTrusted) {
        html += '<div class="ca-ok">✅ All clear — HTTPS interception works in all browsers</div>';
      } else {
        html += '<div class="ca-hint">💡 Download the mobileconfig file → double-click → approve to install CA.</div>';
      }
      $('#caStatusBody').innerHTML = html;
    } catch(e) {
      $('#caStatusBody').innerHTML = '<span style="color:var(--text-muted)">CA status unavailable</span>';
    }
  }

  async function refreshStatus() {
    try {
      const status = await api.getStatus();
      const debug = await api.getDebugStatus();
      $('#statUptime').textContent = fmtUptime(debug.uptime_sec);
      $('#statTotalReqs').textContent = fmtNumber(debug.total_requests);
      $('#statTotalErrs').textContent = fmtNumber(debug.total_errors);
      if (debug.conn_pool) { $('#statMitmActive').textContent = debug.conn_pool.active_mitm; $('#statMitmEvicted').textContent = debug.conn_pool.total_evicted; }
      if (status.rules) { $('#statScripts').textContent = status.rules.enabled_scripts + '/' + status.rules.total_scripts; $('#statRules').textContent = status.rules.total_rules; $('#statHits').textContent = fmtNumber(status.rules.total_hits); }
      if (status.runtime) { $('#statGoroutines').textContent = fmtNumber(status.runtime.goroutines); $('#statHeapAlloc').textContent = fmtBytes(status.runtime.heap_alloc_bytes); $('#statNumGC').textContent = fmtNumber(status.runtime.num_gc); }
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
    // 按 enabled 排序（启用的在前），保留 disabled 选项用于调试禁用规则
    const sorted = [...state.scripts].sort((a, b) => {
      if (a.enabled === b.enabled) return 0;
      return a.enabled ? -1 : 1;
    });
    sorted.forEach(s => {
      const opt = new Option(s.name || s.id, s.id);
      if (!s.enabled) {
        opt.textContent = (s.name || s.id) + ' (disabled)';
        opt.style.color = 'var(--text-muted)';
      }
      sel.appendChild(opt);
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
    const headers = $('#debugHeaders').value;
    const cookie = $('#debugCookie').value;
    if (!path) { toast('Enter a request path', 'error'); return; }

    const stepsEl = $('#debugSteps');
    stepsEl.innerHTML = '<div class="debug-empty">⏳ Running debug...</div>';

    try {
      const result = await api.debugScript(scriptId, method, path, body, dryRun, headers, cookie);
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
        const o = step.output;
        // onResponse 且同时有 backendResp + finalResp：双栏对比展示
        if (step.stage === 'onResponse' && o.backendResp && o.finalResp) {
          let html = '<div class="debug-resp-compare">';
          html += '<div class="debug-resp-col">' +
            '<div class="debug-resp-title">Backend Response' + (o.backendStatus != null ? '<span class="debug-resp-status">' + o.backendStatus + '</span>' : '') + '</div>' +
            '<pre>' + escapeHtml(tryFormatJSON(o.backendResp)) + '</pre>' +
          '</div>';
          html += '<div class="debug-resp-col">' +
            '<div class="debug-resp-title">Final Response' + (o.finalStatus != null ? '<span class="debug-resp-status">' + o.finalStatus + '</span>' : '') + '</div>' +
            '<pre>' + escapeHtml(tryFormatJSON(o.finalResp)) + '</pre>' +
          '</div>';
          html += '</div>';
          if (o.note) html += '<div style="font-size:11px;color:var(--text-muted);margin-top:6px">' + escapeHtml(o.note) + '</div>';
          body.innerHTML = html;
        } else {
          let html = '';
          if (o.script_id) html += '<div style="font-size:11px;color:var(--accent)">Script: ' + escapeHtml(o.script_id) + '</div>';
          if (o.rule) html += '<div style="font-size:11px;color:var(--text-secondary)">Rule: ' + escapeHtml(o.rule) + '</div>';
          if (o.backendURL) html += '<div style="font-size:11px;color:var(--green)">Backend: ' + escapeHtml(o.backendURL) + '</div>';
          if (o.would_send_to) html += '<div style="font-size:11px;color:var(--yellow)">Would POST → ' + escapeHtml(o.would_send_to) + '</div>';
          if (o.status_code) html += '<div style="font-size:11px;color:var(--text-secondary)">Status: ' + o.status_code + '</div>';
          if (o.body) html += '<div style="font-size:11px;color:var(--text-secondary);margin-top:4px">Response Body:</div><pre>' + escapeHtml(tryFormatJSON(o.body)) + '</pre>';
          if (o.backendStatus != null) html += '<div style="font-size:11px;color:var(--text-secondary);margin-top:4px">Backend Status: ' + o.backendStatus + '</div>';
          if (o.backendResp) html += '<div style="font-size:11px;color:var(--text-secondary);margin-top:4px">Backend Response:</div><pre>' + escapeHtml(tryFormatJSON(o.backendResp)) + '</pre>';
          if (o.finalStatus != null) html += '<div style="font-size:11px;color:var(--text-secondary);margin-top:4px">Final Status: ' + o.finalStatus + '</div>';
          if (o.finalResp) html += '<div style="font-size:11px;color:var(--text-secondary);margin-top:4px">Final Response:</div><pre>' + escapeHtml(tryFormatJSON(o.finalResp)) + '</pre>';
          if (o.backendRequest) html += '<div style="font-size:11px;color:var(--text-secondary);margin-top:4px">Backend Request:</div><pre>' + escapeHtml(JSON.stringify(o.backendRequest, null, 2)) + '</pre>';
          if (o.note) html += '<div style="font-size:11px;color:var(--text-muted)">' + escapeHtml(o.note) + '</div>';
          body.innerHTML = html;
        }
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
    $('#btnDebugParseCurl').addEventListener('click', parseCurl);
  }

  // parseCurl 解析 curl 命令，填充到 Debug 表单
  function parseCurl() {
    const el = $('#debugCurl');
    const cmd = el ? el.value.trim() : '';
    if (!cmd) { toast('请先粘贴 curl 命令', 'error'); return; }

    // 1. 提取 URL（单引号或双引号包裹）
    const urlMatch = cmd.match(/['"]((?:https?):\/\/[^'"]+)['"]/i);
    if (!urlMatch) { toast('未识别到 URL', 'error'); return; }

    let method = 'POST';
    let headers = {};
    let cookie = '';
    let body = '';

    // 2. 提取 method（-X 或 --request）
    const methodMatch = cmd.match(/(?:-X|--request)\s+['"]?(\w+)['"]?/i);
    if (methodMatch) method = methodMatch[1].toUpperCase();

    // 3. 提取所有 -H / --header
    const headerRe = /(?:-H|--header)\s+['"]([^'"]+)['"]/gi;
    let h;
    while ((h = headerRe.exec(cmd)) !== null) {
      const line = h[1];
      const idx = line.indexOf(':');
      if (idx > 0) {
        const key = line.substring(0, idx).trim();
        const val = line.substring(idx + 1).trim();
        if (key.toLowerCase() === 'cookie') {
          cookie = val;
        } else if (key.toLowerCase() === 'connection' || key.toLowerCase() === 'accept-encoding' ||
                   key.toLowerCase() === 'content-length') {
          // 忽略传输层/连接层头，避免干扰
        } else {
          headers[key] = val;
        }
      }
    }

    // 4. 提取 body（--data-raw 优先，其次 --data，最后 -d）
    const bodyMatch = cmd.match(/--data-raw\s+'([^']*)'/s) ||
                      cmd.match(/--data-raw\s+"([^"]*)"/s) ||
                      cmd.match(/--data\s+'([^']*)'/s) ||
                      cmd.match(/-d\s+'([^']*)'/s);
    if (bodyMatch) body = bodyMatch[1];

    // 5. path 直接填完整 URL（含 scheme+host），后端 RequireExternal 要求完整 URL，
    //    engine.Match 内部会从完整 URL 解析 Path+RawQuery 去匹配 regex
    const path = urlMatch[1];

    // 6. 填充表单
    $('#debugMethod').value = method;
    $('#debugPath').value = path;
    $('#debugBody').value = body;
    $('#debugCookie').value = cookie;
    const headerLines = Object.keys(headers).map(k => k + ': ' + headers[k]).join('\n');
    $('#debugHeaders').value = headerLines;

    toast('curl 解析完成：' + method + ' ' + path, 'success');
  }

  // ========== Initialization ==========
  function init() {
    initTabs();
    initEditor();
    initNewScriptDialog();
    initReloadAll();
    initDebugActions();
    initReqFilters();

    loadScripts();
    connectSSE();

    setInterval(() => loadScripts(), 10000);
    setInterval(() => { if ($('#tab-status').classList.contains('active')) { refreshStatus(); refreshDiagnostics(); } }, 5000);
    const btnDiag = $('#btnRefreshDiag');
    if (btnDiag) btnDiag.addEventListener('click', refreshDiagnostics);
    initDiagTabs();
    refreshDiagnostics();
    refreshStatus();
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', init);
  else init();
})();
