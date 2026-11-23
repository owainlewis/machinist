import React, { useEffect, useState } from "react";
import { Select } from "@/components/ui/select";
import { harnessLabel } from "./agents.js";
import { api, SignedOutError } from "./api.js";
import { HarnessMark } from "./harness-mark.jsx";
import { Source } from "./source-mark.jsx";
import {
  availableRepos, dailyTouchTrend, dayLabel, doneRate, formatCostPerPR, formatCount, formatMedianCost,
  formatMillis, formatPercent, formatRatio, formatSpend, formatTokens, formatUSD, linePoints, modelLabel,
  monthLabel, monthSpendOf, niceMax, outcomes, plural, ranges, runsOn, sortBySpend, sparkPoints,
  spendShare, statsPath, stepDays, stepSeries, tabs, touchParts, unreportedNote,
} from "./dashboard.js";

const refreshMillis = 15_000;

// Run outcomes use the status colors, and the legend names them too.
const outcomeColors = { succeeded: "var(--success)", failed: "var(--danger)", cancelled: "var(--faint)" };

const prSeries = [
  { key: "opened", label: "Opened", color: "var(--chart-1)" },
  { key: "merged", label: "Merged", color: "var(--chart-2)" },
];

const spendSeries = [{ key: "cost", label: "Reported spend", color: "var(--chart-1)" }];

// Sparkline is the shape of a daily series, drawn behind a tile's number. The
// numbers are in the charts and the daily table, so it is hidden from readers.
function Sparkline({ values }) {
  const points = sparkPoints(values, 100, 32);
  if (!points.length) return null;
  const line = points.map(([x, y]) => `${x},${y}`).join(" ");
  return <svg className="sparkline" viewBox="0 0 100 32" preserveAspectRatio="none" aria-hidden="true">
    <polygon points={`0,32 ${line} 100,32`} />
    <polyline points={line} vectorEffect="non-scaling-stroke" />
  </svg>;
}

function StatTile({ label, value, detail, trend, title }) {
  return <div className="stat-tile" title={title}>
    <p className="label">{label}</p>
    <p className="stat-value">{value}</p>
    {detail && <p className="stat-detail">{detail}</p>}
    {trend && <Sparkline values={trend} />}
  </div>;
}

// Legend names each series beside its color, so color is never the only key.
function Legend({ series }) {
  return <ul className="legend">{series.map(({ key, label, color }) => <li key={key}><span className="swatch" style={{ background: color }} aria-hidden="true" />{label}</li>)}</ul>;
}

function describe(day, series, formatValue) {
  return `${dayLabel(day.day)}: ${series.map(({ key, label }) => `${formatValue(day[key] || 0, day)} ${label.toLowerCase()}`).join(", ")}`;
}

// DayChart is what every daily chart shares: the title, a note, the legend,
// the y axis and its gridlines, one hover column per day with a tooltip, and
// the dates. column draws a day inside its column; overlay draws over them.
function DayChart({ id, title, note, days, series, top, flush, column, overlay, formatTick = formatCount, formatValue = value => value, tooltipExtra }) {
  const [active, setActive] = useState(-1);
  const ticks = [top, top / 2, 0];
  const labelEvery = Math.ceil(days.length / 7);
  const shown = days[active];
  const flushClass = flush ? " chart-flush" : "";
  return <section className="panel chart" aria-labelledby={id}>
    <div className="chart-head">
      <h2 id={id} className="chart-title">{title}</h2>
      {note}
      {series.length > 1 && <Legend series={series} />}
    </div>
    <div className="chart-body">
      <div className="chart-axis" aria-hidden="true">{ticks.map((tick, index) => <span key={index}>{formatTick(tick)}</span>)}</div>
      <div className="chart-plot" onMouseLeave={() => setActive(-1)}>
        <div className="chart-grid" aria-hidden="true"><span /><span /><span /></div>
        {overlay?.(active)}
        <div className={`chart-columns${flushClass}`}>
          {days.map((day, index) => <div key={day.day} className="chart-column" tabIndex={0} aria-label={describe(day, series, formatValue)} data-active={index === active || undefined}
            onMouseEnter={() => setActive(index)} onFocus={() => setActive(index)} onBlur={() => setActive(-1)}>
            {column?.(day)}
          </div>)}
        </div>
        {shown && <div className="chart-tooltip" role="status" style={{ left: `${((active + 0.5) / days.length) * 100}%` }}>
          <strong>{dayLabel(shown.day)}</strong>
          {series.map(({ key, label, color }) => <span key={key}><span className="swatch" style={{ background: color }} aria-hidden="true" />{label} <b>{formatValue(shown[key] || 0, shown)}</b></span>)}
          {tooltipExtra?.(shown)}
        </div>}
      </div>
    </div>
    <div className={`chart-dates${flushClass}`} aria-hidden="true">{days.map((day, index) => <span key={day.day}>{(days.length - 1 - index) % labelEvery === 0 ? dayLabel(day.day) : ""}</span>)}</div>
  </section>;
}

// ColumnChart draws one column per day. Each series is a stacked segment, with
// a 2px gap between segments.
function ColumnChart({ id, title, note, days, series, formatTick, formatValue, tooltipExtra }) {
  const total = day => series.reduce((sum, { key }) => sum + (day[key] || 0), 0);
  const top = niceMax(Math.max(0, ...days.map(total)));
  return <DayChart id={id} title={title} note={note} days={days} series={series} top={top}
    formatTick={formatTick} formatValue={formatValue} tooltipExtra={tooltipExtra}
    column={day => <div className="chart-stack" style={{ height: `${(total(day) / top) * 100}%` }}>
      {[...series].reverse().map(({ key, color }) => day[key] ? <span key={key} className="chart-segment" style={{ flexGrow: day[key], background: color }} /> : null)}
    </div>} />;
}

// LineChart draws one 2px line per series, on one axis. The hover column
// marks each line's point for that day.
function LineChart({ id, title, note, days, series }) {
  const top = niceMax(Math.max(0, ...days.flatMap(day => series.map(({ key }) => day[key] || 0))));
  const lines = series.map(({ key, color }) => ({ key, color, points: linePoints(days.map(day => day[key]), top) }));
  return <DayChart id={id} title={title} note={note} days={days} series={series} top={top} flush
    overlay={active => <>
      <svg className="chart-lines" viewBox="0 0 100 100" preserveAspectRatio="none" aria-hidden="true">
        {lines.map(({ key, color, points }) => <polyline key={key} points={points.map(point => point.join(",")).join(" ")} style={{ stroke: color }} vectorEffect="non-scaling-stroke" />)}
      </svg>
      {active >= 0 && lines.map(({ key, color, points }) => <span key={key} className="chart-marker" aria-hidden="true" style={{ left: `${points[active][0]}%`, top: `${points[active][1]}%`, background: color }} />)}
    </>} />;
}

// SpreadTile shows a median time, with the time nine in ten came in under.
function SpreadTile({ label, spread = {}, detail, none }) {
  return <StatTile label={label} value={formatMillis(spread.median)} detail={spread.p90 != null ? detail || `90% under ${formatMillis(spread.p90)}` : none} />;
}

// MergeSection answers how long a job takes to merge and how much of a
// person it needs, from the pull requests that merged in the range.
function MergeSection({ cycle = {}, touches = {}, days, daily = [] }) {
  const noMerges = "No merged pull requests";
  const reviewed = cycle.merged ? `${formatCount(cycle.reviewed)} of ${plural(cycle.merged, "merged PR")} reviewed by a person` : noMerges;
  return <section className="dashboard-section" aria-labelledby="merge-heading">
    <h2 id="merge-heading" className="label">Time to merge, for pull requests merged in the last {days} days</h2>
    <div className="stat-tiles stat-tiles-5">
      <SpreadTile label="Started to merged" spread={cycle.to_merge} none={noMerges} />
      <SpreadTile label="Started to PR" spread={cycle.to_pr} none={noMerges} />
      <SpreadTile label="PR to first review" spread={cycle.to_review} detail={reviewed} none={cycle.merged ? reviewed : noMerges} />
      <SpreadTile label="Review to merged" spread={cycle.review_to_merge} none={cycle.merged ? reviewed : noMerges} />
      <StatTile label="Human touches per PR" value={formatRatio(touches.per_pr)} detail={cycle.merged ? touchParts(touches) : noMerges} trend={dailyTouchTrend(daily)} />
    </div>
  </section>;
}

// GroupTable counts jobs by one thing, such as preset. name draws a group's
// name, for groups that are more than text.
function GroupTable({ id, title, groups = [], name = group => group.name }) {
  return <section className="panel group-table" aria-labelledby={id}>
    <h2 id={id} className="chart-title">{title}</h2>
    {groups.length ? <table>
      <thead><tr><th scope="col">Name</th><th scope="col">Jobs</th><th scope="col">Done</th><th scope="col">Failed</th><th scope="col">Done rate</th><th scope="col">Median run</th></tr></thead>
      <tbody>{groups.map(group => <tr key={group.name}>
        <th scope="row">{name(group)}</th>
        <td>{formatCount(group.jobs.total)}</td>
        <td>{formatCount(group.jobs.done)}</td>
        <td>{formatCount(group.jobs.failed)}</td>
        <td><RateBar rate={doneRate(group)} /></td>
        <td>{formatMillis(group.run_millis)}</td>
      </tr>)}</tbody>
    </table> : <p className="page-note">No jobs in this range.</p>}
  </section>;
}

function RateBar({ rate, title }) {
  if (!Number.isFinite(rate)) return <span className="rate-none">None yet</span>;
  return <span className="rate" title={title}>
    <span className="rate-track" aria-hidden="true"><span className="rate-fill" style={{ width: `${Math.min(Math.max(rate, 0), 1) * 100}%` }} /></span>
    {formatPercent(rate)}
  </span>;
}

function ModelName({ entry }) {
  return <span className="model-cell">
    <HarnessMark harness={entry.harness} className="size-3.5" />
    <span className="model-cell-text">
      <span className="model-cell-name">{modelLabel(entry)}</span>
      <span className="model-cell-harness">{harnessLabel(entry.harness)}</span>
    </span>
  </span>;
}

function SpendCell({ entry, unit = "run" }) {
  const unknown = entry?.cost_unknown || 0;
  const note = unreportedNote(unknown, unit);
  return <span className="spend-cell" title={unknown > 0 ? "Includes runs from harnesses that do not report USD cost" : undefined}>
    <span>{formatSpend(entry)}</span>
    {note && <span className="spend-sub">{note}</span>}
  </span>;
}

function DailyTable({ days }) {
  return <details className="panel daily">
    <summary className="label">Daily numbers</summary>
    <table>
      <thead><tr><th scope="col">Day</th><th scope="col">Jobs started</th>{outcomes.map(([key, label]) => <th key={key} scope="col">{label}</th>)}<th scope="col">PRs opened</th><th scope="col">PRs merged</th><th scope="col">Spend</th></tr></thead>
      <tbody>{[...days].reverse().map(day => <tr key={day.day}><th scope="row">{dayLabel(day.day)}</th><td>{day.jobs}</td>{outcomes.map(([key]) => <td key={key}>{day[key]}</td>)}<td>{day.opened || 0}</td><td>{day.merged || 0}</td><td>{formatSpend(day)}</td></tr>)}</tbody>
    </table>
  </details>;
}

function overviewSpendDetail(current, mtd) {
  const parts = [];
  if (Number.isFinite(current.cost_per_merged_pr)) {
    parts.push(`${formatUSD(current.cost_per_merged_pr)}${current.cost_unknown ? "+" : ""} / merged PR`);
  } else if (current.prs?.merged > 0 && current.cost_unknown > 0) {
    parts.push("Cost / PR not reported");
  } else if (mtd.tokens || current.tokens) {
    parts.push(`${formatTokens(mtd.tokens || current.tokens)} tokens`);
  }
  if (mtd.cost_unknown > 0) {
    if (!parts.some(part => part.includes("tokens")) && mtd.tokens > 0) parts.push(`${formatTokens(mtd.tokens)} tokens`);
    parts.push(unreportedNote(mtd.cost_unknown));
  }
  return parts.join(" · ") || "No spend yet";
}

function OverviewTab({ current }) {
  const runs = current.runs;
  const finished = runs ? runs.succeeded + runs.failed : 0;
  const steps = stepSeries(current.steps || []);
  const mtd = monthSpendOf(current);
  return <>
    <div className="stat-tiles">
      <StatTile label="Jobs" value={formatCount(current.jobs.total)} detail={`${formatCount(current.jobs.done)} done, ${formatCount(current.jobs.failed)} failed`} trend={current.daily.map(day => day.jobs)} />
      <StatTile label="Runs" value={formatCount(runs.total)} detail={`${formatCount(current.jobs.active)} jobs active`} trend={current.daily.map(runsOn)} />
      <StatTile label="Run success" value={formatPercent(current.success_rate)} detail={finished ? `${formatCount(runs.succeeded)} of ${formatCount(finished)} runs` : "No finished runs"} />
      <StatTile label="Median run" value={formatMillis(current.run_millis.median)} detail={current.run_millis.p90 != null ? `90% under ${formatMillis(current.run_millis.p90)}` : "No finished runs"} />
      <StatTile label="Median wait" value={formatMillis(current.wait_millis.median)} detail={current.wait_millis.p90 != null ? `90% under ${formatMillis(current.wait_millis.p90)}` : "No runs started"} />
      <StatTile label="Spend this month" value={formatSpend(mtd)} detail={overviewSpendDetail(current, mtd)}
        title={mtd.cost_unknown > 0 ? "Some runs used harnesses that do not report USD cost" : undefined} />
    </div>
    <MergeSection cycle={current.cycle} touches={current.touches} days={current.days} daily={current.daily} />
    <div className="chart-row">
      <LineChart id="prs-chart" title="Pull requests per day" days={current.daily} series={prSeries}
        note={<p className="chart-note">Merge rate <b>{formatPercent(current.prs?.merge_rate)}</b>{current.prs?.opened ? ` of ${plural(current.prs.opened, "opened PR")}` : ""}</p>} />
      <ColumnChart id="steps-chart" title="Step runs per day" days={stepDays(current.daily, steps)} series={steps}
        note={!steps.length && <p className="chart-note">No steps ran in this range</p>} />
    </div>
    <div className="chart-row">
      <ColumnChart id="runs-chart" title="Runs per day" days={current.daily} series={outcomes.map(([key, label]) => ({ key, label, color: outcomeColors[key] }))} />
      <ColumnChart id="jobs-chart" title="Jobs started per day" days={current.daily} series={[{ key: "jobs", label: "Jobs", color: "var(--chart-1)" }]} />
    </div>
    <div className="chart-row">
      <GroupTable id="presets-table" title="By preset" groups={current.presets} />
      <GroupTable id="repos-table" title="By repository" groups={current.repos} />
    </div>
    <GroupTable id="sources-table" title="By source" groups={current.sources} name={group => <Source source={group.name} />} />
    <DailyTable days={current.daily} />
  </>;
}

function ModelsTab({ current }) {
  const [byStep, setByStep] = useState(false);
  const [stepFilter, setStepFilter] = useState("");
  const allRows = byStep ? (current.model_steps || []) : (current.models || []);
  const stepNames = [...new Set((current.model_steps || []).map(row => row.step).filter(Boolean))];
  const activeStep = stepNames.includes(stepFilter) ? stepFilter : "";
  const rows = byStep && activeStep ? allRows.filter(row => row.step === activeStep) : allRows;
  const hasUnknown = rows.some(row => row.cost_unknown > 0);

  return <section className="panel model-table" aria-labelledby="models-heading">
    <div className="chart-head">
      <div>
        <h2 id="models-heading" className="chart-title">Harness &amp; model comparison</h2>
        <p className="chart-note">Last {current.days} days{current.repo ? ` · ${current.repo}` : ""}</p>
      </div>
      <div className="dashboard-toggle" role="group" aria-label="Group models by">
        <button type="button" aria-pressed={!byStep} onClick={() => setByStep(false)}>All steps</button>
        <button type="button" aria-pressed={byStep} onClick={() => setByStep(true)}>By step</button>
      </div>
    </div>
    {byStep && stepNames.length > 1 && <div className="quick-filters model-step-filters" role="group" aria-label="Filter by step">
      <button type="button" className="filter-chip" aria-pressed={!activeStep} onClick={() => setStepFilter("")}>All steps</button>
      {stepNames.map(step => <button key={step} type="button" className="filter-chip" aria-pressed={activeStep === step} onClick={() => setStepFilter(activeStep === step ? "" : step)}>{step}</button>)}
    </div>}
    {rows.length ? <div className="table-scroll">
      <table>
        <thead>
          <tr>
            {byStep && <th scope="col">Step</th>}
            <th scope="col" className="model-name-col">Harness &amp; model</th>
            <th scope="col">Runs / steps</th>
            <th scope="col">Pass rate</th>
            <th scope="col">Merge rate</th>
            <th scope="col">Median duration</th>
            <th scope="col">Median cost</th>
            <th scope="col">Total spend</th>
            <th scope="col">Tokens</th>
            <th scope="col">Touches / PR</th>
          </tr>
        </thead>
        <tbody>
          {rows.map(row => <tr key={`${row.step || ""}:${row.harness}:${row.model}`}>
            {byStep && <td className="model-step-cell"><span className="step-tag">{row.step}</span></td>}
            <th scope="row"><ModelName entry={row} /></th>
            <td>{formatCount(row.runs)} / {formatCount(row.steps)}</td>
            <td><RateBar rate={row.pass_rate} title={`${formatCount(row.passed)} passed, ${formatCount(row.failed)} failed`} /></td>
            <td><RateBar rate={row.merge_rate} title={row.opened_prs ? `${formatCount(row.merged_prs)} of ${plural(row.opened_prs, "opened PR")} merged` : undefined} /></td>
            <td>{formatMillis(row.duration_millis?.median)}</td>
            <td>{formatMedianCost(row)}</td>
            <td><SpendCell entry={row} unit="step" /></td>
            <td>{formatTokens(row.tokens)}</td>
            <td>
              <span className="spend-cell">
                <span>{formatRatio(row.touches_per_pr)}</span>
                {Number.isFinite(row.rework_rate) && <span className="spend-sub">{formatPercent(row.rework_rate)} rework</span>}
              </span>
            </td>
          </tr>)}
        </tbody>
      </table>
    </div> : <p className="page-note">No model steps ran in the last {current.days} days.</p>}
    {hasUnknown && <p className="spend-note">Some harnesses do not report USD cost; those steps show token counts and are marked unreported instead of $0.00.</p>}
  </section>;
}

function SpendGroupTable({ id, title, groups = [] }) {
  const sorted = sortBySpend(groups);
  return <section className="panel group-table spend-group-table" aria-labelledby={id}>
    <h2 id={id} className="chart-title">{title}</h2>
    {sorted.length ? <div className="table-scroll">
      <table>
        <thead><tr><th scope="col">Name</th><th scope="col">Runs</th><th scope="col">Merged PRs</th><th scope="col">Total spend</th><th scope="col">Cost / PR</th><th scope="col">Tokens</th></tr></thead>
        <tbody>{sorted.map(group => {
          const perPR = group.merged_prs > 0 && group.has_cost ? group.cost / group.merged_prs : null;
          return <tr key={group.name}>
            <th scope="row">{group.name}</th>
            <td>{formatCount(group.runs)}</td>
            <td>{formatCount(group.merged_prs)}</td>
            <td><SpendCell entry={group} /></td>
            <td>{formatCostPerPR(perPR, { mergedPRs: group.merged_prs, costUnknown: group.cost_unknown })}</td>
            <td>{formatTokens(group.tokens)}</td>
          </tr>;
        })}</tbody>
      </table>
    </div> : <p className="page-note">No spend in this range.</p>}
  </section>;
}

function SpendingTab({ current }) {
  const mtd = monthSpendOf(current);
  const rangeSpend = { cost: current.cost, has_cost: current.has_cost || current.cost > 0, cost_unknown: current.cost_unknown || 0, tokens: current.tokens };
  const modelsBySpend = [...(current.models || [])].sort((a, b) => (b.cost || 0) - (a.cost || 0) || (b.tokens || 0) - (a.tokens || 0));
  const totalModelCost = modelsBySpend.reduce((sum, row) => sum + (row.cost || 0), 0);
  const mtdDetail = [monthLabel(mtd.month), `${formatTokens(mtd.tokens)} tokens`, unreportedNote(mtd.cost_unknown)].filter(Boolean).join(" · ");
  const rangeDetail = [plural(current.runs?.total || 0, "run"), `${formatTokens(current.tokens)} tokens`, unreportedNote(current.cost_unknown)].filter(Boolean).join(" · ");
  const perPRDetail = [
    current.prs?.merged ? `${plural(current.prs.merged, "merged PR")} in last ${current.days} days` : "No merged PRs in range",
    unreportedNote(current.cost_unknown),
  ].filter(Boolean).join(" · ");
  const tokensDetail = current.cost_unknown > 0
    ? `${plural(current.cost_unknown, "run")} with unreported cost`
    : current.runs?.total > 0 ? "All runs reported cost" : "No runs in range";

  return <>
    <div className="stat-tiles stat-tiles-4">
      <StatTile label="Total spend this month" value={formatSpend(mtd)} detail={mtdDetail} />
      <StatTile label={`Spend (${current.days} days)`} value={formatSpend(rangeSpend)} detail={rangeDetail} trend={current.daily.map(day => day.cost || 0)} />
      <StatTile label="Cost per merged PR" value={formatCostPerPR(current.cost_per_merged_pr, { mergedPRs: current.prs?.merged || 0, costUnknown: current.cost_unknown || 0 })} detail={perPRDetail} />
      <StatTile label={`Tokens (${current.days} days)`} value={formatTokens(current.tokens)} detail={tokensDetail} />
    </div>
    <ColumnChart id="spend-chart" title="Daily spend" days={current.daily} series={spendSeries}
      formatTick={tick => formatUSD(tick)}
      formatValue={(_, day) => formatSpend(day)}
      note={<p className="chart-note">Total <b>{formatSpend(rangeSpend)}</b>{current.cost_unknown > 0 ? ` · ${unreportedNote(current.cost_unknown)}` : ""}</p>}
      tooltipExtra={day => <>
        {day.tokens > 0 && <span>Tokens <b>{formatTokens(day.tokens)}</b></span>}
        {day.cost_unknown > 0 && <span>Unreported <b>{plural(day.cost_unknown, "run")}</b></span>}
      </>} />
    <section className="panel model-table" aria-labelledby="spend-models-table">
      <h2 id="spend-models-table" className="chart-title">Spend by model</h2>
      {modelsBySpend.length ? <div className="table-scroll">
        <table>
          <thead><tr><th scope="col">Harness &amp; model</th><th scope="col">Steps</th><th scope="col">Total spend</th><th scope="col">Median cost</th><th scope="col">Tokens</th><th scope="col">Share of spend</th></tr></thead>
          <tbody>{modelsBySpend.map(row => <tr key={`${row.harness}:${row.model}`}>
            <th scope="row"><ModelName entry={row} /></th>
            <td>{formatCount(row.steps)}</td>
            <td><SpendCell entry={row} unit="step" /></td>
            <td>{formatMedianCost(row)}</td>
            <td>{formatTokens(row.tokens)}</td>
            <td><RateBar rate={row.has_cost ? spendShare(row.cost, totalModelCost) : NaN} /></td>
          </tr>)}</tbody>
        </table>
      </div> : <p className="page-note">No model spend in this range.</p>}
    </section>
    <div className="chart-row">
      <SpendGroupTable id="spend-repos-table" title="Spend by repository" groups={current.repos} />
      <SpendGroupTable id="spend-presets-table" title="Spend by preset" groups={current.presets} />
    </div>
  </>;
}

export function DashboardPage({ onSignedOut }) {
  const [days, setDays] = useState(14);
  const [repo, setRepo] = useState("");
  const [tab, setTab] = useState("overview");
  const [stats, setStats] = useState(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let stopped = false;
    let timer;
    const controller = new AbortController();
    const load = async () => {
      try {
        const reply = await api(statsPath(days, repo), { signal: controller.signal });
        if (stopped) return;
        setStats(reply);
        setError("");
      } catch (failure) {
        if (stopped || failure.name === "AbortError") return;
        if (failure instanceof SignedOutError) { onSignedOut(); return; }
        setError(failure.message);
      }
      if (!stopped) timer = window.setTimeout(load, refreshMillis);
    };
    load();
    return () => { stopped = true; controller.abort(); window.clearTimeout(timer); };
  }, [days, repo, onSignedOut]);

  const current = stats?.days === days && (stats?.repo || "") === repo ? stats : null;
  const repos = availableRepos(stats, repo);

  return <div className="page dashboard-page">
    <header className="page-head">
      <h1>Dashboard</h1>
      <div className="dashboard-controls">
        {repos.length > 0 && <Select className="repo-filter" value={repo} onChange={setRepo} aria-label="Repository">
          <option value="">All repositories</option>
          {repos.map(name => <option key={name} value={name}>{name}</option>)}
        </Select>}
        <div className="range" role="group" aria-label="Time range">
          {ranges.map(range => <button key={range} type="button" aria-pressed={range === days} onClick={() => setDays(range)}>{range} days</button>)}
        </div>
      </div>
    </header>
    <div className="dashboard-tabs" role="tablist" aria-label="Dashboard sections">
      {tabs.map(([key, label]) => <button key={key} type="button" role="tab" id={`tab-${key}`} aria-selected={tab === key} aria-controls={`panel-${key}`}
        className={tab === key ? "dashboard-tab dashboard-tab-active" : "dashboard-tab"} onClick={() => setTab(key)}>{label}</button>)}
    </div>
    {error && <p className="form-error" role="alert">{error}</p>}
    {!current && !error && <p className="page-note" role="status">Loading the dashboard</p>}
    {current && <div role="tabpanel" id={`panel-${tab}`} aria-labelledby={`tab-${tab}`} className="dashboard-panel">
      {tab === "overview" && <OverviewTab current={current} />}
      {tab === "models" && <ModelsTab current={current} />}
      {tab === "spending" && <SpendingTab current={current} />}
    </div>}
  </div>;
}
