export function DisclosureAction({ noun = "details" }) {
  return (
    <span className="disclosure-action">
      <span className="disclosure-closed">View {noun}</span>
      <span className="disclosure-open">Hide {noun}</span>
    </span>
  );
}
