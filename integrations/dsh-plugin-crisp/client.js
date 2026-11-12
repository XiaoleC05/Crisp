// Crisp quality gate - Web client half.
//
// Registers a left-sidebar entry ("质量门禁") and the standalone main-area page
// it opens. The page fetches GET /crisp/status from the Host plugin half and
// renders the live gate configuration and the latest quality decisions.
//
// Plain browser bundle: the module loader injects React, and this file must not
// import any Harness client package. Styling uses only --dsw-alias-* theme
// tokens, so it follows the host theme in light and dark mode.
window.__ModuleLoader__.load({
  id: "@local/crisp-quality-gate",
  factory(require) {
    const React = require("react");
    const h = React.createElement;

    const NS = "crispQualityGate";
    const PANEL_ID = "crisp";
    const STATUS_PATH = "/crisp/status";
    const REFRESH_MS = 5000;

    const zh = {
      panel: "质量门禁",
      title: "Crisp 质量门禁",
      subtitle: "对每条最终回答做确定性规则检查 + LLM 语义审查；guard 模式会在 material 问题时要求一次完整重写。",
      back: "返回对话",
      refresh: "刷新",
      refreshAria: "刷新质量门禁状态",
      loading: "正在读取质量门禁状态…",
      loadError: "无法读取状态",
      retry: "重试",
      statusOn: "运行中",
      statusOff: "已停用",
      mode: "当前模式",
      modeGuard: "拦截并请求一次重写",
      modeObserve: "只记录，不影响回答",
      critic: "语义审查",
      notConfigured: "未配置",
      thinkingOff: "思考已关闭",
      today: "今日审查",
      times: "次",
      todayPass: "通过",
      todayBlock: "拦截",
      todayFailOpen: "fail-open",
      avgLatency: "Critic 平均延迟",
      noCriticCalls: "今日暂无 Critic 调用",
      criticCalls: "次调用",
      recent: "最近审查",
      recentHint: "最新 20 条记录",
      colTime: "时间",
      colDecision: "判定",
      colReason: "原因",
      colRules: "规则信号",
      colCritic: "Critic",
      colTurn: "Turn",
      empty: "还没有审查记录。下一条回答结束后会出现在这里。",
      emptyHint: "如果一直没有记录，请确认插件已启用且日志路径可写。",
      pass: "通过",
      block: "拦截",
      failOpen: "FAIL-OPEN",
      none: "—",
      logFile: "日志",
      logMissing: "日志文件尚未生成",
      updated: "更新于",
      autoRefresh: "每 5 秒自动刷新",
      copyPath: "复制路径",
      copied: "已复制",
      warnings: "配置提示",
      reasonPrefix: "原因",
      criticUnavailable: "Critic 不可用（fail-open）",
    };

    const en = {
      panel: "Quality gate",
      title: "Crisp quality gate",
      subtitle: "Deterministic rules plus an LLM critic on every final answer; guard mode requests exactly one full rewrite when a material issue is found.",
      back: "Back to chat",
      refresh: "Refresh",
      refreshAria: "Refresh quality gate status",
      loading: "Loading quality gate status…",
      loadError: "Unable to load status",
      retry: "Retry",
      statusOn: "Active",
      statusOff: "Disabled",
      mode: "Mode",
      modeGuard: "Blocks and requests one rewrite",
      modeObserve: "Records only, never changes answers",
      critic: "Semantic critic",
      notConfigured: "Not configured",
      thinkingOff: "Thinking off",
      today: "Checks today",
      times: "checks",
      todayPass: "passed",
      todayBlock: "blocked",
      todayFailOpen: "fail-open",
      avgLatency: "Avg critic latency",
      noCriticCalls: "No critic calls today",
      criticCalls: "calls",
      recent: "Recent checks",
      recentHint: "Latest 20 records",
      colTime: "Time",
      colDecision: "Decision",
      colReason: "Reason",
      colRules: "Rule signals",
      colCritic: "Critic",
      colTurn: "Turn",
      empty: "No checks recorded yet. The next final answer will appear here.",
      emptyHint: "If this stays empty, confirm the plugin is enabled and the log path is writable.",
      pass: "PASS",
      block: "BLOCK",
      failOpen: "FAIL-OPEN",
      none: "—",
      logFile: "Log",
      logMissing: "Log file not created yet",
      updated: "Updated",
      autoRefresh: "Auto-refreshes every 5s",
      copyPath: "Copy path",
      copied: "Copied",
      warnings: "Config warnings",
      reasonPrefix: "Reason",
      criticUnavailable: "Critic unavailable (fail-open)",
    };

    const REASON_LABELS = {
      critic_pass: { zh: "语义通过", en: "critic passed" },
      critic_error: { zh: "Critic 错误", en: "critic error" },
      semantic_duplicate: { zh: "语义重复", en: "semantic duplicate" },
      filler: { zh: "无效铺垫", en: "filler" },
      over_fragmentation: { zh: "过度拆分", en: "over-fragmented" },
      redundant_explanation: { zh: "重复解释", en: "redundant explanation" },
      conclusion_repetition: { zh: "结论重复", en: "repeated conclusion" },
      low_information_density: { zh: "信息密度低", en: "low information density" },
      semantic: { zh: "语义问题", en: "semantic issue" },
      not_reviewed: { zh: "未送 Critic", en: "not reviewed" },
      short_answer: { zh: "短回答跳过", en: "short answer skipped" },
      rules_only: { zh: "仅规则", en: "rules only" },
      length_signal: { zh: "长度信号", en: "length signal" },
    };

    const STYLES = [
      ".crisp-page{box-sizing:border-box;height:100%;overflow:auto;padding:calc(var(--dsh-frame-top-clearance,0px) + 28px) 32px 56px;color:var(--dsw-alias-label-primary);background:var(--dsw-alias-bg-base);font:inherit;}",
      ".crisp-inner{max-width:1060px;margin:0 auto;display:flex;flex-direction:column;gap:20px;}",
      ".crisp-head{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;}",
      ".crisp-head h1{margin:0;font-size:22px;line-height:1.3;font-weight:600;letter-spacing:-.01em;}",
      ".crisp-sub{margin:6px 0 0;color:var(--dsw-alias-label-secondary);font-size:13px;line-height:1.55;max-width:620px;}",
      ".crisp-actions{display:flex;align-items:center;gap:8px;flex:none;}",
      ".crisp-btn{appearance:none;border:1px solid var(--dsw-alias-border-l2);background:var(--dsw-alias-bg-layer-1);color:var(--dsw-alias-label-primary);border-radius:8px;height:32px;padding:0 12px;font:inherit;font-size:13px;line-height:30px;cursor:pointer;display:inline-flex;align-items:center;gap:6px;white-space:nowrap;}",
      ".crisp-btn:hover{background:var(--dsw-alias-interactive-bg-hover);}",
      ".crisp-btn:active{background:var(--dsw-alias-interactive-bg-active);}",
      ".crisp-btn-icon{width:32px;padding:0;justify-content:center;}",
      ".crisp-pill{display:inline-flex;align-items:center;gap:6px;height:24px;padding:0 9px;border-radius:999px;font-size:12px;line-height:22px;border:1px solid var(--dsw-alias-border-l1);color:var(--dsw-alias-label-secondary);}",
      ".crisp-dot{width:6px;height:6px;border-radius:50%;background:var(--dsw-alias-state-success-primary);}",
      ".crisp-dot.off{background:var(--dsw-alias-label-tertiary);}",
      ".crisp-cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(210px,1fr));gap:12px;}",
      ".crisp-card{background:var(--dsw-alias-bg-layer-1);border:1px solid var(--dsw-alias-border-l1);border-radius:12px;padding:16px;min-width:0;}",
      ".crisp-card-label{font-size:12px;color:var(--dsw-alias-label-caption);letter-spacing:.02em;}",
      ".crisp-card-value{margin-top:8px;font-size:22px;line-height:1.2;font-weight:600;word-break:break-word;}",
      ".crisp-card-hint{margin-top:8px;font-size:12px;line-height:1.5;color:var(--dsw-alias-label-secondary);}",
      ".crisp-section-head{display:flex;align-items:baseline;justify-content:space-between;gap:12px;}",
      ".crisp-section-head h2{margin:0;font-size:15px;font-weight:600;}",
      ".crisp-section-head span{font-size:12px;color:var(--dsw-alias-label-caption);}",
      ".crisp-table{border:1px solid var(--dsw-alias-border-l1);border-radius:12px;background:var(--dsw-alias-bg-layer-1);overflow:hidden;}",
      ".crisp-table-scroll{overflow-x:auto;}",
      ".crisp-row{display:grid;grid-template-columns:132px 96px minmax(130px,1.1fr) minmax(120px,1fr) 132px 52px;gap:12px;align-items:center;padding:10px 16px;border-top:1px solid var(--dsw-alias-border-l1);font-size:13px;min-width:760px;}",
      ".crisp-row.head{border-top:0;padding:9px 16px;color:var(--dsw-alias-label-caption);font-size:12px;background:var(--dsw-alias-bg-layer-1);}",
      ".crisp-row .muted{color:var(--dsw-alias-label-secondary);}",
      ".crisp-row .mono{font-family:var(--dsh-code-font-family,ui-monospace,SFMono-Regular,Menlo,Consolas,monospace);font-size:12px;}",
      ".crisp-badge{display:inline-flex;align-items:center;justify-content:center;height:22px;padding:0 9px;border-radius:6px;font-size:12px;font-weight:600;letter-spacing:.02em;white-space:nowrap;border:1px solid var(--dsw-alias-border-l2);background:transparent;}",
      ".crisp-badge.pass{color:var(--dsw-alias-state-success-primary);border-color:color-mix(in srgb,var(--dsw-alias-state-success-primary) 42%,transparent);}",
      ".crisp-badge.block{color:var(--dsw-alias-state-error-primary);border-color:color-mix(in srgb,var(--dsw-alias-state-error-primary) 42%,transparent);}",
      ".crisp-badge.fail{color:var(--dsw-alias-state-warn-label);border-color:color-mix(in srgb,var(--dsw-alias-state-warn-label) 42%,transparent);}",
      ".crisp-rules{display:flex;flex-wrap:wrap;gap:4px;}",
      ".crisp-chip{display:inline-flex;height:20px;align-items:center;padding:0 7px;border-radius:5px;border:1px solid var(--dsw-alias-border-l2);color:var(--dsw-alias-label-secondary);font-size:11px;}",
      ".crisp-empty{border:1px dashed var(--dsw-alias-border-l2);border-radius:12px;padding:28px;text-align:center;}",
      ".crisp-empty strong{display:block;font-size:14px;font-weight:600;}",
      ".crisp-empty p{margin:8px 0 0;font-size:12px;color:var(--dsw-alias-label-secondary);}",
      ".crisp-footer{display:flex;flex-wrap:wrap;align-items:center;justify-content:space-between;gap:10px;padding-top:4px;font-size:12px;color:var(--dsw-alias-label-caption);}",
      ".crisp-logpath{display:flex;align-items:center;gap:8px;min-width:0;}",
      ".crisp-logpath code{max-width:520px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-family:var(--dsh-code-font-family,ui-monospace,SFMono-Regular,Menlo,Consolas,monospace);font-size:11px;color:var(--dsw-alias-label-secondary);}",
      ".crisp-linkbtn{appearance:none;border:0;background:none;color:var(--dsw-alias-label-secondary);font:inherit;font-size:12px;cursor:pointer;padding:2px 0;text-decoration:underline;text-underline-offset:2px;}",
      ".crisp-linkbtn:hover{color:var(--dsw-alias-label-primary);}",
      ".crisp-error{max-width:560px;margin:8vh auto 0;text-align:center;}",
      ".crisp-error h2{margin:0;font-size:18px;}",
      ".crisp-error p{margin:8px 0 18px;color:var(--dsw-alias-label-secondary);font-size:13px;word-break:break-word;}",
      ".crisp-loading{max-width:560px;margin:8vh auto 0;text-align:center;color:var(--dsw-alias-label-secondary);font-size:13px;}",
      ".crisp-warn{margin:0;padding:10px 14px;border:1px solid var(--dsw-alias-border-l1);border-radius:10px;background:var(--dsw-alias-bg-layer-1);color:var(--dsw-alias-state-warn-label);font-size:12px;line-height:1.6;}",
      "@media (max-width:720px){.crisp-page{padding-left:18px;padding-right:18px;}.crisp-head{flex-direction:column;}.crisp-actions{width:100%;}}",
    ].join("");

    function CrispIcon(props) {
      const size = typeof props.size === "number" ? props.size : 18;
      return h(
        "svg",
        { width: size, height: size, viewBox: "0 0 24 24", fill: "none", "aria-hidden": "true" },
        h("path", {
          d: "M12 3.2 18.8 6v5.1c0 4.4-2.8 7.6-6.8 9.7-4-2.1-6.8-5.3-6.8-9.7V6L12 3.2Z",
          stroke: "currentColor",
          strokeWidth: 1.7,
          strokeLinejoin: "round",
        }),
        h("path", {
          d: "M8.6 12.1 10.9 14.4 15.5 9.8",
          stroke: "currentColor",
          strokeWidth: 1.7,
          strokeLinecap: "round",
          strokeLinejoin: "round",
        }),
      );
    }

    function RefreshIcon() {
      return h(
        "svg",
        { width: 16, height: 16, viewBox: "0 0 24 24", fill: "none", "aria-hidden": "true" },
        h("path", {
          d: "M20 11a8 8 0 1 0-.6 4.2M20 5v6h-6",
          stroke: "currentColor",
          strokeWidth: 1.7,
          strokeLinecap: "round",
          strokeLinejoin: "round",
        }),
      );
    }

    function useStatus() {
      const [state, setState] = React.useState({ kind: "loading" });
      const mounted = React.useRef(true);
      const load = React.useCallback(async () => {
        try {
          const response = await fetch(STATUS_PATH, {
            method: "GET",
            credentials: "same-origin",
            cache: "no-store",
            headers: { accept: "application/json" },
          });
          if (!response.ok) throw new Error("HTTP " + response.status);
          const payload = await response.json();
          if (payload === null || typeof payload !== "object") throw new Error("invalid payload");
          if (mounted.current) {
            setState({ kind: "ready", data: payload, at: Date.now() });
          }
        } catch (error) {
          if (!mounted.current) return;
          setState((previous) => {
            if (previous.kind === "ready") return { ...previous, error: String(error && error.message ? error.message : error) };
            return { kind: "error", message: String(error && error.message ? error.message : error) };
          });
        }
      }, []);
      React.useEffect(() => {
        mounted.current = true;
        load();
        const timer = setInterval(() => {
          if (document.visibilityState !== "hidden") load();
        }, REFRESH_MS);
        const onVisibility = () => {
          if (document.visibilityState === "visible") load();
        };
        document.addEventListener("visibilitychange", onVisibility);
        return () => {
          mounted.current = false;
          clearInterval(timer);
          document.removeEventListener("visibilitychange", onVisibility);
        };
      }, [load]);
      return { state, reload: load };
    }

    function formatTime(iso, locale) {
      const date = new Date(typeof iso === "string" ? iso : "");
      if (Number.isNaN(date.getTime())) return "—";
      const time = date.toLocaleTimeString(locale, { hour12: false, hour: "2-digit", minute: "2-digit", second: "2-digit" });
      const today = new Date();
      const sameDay = date.getFullYear() === today.getFullYear() && date.getMonth() === today.getMonth() && date.getDate() === today.getDate();
      return sameDay ? time : date.toLocaleDateString(locale, { month: "2-digit", day: "2-digit" }) + " " + time;
    }

    function reasonLabel(reasonType, chinese) {
      const entry = REASON_LABELS[reasonType];
      if (entry === undefined) return typeof reasonType === "string" && reasonType !== "" ? reasonType : "—";
      return chinese ? entry.zh : entry.en;
    }

    function DecisionBadge(props) {
      const t = props.t;
      if (props.failOpen) return h("span", { className: "crisp-badge fail" }, t("failOpen"));
      if (props.decision === "block") return h("span", { className: "crisp-badge block" }, t("block"));
      return h("span", { className: "crisp-badge pass" }, t("pass"));
    }

    function StatCard(props) {
      return h(
        "section",
        { className: "crisp-card" },
        h("div", { className: "crisp-card-label" }, props.label),
        h("div", { className: "crisp-card-value" }, props.value),
        props.hint ? h("div", { className: "crisp-card-hint" }, props.hint) : null,
      );
    }

    function CrispPage(props) {
      const t = typeof props.t === "function" ? props.t : (key) => key;
      const chinese = typeof t("panel") === "string" && /[\u4e00-\u9fff]/.test(t("panel"));
      const { state, reload } = useStatus();
      const [copied, setCopied] = React.useState(false);

      React.useEffect(() => {
        const style = document.createElement("style");
        style.dataset.plugin = "crisp-quality-gate";
        style.textContent = STYLES;
        document.head.appendChild(style);
        return () => {
          style.remove();
        };
      }, []);

      function copyPath(path) {
        if (typeof path !== "string" || path === "") return;
        const done = () => {
          setCopied(true);
          setTimeout(() => setCopied(false), 1500);
        };
        try {
          navigator.clipboard.writeText(path).then(done).catch(() => done());
        } catch {
          done();
        }
      }

      if (state.kind === "loading") {
        return h("main", { className: "crisp-page" }, h("div", { className: "crisp-loading" }, t("loading")));
      }
      if (state.kind === "error") {
        return h(
          "main",
          { className: "crisp-page" },
          h(
            "div",
            { className: "crisp-error" },
            h("h2", null, t("loadError")),
            h("p", null, state.message),
            h("button", { type: "button", className: "crisp-btn", onClick: reload }, t("retry")),
          ),
        );
      }

      const data = state.data;
      const config = data.config || {};
      const critic = config.critic || {};
      const summary = data.summary || {};
      const today = summary.today || {};
      const guard = summary.guard || {};
      const recent = Array.isArray(data.recent) ? data.recent : [];
      const warnings = Array.isArray(config.warnings) ? config.warnings : [];
      const locale = chinese ? "zh-CN" : "en-US";
      const mode = typeof config.mode === "string" ? config.mode : "observe";
      const enabled = config.enabled !== false;
      const criticModel = typeof critic.model === "string" && critic.model !== "" ? critic.model : t("notConfigured");
      const criticSource = critic.mode === "harness" ? "Harness" : critic.baseURLHost ? "HTTP · " + critic.baseURLHost : "HTTP";
      const todayHint = t("todayPass") + " " + (today.pass || 0) + " · " + t("todayBlock") + " " + (today.block || 0) + " · " + t("todayFailOpen") + " " + (today.failOpen || 0);
      const latencyValue = summary.avgCriticLatencyMs > 0 ? summary.avgCriticLatencyMs + " ms" : "—";
      const latencyHint = summary.criticCalls > 0 ? summary.criticCalls + " " + t("criticCalls") : t("noCriticCalls");
      const updatedText = state.at ? t("updated") + " " + new Date(state.at).toLocaleTimeString(locale, { hour12: false }) : "";
      const logFile = typeof config.logFile === "string" ? config.logFile : "";
      const logFound = data.log && data.log.exists !== false;

      return h(
        "main",
        { className: "crisp-page" },
        h(
          "div",
          { className: "crisp-inner" },
          h(
            "header",
            { className: "crisp-head" },
            h(
              "div",
              null,
              h("h1", null, t("title")),
              h("p", { className: "crisp-sub" }, t("subtitle")),
            ),
            h(
              "div",
              { className: "crisp-actions" },
              h(
                "span",
                { className: "crisp-pill", title: enabled ? t("statusOn") : t("statusOff") },
                h("span", { className: "crisp-dot" + (enabled ? "" : " off") }),
                enabled ? t("statusOn") : t("statusOff"),
              ),
              h(
                "button",
                { type: "button", className: "crisp-btn", onClick: reload, title: t("refreshAria") },
                h(RefreshIcon, null),
                t("refresh"),
              ),
              props.closePanel
                ? h("button", { type: "button", className: "crisp-btn", onClick: props.closePanel }, t("back"))
                : null,
            ),
          ),
          h(
            "div",
            { className: "crisp-cards" },
            h(StatCard, {
              label: t("mode"),
              value: mode.toUpperCase(),
              hint: mode === "guard" ? t("modeGuard") : t("modeObserve"),
            }),
            h(StatCard, {
              label: t("critic"),
              value: criticModel,
              hint: critic.configured === false ? criticSource + " · " + t("notConfigured") : criticSource + (critic.thinkingOff ? " · " + t("thinkingOff") : ""),
            }),
            h(StatCard, {
              label: t("today"),
              value: (today.total || 0) + " " + t("times"),
              hint: todayHint,
            }),
            h(StatCard, {
              label: t("avgLatency"),
              value: latencyValue,
              hint: latencyHint,
            }),
          ),
          h(
            "div",
            { className: "crisp-section-head" },
            h("h2", null, t("recent")),
            h("span", null, t("recentHint")),
          ),
          recent.length === 0
            ? h(
                "div",
                { className: "crisp-empty" },
                h("strong", null, t("empty")),
                h("p", null, t("emptyHint")),
              )
            : h(
                "section",
                { className: "crisp-table" },
                h(
                  "div",
                  { className: "crisp-table-scroll" },
                  h(
                    "div",
                    null,
                    h(
                      "div",
                      { className: "crisp-row head" },
                      h("span", null, t("colTime")),
                      h("span", null, t("colDecision")),
                      h("span", null, t("colReason")),
                      h("span", null, t("colRules")),
                      h("span", null, t("colCritic")),
                      h("span", null, t("colTurn")),
                    ),
                    recent.map((check, index) =>
                      h(
                        "div",
                        { className: "crisp-row", key: String(index) + "-" + String(check.ts) },
                        h("span", { className: "mono muted" }, formatTime(check.ts, locale)),
                        h(DecisionBadge, { t, decision: check.decision, failOpen: check.failOpen }),
                        h("span", null, reasonLabel(check.reasonType, chinese)),
                        h(
                          "span",
                          { className: "crisp-rules" },
                          Array.isArray(check.ruleTypes) && check.ruleTypes.length > 0
                            ? check.ruleTypes.map((rule) => h("span", { className: "crisp-chip", key: rule }, rule))
                            : h("span", { className: "muted" }, t("none")),
                        ),
                        h(
                          "span",
                          { className: "muted" },
                          check.criticCalled
                            ? String(check.criticLatencyMs) + " ms · " + String(check.criticTokens) + " tok"
                            : t("criticUnavailable"),
                        ),
                        h("span", { className: "muted" }, check.turn === null ? "—" : "T" + check.turn),
                      ),
                    ),
                  ),
                ),
              ),
          warnings.length > 0
            ? h("p", { className: "crisp-warn" }, t("warnings") + "：" + warnings.join("；"))
            : null,
          h(
            "footer",
            { className: "crisp-footer" },
            h(
              "div",
              { className: "crisp-logpath" },
              h("span", null, t("logFile") + "："),
              h("code", { title: logFile }, logFound ? logFile : t("logMissing")),
              logFile && logFound
                ? h("button", { type: "button", className: "crisp-linkbtn", onClick: () => copyPath(logFile) }, copied ? t("copied") : t("copyPath"))
                : null,
            ),
            h("span", null, updatedText + " · " + t("autoRefresh")),
          ),
        ),
      );
    }

    return {
      inject: ["slots", "locale", "layout"],
      apply(ctx) {
        ctx.effect(() => ctx.locale.register(NS, { zh, en }), "crisp-quality-gate: client dictionaries");
        const t = ctx.locale.bind(NS);
        ctx.slots.inject("main", function* () {
          yield ctx.slots.register(
            {
              name: "main",
              key: PANEL_ID,
              locale: NS,
              inject: () => ({
                closePanel: () => ctx.layout.selectPanel(null),
                t,
              }),
            },
            CrispPage,
          );
        });
        ctx.slots.inject("sidebar.panellist", () =>
          ctx.slots.register(
            {
              name: "sidebar.panellist",
              id: PANEL_ID,
              order: 2,
              locale: NS,
              label: () => t("panel"),
            },
            CrispIcon,
          ),
        );
      },
    };
  },
});
