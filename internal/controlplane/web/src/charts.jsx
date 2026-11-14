import React, { useId } from "react";
import { scaleTop } from "./usage.js";

// The server counts by UTC day, so the labels are in UTC too.
const shortDay = new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric", timeZone: "UTC" });
const longDay = new Intl.DateTimeFormat("en-US", { weekday: "short", month: "short", day: "numeric", timeZone: "UTC" });
const date = day => new Date(`${day}T00:00:00Z`);

// DayChart draws one bar per day. Each series is a segment of the bar, the first at the
// bottom. The same numbers are in a table for a screen reader.
export function DayChart({ title, days = [], series, format = String, axisFormat = format, empty, note }) {
  const id = useId();
  const top = scaleTop(Math.max(0, ...days.map(day => series.reduce((sum, { key }) => sum + (day[key] || 0), 0))));
  const any = days.some(day => series.some(({ key }) => day[key] > 0));
  const swatch = index => series.length > 1 && <i className={`swatch series-${index + 1}`} />;
  return <section className="panel" aria-labelledby={id}>
    <div className="panel-head">
      <h2 id={id} className="label">{title}</h2>
      {series.length > 1 && <ul className="legend">{series.map(({ key, label }, index) => <li key={key}>{swatch(index)}{label}</li>)}</ul>}
    </div>
    {!any && <p className="chart-empty">{empty}</p>}
    {any && <div className="plot" aria-hidden="true">
      <div className="plot-y"><span>{axisFormat(top)}</span><span>{axisFormat(top / 2)}</span><span>0</span></div>
      <div className="plot-area">
        <ol className="plot-bars">{days.map(day => <li key={day.day} className="plot-col">
          <div className="plot-tip">
            <strong>{longDay.format(date(day.day))}</strong>
            {series.map(({ key, label }, index) => <span key={key}>{swatch(index)}{label}<b>{format(day[key] || 0)}</b></span>)}
          </div>
          {series.map(({ key }, index) => day[key] > 0 && <i key={key} className={`plot-seg series-${index + 1}`} style={{ height: `${100 * day[key] / top}%` }} />)}
        </li>)}</ol>
      </div>
      <div className="plot-x">{[0, Math.floor((days.length - 1) / 2), days.length - 1].map(at => <span key={at}>{shortDay.format(date(days[at].day))}</span>)}</div>
    </div>}
    {any && <table className="sr-only">
      <caption>{title}</caption>
      <thead><tr><th scope="col">Day</th>{series.map(({ key, label }) => <th key={key} scope="col">{label}</th>)}</tr></thead>
      <tbody>{days.map(day => <tr key={day.day}><th scope="row">{longDay.format(date(day.day))}</th>{series.map(({ key }) => <td key={key}>{format(day[key] || 0)}</td>)}</tr>)}</tbody>
    </table>}
    {note && <p className="panel-foot">{note}</p>}
  </section>;
}
