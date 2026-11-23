import React, { useEffect, useState } from "react";
import { api, SignedOutError } from "./api.js";
import { Source } from "./source-mark.jsx";
import { dayLabel, doneRate, formatCount, formatMillis, formatPercent, formatRatio, formatTokens, linePoints, niceMax, outcomes, plural, ranges, runsOn, sparkPoints, stepDays, stepSeries, touchParts } from "./dashboard.js";

const refreshMillis = 15_000;

// Run outcomes use the status colors, and the legend names them too.
const outcomeColors = { succeeded: "var(--success)", failed: "var(--danger)", cancelled: "var(--faint)" };

const prSeries = [
  { key: "opened", label: "Opened", color: "var(--chart-1)" },
  { key: "merged", label: "Merged", color: "var(--chart-2)" },
];

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

function StatTile({ label, value, detail, trend }) {
  return <div className="stat-tile">
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

function describe(day, series) {
  return `${dayLabel(day.day)}: ${series.map(({ key, label }) => `${day[key] || 0} ${label.toLowerCase()}`).join(", ")}`;
}

// DayChart is what every daily chart shares: the title, a note, the legend,
// the y axis and its gridlines, one hover column per day with a tooltip, and
// the dates. column draws a day inside its column; overlay draws over them.
function DayChart({ id, title, note, days, series, top, flush, column, overlay }) {
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
      <div className="chart-axis" aria-hidden="true">{ticks.map(tick => <span key={tick}>{formatCount(tick)}</span>)}</div>
      <div className="chart-plot" onMouseLeave={() => setActive(-1)}>
        <div className="chart-grid" aria-hidden="true"><span /><span /><span /></div>
        {overlay?.(active)}
        <div className={`chart-columns${flushClass}`}>
          {days.map((day, index) => <div key={day.day} className="chart-column" tabIndex={0} aria-label={describe(day, series)} data-active={index === active || undefined}
            onMouseEnter={() => setActive(index)} onFocus={() => setActive(index)} onBlur={() => setActive(-1)}>
            {column?.(day)}
          </div>)}
        </div>
        {shown && <div className="chart-tooltip" role="status" style={{ left: `${((active + 0.5) / days.length) * 100}%` }}>
          <strong>{dayLabel(shown.day)}</strong>
          {series.map(({ key, label, color }) => <span key={key}><span className="swatch" style={{ background: color }} aria-hidden="true" />{label} <b>{shown[key] || 0}</b></span>)}
        </div>}
      </div>
    </div>
    <div className={`chart-dates${flushClass}`} aria-hidden="true">{days.map((day, index) => <span key={day.day}>{(days.length - 1 - index) % labelEvery === 0 ? dayLabel(day.day) : ""}</span>)}</div>
  </section>;
}

// ColumnChart draws one column per day. Each series is a stacked segment, with
// a 2px gap between segments.
function ColumnChart({ id, title, note, days, series }) {
  const total = day => series.reduce((sum, { key }) => sum + (day[key] || 0), 0);
  const top = niceMax(Math.max(0, ...days.map(total)));
  return <DayChart id={id} title={title} note={note} days={days} series={series} top={top}
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
function MergeSection({ cycle = {}, touches = {}, days }) {
  const noMerges = "No merged pull requests";
  const reviewed = cycle.merged ? `${formatCount(cycle.reviewed)} of ${plural(cycle.merged, "merged PR")} reviewed by a person` : noMerges;
  return <section className="dashboard-section" aria-labelledby="merge-heading">
    <h2 id="merge-heading" className="label">Time to merge, for pull requests merged in the last {days} days</h2>
    <div className="stat-tiles stat-tiles-5">
      <SpreadTile label="Started to merged" spread={cycle.to_merge} none={noMerges} />
      <SpreadTile label="Started to PR" spread={cycle.to_pr} none={noMerges} />
      <SpreadTile label="PR to first review" spread={cycle.to_review} detail={reviewed} none={cycle.merged ? reviewed : noMerges} />
      <SpreadTile label="Review to merged" spread={cycle.review_to_merge} none={cycle.merged ? reviewed : noMerges} />
      <StatTile label="Human touches per PR" value={formatRatio(touches.per_pr)} detail={cycle.merged ? touchParts(touches) : noMerges} />
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

function RateBar({ rate }) {
  if (!Number.isFinite(rate)) return <span className="rate-none">None yet</span>;
  return <span className="rate">
    <span className="rate-track" aria-hidden="true"><span className="rate-fill" style={{ width: `${rate * 100}%` }} /></span>
    {formatPercent(rate)}
  </span>;
}

function DailyTable({ days }) {
  return <details className="panel daily">
    <summary className="label">Daily numbers</summary>
    <table>
      <thead><tr><th scope="col">Day</th><th scope="col">Jobs started</th>{outcomes.map(([key, label]) => <th key={key} scope="col">{label}</th>)}<th scope="col">PRs opened</th><th scope="col">PRs merged</th></tr></thead>
      <tbody>{[...days].reverse().map(day => <tr key={day.day}><th scope="row">{dayLabel(day.day)}</th><td>{day.jobs}</td>{outcomes.map(([key]) => <td key={key}>{day[key]}</td>)}<td>{day.opened || 0}</td><td>{day.merged || 0}</td></tr>)}</tbody>
    </table>
  </details>;
}

export function DashboardPage({ onSignedOut }) {
  const [days, setDays] = useState(14);
  const [stats, setStats] = useState(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let stopped = false;
    let timer;
    const controller = new AbortController();
    const load = async () => {
      try {
        const reply = await api(`/api/v1/stats?days=${days}`, { signal: controller.signal });
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
  }, [days, onSignedOut]);

  const current = stats?.days === days ? stats : null;
  const runs = current?.runs;
  const finished = runs ? runs.succeeded + runs.failed : 0;
  const steps = stepSeries(current?.steps || []);
  return <div className="page dashboard-page">
    <header className="page-head">
      <h1>Dashboard</h1>
      <div className="range" role="group" aria-label="Time range">
        {ranges.map(range => <button key={range} type="button" aria-pressed={range === days} onClick={() => setDays(range)}>{range} days</button>)}
      </div>
    </header>
    {error && <p className="form-error" role="alert">{error}</p>}
    {!current && !error && <p className="page-note" role="status">Loading the dashboard</p>}
    {current && <>
      <div className="stat-tiles">
        <StatTile label="Jobs" value={formatCount(current.jobs.total)} detail={`${formatCount(current.jobs.done)} done, ${formatCount(current.jobs.failed)} failed`} trend={current.daily.map(day => day.jobs)} />
        <StatTile label="Runs" value={formatCount(runs.total)} detail={`${formatCount(current.jobs.active)} jobs active`} trend={current.daily.map(runsOn)} />
        <StatTile label="Run success" value={formatPercent(current.success_rate)} detail={finished ? `${formatCount(runs.succeeded)} of ${formatCount(finished)} runs` : "No finished runs"} />
        <StatTile label="Median run" value={formatMillis(current.run_millis.median)} detail={current.run_millis.p90 != null ? `90% under ${formatMillis(current.run_millis.p90)}` : "No finished runs"} />
        <StatTile label="Median wait" value={formatMillis(current.wait_millis.median)} detail={current.wait_millis.p90 != null ? `90% under ${formatMillis(current.wait_millis.p90)}` : "No runs started"} />
        <StatTile label="Tokens" value={formatTokens(current.tokens)} detail={current.cost ? `$${current.cost.toFixed(2)}` : "No cost reported"} />
      </div>
      <MergeSection cycle={current.cycle} touches={current.touches} days={current.days} />
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
    </>}
  </div>;
}
