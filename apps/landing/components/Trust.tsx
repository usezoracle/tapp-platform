import { Corners } from "./Cross";
import { DuotoneIcon, type DuotoneName } from "./DuotoneIcon";
import { Container, Heading } from "./Section";

const points: { icon: DuotoneName; title: string; line: string }[] = [
  {
    icon: "wallet",
    title: "Your money stays yours.",
    line: "The card is prepaid. You top up what you spend, and nothing more.",
  },
  {
    icon: "check",
    title: "Your shares are real.",
    line: "Listed on Freedom Exchange and priced every trading day.",
  },
  {
    icon: "hourglass",
    title: "Shares from taps unlock after 120 days.",
    line: "So a refund can always be honoured.",
  },
  {
    icon: "receipt",
    title: "Every tap is on the record.",
    line: "What you paid, what you own, and when. In the app, always.",
  },
];

export function Trust() {
  return (
    <section id="trust" className="border-t border-line py-24 md:py-32">
      <Container>
        <Heading eyebrow="In plain words" title="What you should know before you tap." />
        <div className="relative mt-14 md:mt-16 grid sm:grid-cols-2 border border-line">
          <Corners />
          {points.map((p, i) => (
            <div
              key={p.title}
              className={`p-7 md:p-9 border-line ${i > 0 ? "border-t sm:border-t-0" : ""} ${i % 2 === 1 ? "sm:border-l" : ""} ${i >= 2 ? "sm:border-t" : ""}`}
            >
              <span className="text-accent">
                <DuotoneIcon name={p.icon} size={26} />
              </span>
              <h3 className="display mt-8 text-[22px] md:text-[24px]">{p.title}</h3>
              <p className="mt-2 text-[16px] text-fg-muted max-w-[380px]">{p.line}</p>
            </div>
          ))}
        </div>
      </Container>
    </section>
  );
}
