import { Corners } from "./Cross";
import { DuotoneIcon, type DuotoneName } from "./DuotoneIcon";
import { Container, Heading } from "./Section";

const steps: { n: string; icon: DuotoneName; title: string; line: string }[] = [
  { n: "01", icon: "tap", title: "Tap.", line: "Pay like any card." },
  { n: "02", icon: "slice", title: "Own.", line: "Part of the fee buys you shares in that shop." },
  { n: "03", icon: "chart", title: "Grow.", line: "Your shares trade on Freedom Exchange." },
];

export function HowItWorks() {
  return (
    <section id="how" className="py-24 md:py-32">
      <Container>
        <Heading eyebrow="How it works" title="Three things happen when you tap." />
        <div className="relative mt-14 md:mt-16 grid md:grid-cols-3 border border-line">
          <Corners />
          {steps.map((s, i) => (
            <div
              key={s.n}
              className={`relative p-7 md:p-9 ${i > 0 ? "border-t md:border-t-0 md:border-l border-line" : ""}`}
            >
              <div className="flex items-center justify-between">
                <span className="text-accent">
                  <DuotoneIcon name={s.icon} size={28} />
                </span>
                <span className="font-mono text-[12px] tracking-[0.12em] text-fg-subtle">{s.n}</span>
              </div>
              <h3 className="display mt-10 text-[28px] md:text-[32px]">{s.title}</h3>
              <p className="mt-2 text-[16px] text-fg-muted">{s.line}</p>
            </div>
          ))}
        </div>
      </Container>
    </section>
  );
}
