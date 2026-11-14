// What the runs of a period add up to. A cancelled run neither passed nor failed, so the
// pass rate leaves it out.
export function usageTotals(days = []) {
  const total = { succeeded: 0, failed: 0, cancelled: 0, tokens: 0, cost: 0 };
  for (const day of days) for (const key of Object.keys(total)) total[key] += day[key] || 0;
  const judged = total.succeeded + total.failed;
  return { ...total, judged, passRate: judged ? Math.round(100 * total.succeeded / judged) : null };
}

// The top of a chart's scale: the next round number whose half is round too.
export function scaleTop(value) {
  if (!(value > 0)) return 2;
  const power = 10 ** Math.floor(Math.log10(value));
  return Math.max(2, [1, 2, 4, 6, 10].find(step => step * power >= value) * power);
}
