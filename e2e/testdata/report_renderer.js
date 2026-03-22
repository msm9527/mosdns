(function () {
  function renderDNSTable(items) {
    return renderTable(["用例", "检查名", "传输", "监听器", "域名", "类型", "返回码", "操作"], items.map((item, index) => [
      localCase(item.case), item.name, item.network, item.listener, item.domain, item.query, item.rcode,
      `<button class="action-button dns-detail" data-index="${index}">查看</button>`
    ]));
  }

  function renderAPITable(items) {
    return renderTable(["用例", "方法", "路径", "结果", "操作"], items.map((item, index) => [
      localCase(item.case), item.method, item.path, escapeHTML(item.detail),
      `<button class="action-button api-detail" data-index="${index}">查看</button>`
    ]));
  }

  function renderCaseList(items) {
    if (!items.length) return `<div class="info-card">没有匹配的用例。</div>`;
    return items.map((item, index) => `
      <article class="case-item">
        <div class="case-top">
          <span class="status-badge ${statusClass(item.status)}">${statusText(item.status)}</span>
          <strong>${localCase(item.name)}</strong>
          <span class="muted">耗时 ${item.duration}</span>
        </div>
        <div class="muted">${escapeHTML(item.detail)}</div>
        <div class="case-actions">
          <span class="chip">检查 ${item.checks.length}</span>
          <span class="chip">指标 ${item.metrics.length}</span>
          <button class="action-button case-detail" data-index="${index}">查看详情</button>
        </div>
      </article>`).join("");
  }

  function renderTimeline(items) {
    return `<div class="timeline-grid">${items.map((item) => `
      <article class="timeline-item">
        <div class="timeline-top">
          <span class="status-badge ${statusClass(item.status)}">${statusText(item.status)}</span>
          <strong>${localCase(item.name)}</strong>
          <span class="muted">${localCategory(item.category)} / ${item.duration}</span>
        </div>
        <div class="timeline-bar"><span style="width:${item.duration_width}"></span></div>
        <div class="case-actions">
          <span class="chip">检查 ${item.checks_count}</span>
          <span class="chip">指标 ${item.metrics_count}</span>
          <a href="${item.artifact}" class="chip">附件</a>
        </div>
        <div class="muted" style="margin-top:10px;">${escapeHTML(item.detail)}</div>
      </article>`).join("")}</div>`;
  }

  function renderMetricTable(items, includeCase) {
    if (!items.length) return `<div class="info-card">暂无数据。</div>`;
    const headers = includeCase ? ["用例", "指标", "值", "说明"] : ["指标", "值", "说明"];
    const rows = items.map((item) => includeCase ? [localCase(item.case), metricText(item.name), item.value, item.detail] : [metricText(item.name), item.value, item.detail]);
    return renderTable(headers, rows);
  }

  function renderCheckList(items) {
    if (!items.length) return `<div class="info-card">暂无检查项。</div>`;
    return renderTable(["名称", "详情"], items.map((item) => [item.name, escapeHTML(item.detail)]));
  }

  function renderArtifactsTable(items) {
    return renderTable(["名称", "类型", "路径"], items.map((item) => [localArtifact(item.name), item.kind, `<a href="${item.path}">${item.path}</a>`]));
  }

  function renderKeyValueTable(rows) {
    return renderTable(["字段", "值"], rows.map((row) => [row[0], escapeHTML(row[1])]));
  }

  function renderTable(headers, rows) {
    if (!rows.length) return `<div class="info-card">暂无数据。</div>`;
    return `<div class="table-wrap"><table><thead><tr>${headers.map((h) => `<th>${h}</th>`).join("")}</tr></thead><tbody>${rows.map((row) => `<tr>${row.map((cell) => `<td>${cell}</td>`).join("")}</tr>`).join("")}</tbody></table></div>`;
  }

  function renderPager(page, kind) {
    if (page.totalPages <= 1) return "";
    return `<div class="pager">
      <button data-page-kind="${kind}" data-page-value="${page.page - 1}" ${page.page <= 1 ? "disabled" : ""}>上一页</button>
      <span class="muted">第 ${page.page} / ${page.totalPages} 页，共 ${page.totalItems} 条</span>
      <button data-page-kind="${kind}" data-page-value="${page.page + 1}" ${page.page >= page.totalPages ? "disabled" : ""}>下一页</button>
    </div>`;
  }

  function renderSearchBar(id, placeholder, value) {
    return `<div class="filter-bar"><input id="${id}" class="filter-input" type="search" value="${escapeAttr(value)}" placeholder="${placeholder}"></div>`;
  }

  function block(title, body) {
    return `<section class="drawer-block"><div class="section-head"><h3>${title}</h3></div>${body}</section>`;
  }

  function panel(title, note, body) {
    return `<section class="panel"><div class="section-head"><div><h2>${title}</h2><div class="section-note">${note}</div></div></div>${body}</section>`;
  }

  function statCard(label, value, sub) {
    return `<div class="stat-card"><div class="stat-label">${label}</div><div class="stat-value">${value}</div><div class="card-sub">${sub}</div></div>`;
  }

  function paginate(items, page, size) {
    const totalItems = items.length;
    const totalPages = Math.max(1, Math.ceil(totalItems / size));
    const current = Math.min(Math.max(page, 1), totalPages);
    const start = (current - 1) * size;
    return { items: items.slice(start, start + size), page: current, totalPages, totalItems };
  }

  function matchQuery(values, query) {
    if (!query) return true;
    const term = query.toLowerCase();
    return values.some((value) => String(value || "").toLowerCase().includes(term));
  }

  function statusText(value) { return value === "passed" ? "通过" : "失败"; }
  function statusClass(value) { return value === "passed" ? "status-passed" : "status-failed"; }
  function metricText(value) { return value.replace("load ", "负载 ").replace("cache_", "缓存 "); }
  function option(value, label, current) { return `<option value="${value}" ${value === current ? "selected" : ""}>${label}</option>`; }
  function escapeAttr(value) { return escapeHTML(value).replace(/"/g, "&quot;"); }

  function localCategory(value) {
    const map = { "Control Plane": "控制面", "DNS Data Plane": "DNS 数据面", "Policy Switches": "策略开关", "Rule Management": "规则管理", "Cache & Load": "缓存与压测", "General": "通用" };
    return map[value] || value;
  }

  function localCase(value) {
    const map = {
      "control api": "控制接口", "udp and tcp dns": "UDP/TCP 主监听", "specialized listeners": "专用监听器",
      "block and ad switches": "拦截与广告开关", "query type and ipv6 switches": "查询类型与 IPv6 开关",
      "routing mode switches": "路由模式开关", "rule apis": "规则接口", "cache stats and stability": "缓存统计与稳定性", "setup fixture": "夹具初始化"
    };
    return map[value] || value;
  }

  function localArtifact(value) {
    return value.replace("HTML Report", "HTML 报告").replace("HTML Stylesheet", "报告样式").replace("HTML Renderer Script", "渲染脚本").replace("HTML Script", "交互脚本").replace("JSON Report", "JSON 报告").replace("Artifact", "附件");
  }

  function escapeHTML(value) {
    return String(value || "").replace(/[&<>"']/g, (char) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[char]));
  }

  window.mosdnsReportHelper = {
    block, escapeAttr, escapeHTML, localArtifact, localCase, localCategory, matchQuery, metricText,
    option, paginate, panel, renderAPITable, renderArtifactsTable, renderCaseList, renderCheckList,
    renderDNSTable, renderKeyValueTable, renderMetricTable, renderPager, renderSearchBar, renderTimeline,
    statCard, statusClass, statusText
  };
})();
