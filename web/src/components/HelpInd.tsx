import clsx from "clsx";

// HelpInd is the inline indicator a column header / tile / chart can
// render to flag "there's help for this entity." Hovering shows the
// one-liner; clicking opens the drawer scrolled to the entry.
//
// Lives outside HelpDrawer.tsx so importing it does NOT drag the
// 164-entry registry into the shell chunk. The delegated click
// handler in App.tsx is what actually opens the drawer.
export function HelpInd({
  id,
  className,
}: {
  id: string;
  className?: string;
}) {
  return (
    <button
      type="button"
      data-help-id={id}
      title="Click for help · press ? for the full drawer"
      className={clsx(
        "ml-1 inline-flex h-3.5 w-3.5 items-center justify-center rounded-full border border-line-3 text-[8px] font-semibold text-fg-3 hover:border-accent hover:text-accent",
        className,
      )}
    >
      ?
    </button>
  );
}

// TitleWithHelp pairs a text title with an inline HelpInd. Designed
// to be passed as ChartShell's `title` prop (ReactNode-typed).
export function TitleWithHelp({
  text,
  helpId,
}: {
  text: string;
  helpId: string;
}) {
  return (
    <span className="inline-flex items-center">
      {text}
      <HelpInd id={helpId} />
    </span>
  );
}
