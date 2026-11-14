const fromPeople = new Set(["request", "comment", "review"]);

// A report ends with the question or summary that the next message repeats.
export function reportText(body) {
  const match = body.match(/^(?:pr_ready|needs_input|no_change|failed): ([\s\S]*?)(?:\nQuestion: [\s\S]*)?$/);
  return match ? match[1] : body;
}

function instant(value) {
  const time = Date.parse(value);
  return Number.isFinite(time) ? time : Infinity;
}

// A ticket's log and runs become one conversation. Everything the agent does
// between two things a person says is one assistant turn.
export function ticketThread(entries, runs) {
  const timeline = [
    ...entries.map(entry => ({ at: instant(entry.created_at), entry })),
    ...runs.map(run => ({ at: instant(run.started_at), run })),
  ].sort((a, b) => a.at - b.at);
  const messages = [];
  let turn = null;
  const agentTurn = id => {
    if (!turn) {
      turn = { id: `turn-${id}`, role: "assistant", content: [] };
      messages.push(turn);
    }
    return turn;
  };
  for (const { entry, run } of timeline) {
    if (run) {
      agentTurn(run.id).content.push({ type: "data-run", id: run.id, data: run });
    } else if (fromPeople.has(entry.kind)) {
      turn = null;
      messages.push({ id: `entry-${entry.id}`, role: "user", content: [{ type: "text", text: entry.body }], metadata: { custom: { author: entry.author } } });
    } else if (entry.kind === "ci") {
      turn = null;
      messages.push({ id: `entry-${entry.id}`, role: "system", content: [{ type: "text", text: entry.body }] });
    } else {
      const text = entry.kind === "report" ? reportText(entry.body) : entry.body;
      const { content } = agentTurn(entry.id);
      if (content.at(-1)?.text !== text) content.push({ type: "text", text });
    }
  }
  return messages;
}
