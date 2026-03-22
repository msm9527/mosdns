(function () {
  const report = JSON.parse(document.getElementById("report-data").textContent);
  const H = window.mosdnsReportHelper;
  const tabs = ["overview", "dns", "api", "performance", "cases"];
  const pageSize = { dns: 10, api: 8, cases: 4 };
  const state = {
    tab: tabs.includes(location.hash.slice(1)) ? location.hash.slice(1) : "overview",
    dnsPage: 1,
    apiPage: 1,
    casePage: 1,
    dnsQuery: "",
    apiQuery: "",
    caseQuery: "",
    caseStatus: "all"
  };

  init();

  function init() {
    bindTabs();
    bindDrawer();
    renderHero();
    renderOverview();
    renderDNS();
    renderAPI();
    renderPerformance();
    renderCases();
    syncTabs();
    window.addEventListener("hashchange", () => {
      state.tab = tabs.includes(location.hash.slice(1)) ? location.hash.slice(1) : "overview";
      syncTabs();
    });
  }

  function bindTabs() {
    document.querySelectorAll(".tab-button").forEach((button) => {
      button.addEventListener("click", () => {
        state.tab = button.dataset.tab;
        location.hash = state.tab;
        syncTabs();
      });
    });
  }

  function bindDrawer() {
    document.getElementById("drawer-close").addEventListener("click", closeDrawer);
    document.querySelector("[data-close-drawer='true']").addEventListener("click", closeDrawer);
  }

  function syncTabs() {
    document.querySelectorAll(".tab-button").forEach((button) => {
      button.classList.toggle("active", button.dataset.tab === state.tab);
    });
    document.querySelectorAll(".tab-panel").forEach((panel) => {
      panel.classList.toggle("active", panel.id === `tab-${state.tab}`);
    });
  }

  function renderHero() {
    document.getElementById("hero-summary").innerHTML = [
      H.statCard("执行状态", `<span class="status-badge ${H.statusClass(report.status)}">${H.statusText(report.status)}</span>`, `总用例 ${report.total}，通过率 ${report.success_rate}`),
      H.statCard("执行窗口", `<span class="mono">${report.duration}</span>`, `${report.started_at} 到 ${report.finished_at}`),
      H.statCard("断言覆盖", `<span class="mono">${report.coverage.dns_checks}</span>`, `DNS 断言，接口检查 ${report.coverage.api_checks}`),
      H.statCard("性能指标", `<span class="mono">${report.coverage.load_metrics + report.coverage.cache_metrics}</span>`, `负载 ${report.coverage.load_metrics}，缓存 ${report.coverage.cache_metrics}`)
    ].join("");
  }

  function renderOverview() {
    document.getElementById("tab-overview").innerHTML = `
      ${H.panel("总览", "关键统计与环境信息。", `<div class="stat-grid">${[
        H.statCard("总用例", report.total, "完整执行的 suite 数量"),
        H.statCard("总检查", report.checks_total, "断言与运行校验总数"),
        H.statCard("总指标", report.metrics_total, "采集到的运行指标"),
        H.statCard("产物数量", report.artifacts.length, "报告与用例附件")
      ].join("")}</div>`)}
      <div class="summary-grid">
        ${H.panel("执行环境", "本次运行的环境上下文。", H.renderKeyValueTable([
          ["夹具模式", report.environment.fixture_mode], ["Go 版本", report.environment.go_version],
          ["平台", `${report.environment.goos}/${report.environment.goarch}`], ["CI 环境", report.environment.ci],
          ["提交", report.environment.commit || "-"], ["分支/引用", report.environment.ref || "-"], ["报告目录", report.environment.report_dir]
        ]))}
        ${H.panel("报告产物", "点击可直接查看生成文件。", H.renderArtifactsTable(report.artifacts))}
      </div>
      ${H.panel("执行时间线", "各 suite 耗时和覆盖数量。", H.renderTimeline(report.suites))}
    `;
  }

  function renderDNS() {
    renderPagedPanel("tab-dns", "DNS 断言", "支持搜索、翻页和查看单条详情。", "dns", filterDNSRows(), H.renderDNSTable, openDNSDrawer, "搜索域名、监听器、用例名或返回码");
  }

  function renderAPI() {
    renderPagedPanel("tab-api", "接口检查", "展示规则与控制面接口校验，并支持查看详情。", "api", filterAPIRows(), H.renderAPITable, openAPIDrawer, "搜索方法、路径、用例名或结果");
  }

  function renderPerformance() {
    document.getElementById("tab-performance").innerHTML = `
      ${H.panel("性能亮点", "优先展示吞吐与时延核心指标。", `<div class="card-grid">${report.performance.highlights.map((item) => H.statCard(H.metricText(item.name), item.value, item.detail)).join("")}</div>`)}
      <div class="summary-grid">
        ${H.panel("负载指标", "并发 DNS 请求压测结果。", H.renderMetricTable(report.performance.load_metrics))}
        ${H.panel("缓存指标", "缓存命中路径统计。", H.renderMetricTable(report.performance.cache_metrics))}
      </div>
      ${H.panel("其他指标", "非缓存/压测类的其他运行指标。", H.renderMetricTable(report.performance.other_metrics, true))}
    `;
  }

  function renderCases() {
    const rows = filterCaseRows();
    const page = H.paginate(rows, state.casePage, pageSize.cases);
    document.getElementById("tab-cases").innerHTML = H.panel("用例详情", "支持按状态和关键字筛选，并可查看完整检查与指标。", `
      <div class="filter-bar">
        <input id="case-search" class="filter-input" type="search" value="${H.escapeAttr(state.caseQuery)}" placeholder="搜索用例名、摘要或详情">
        <select id="case-status" class="filter-select">
          ${H.option("all", "全部状态", state.caseStatus)}
          ${H.option("passed", "仅看通过", state.caseStatus)}
          ${H.option("failed", "仅看失败", state.caseStatus)}
        </select>
      </div>
      <div class="case-list">${H.renderCaseList(page.items)}</div>
      ${H.renderPager(page, "cases")}
    `);
    bindSearch("case-search", "caseQuery", "casePage");
    document.getElementById("case-status").addEventListener("change", (event) => {
      state.caseStatus = event.target.value;
      state.casePage = 1;
      renderCases();
    });
    bindPager("cases");
    bindDetailButtons(".case-detail", page.items, openCaseDrawer);
  }

  function renderPagedPanel(id, title, note, kind, rows, renderer, opener, placeholder) {
    const page = H.paginate(rows, state[`${kind}Page`], pageSize[kind]);
    document.getElementById(id).innerHTML = H.panel(title, note, `
      ${H.renderSearchBar(`${kind}-search`, placeholder, state[`${kind}Query`])}
      ${renderer(page.items)}
      ${H.renderPager(page, kind)}
    `);
    bindSearch(`${kind}-search`, `${kind}Query`, `${kind}Page`);
    bindPager(kind);
    bindDetailButtons(`.${kind}-detail`, page.items, opener);
  }

  function filterDNSRows() {
    return report.dns_checks.filter((item) => H.matchQuery([item.case, item.name, item.listener, item.domain, item.rcode, item.answers], state.dnsQuery));
  }

  function filterAPIRows() {
    return report.api_checks.filter((item) => H.matchQuery([item.case, item.method, item.path, item.detail], state.apiQuery));
  }

  function filterCaseRows() {
    return report.cases.filter((item) => H.matchQuery([item.name, item.detail], state.caseQuery) && (state.caseStatus === "all" || item.status === state.caseStatus));
  }

  function bindSearch(id, key, pageKey) {
    document.getElementById(id).addEventListener("input", (event) => {
      state[key] = event.target.value.trim();
      state[pageKey] = 1;
      rerender(pageKey);
    });
  }

  function bindPager(kind) {
    document.querySelectorAll(`[data-page-kind='${kind}']`).forEach((button) => {
      button.addEventListener("click", () => {
        state[`${kind}Page`] = Number(button.dataset.pageValue);
        rerender(`${kind}Page`);
      });
    });
  }

  function bindDetailButtons(selector, items, handler) {
    document.querySelectorAll(selector).forEach((button) => {
      button.addEventListener("click", () => handler(items[Number(button.dataset.index)]));
    });
  }

  function rerender(pageKey) {
    if (pageKey === "dnsPage") renderDNS();
    if (pageKey === "apiPage") renderAPI();
    if (pageKey === "casePage") renderCases();
  }

  function openDNSDrawer(item) {
    openDrawer("DNS 断言详情", item.name, [
      H.block("来源", H.renderKeyValueTable([["用例", H.localCase(item.case)], ["传输", item.network], ["监听器", item.listener], ["域名", item.domain], ["类型", item.query]])),
      H.block("结果", H.renderKeyValueTable([["返回码", item.rcode], ["应答", item.answers]]))
    ]);
  }

  function openAPIDrawer(item) {
    openDrawer("接口检查详情", `${item.method} ${item.path}`, [
      H.block("来源", H.renderKeyValueTable([["用例", H.localCase(item.case)], ["检查名", item.name]])),
      H.block("结果", `<pre>${H.escapeHTML(item.detail)}</pre>`)
    ]);
  }

  function openCaseDrawer(item) {
    openDrawer("用例详情", H.localCase(item.name), [
      H.block("概要", H.renderKeyValueTable([["状态", H.statusText(item.status)], ["耗时", item.duration], ["摘要", item.detail], ["附件", item.artifact || "-"]])),
      H.block("检查项", H.renderCheckList(item.checks)),
      H.block("指标项", H.renderMetricTable(item.metrics, true))
    ]);
  }

  function openDrawer(kicker, title, blocks) {
    document.getElementById("drawer-kicker").textContent = kicker;
    document.getElementById("drawer-title").textContent = title;
    document.getElementById("drawer-body").innerHTML = blocks.join("");
    document.getElementById("detail-drawer").classList.remove("hidden");
  }

  function closeDrawer() {
    document.getElementById("detail-drawer").classList.add("hidden");
  }
})();
