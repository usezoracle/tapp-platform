import { Container, Heading } from "./Section";

const faqs = [
  {
    q: "Is this a bank card?",
    a: "No. It is a prepaid card. You top it up first and spend what you loaded, so there is nothing to owe and nothing to be charged interest on.",
  },
  {
    q: "Where can I use it?",
    a: "At any shop listed on Freedom. The app shows you every one of them, and the list grows as businesses join.",
  },
  {
    q: "What if I return something?",
    a: "You get your money back as normal. The shares that tap bought are returned too, which is why shares from taps unlock after 120 days.",
  },
  {
    q: "Can I sell my shares?",
    a: "Yes, once they have unlocked. They trade on Freedom Exchange every trading day at the market price, from inside the app.",
  },
  {
    q: "How do I list my business?",
    a: "Register on the business portal, tell us about your shop, and choose how much of each tap becomes shares. It takes an afternoon.",
  },
];

export function Faq() {
  return (
    <section id="faq" className="border-t border-line py-24 md:py-32">
      <Container>
        <div className="grid gap-10 lg:grid-cols-12">
          <div className="lg:col-span-4">
            <Heading eyebrow="Questions" title="Asked often." />
          </div>
          <div className="lg:col-span-7 lg:col-start-6 border-t border-line">
            {faqs.map((f) => (
              <details key={f.q} className="group border-b border-line">
                <summary className="flex items-center justify-between gap-6 py-5 text-[17px] font-medium">
                  {f.q}
                  <svg
                    className="faq-plus shrink-0 text-fg-subtle transition-transform"
                    width="16"
                    height="16"
                    viewBox="0 0 16 16"
                    aria-hidden
                  >
                    <path d="M8 2v12M2 8h12" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
                  </svg>
                </summary>
                <p className="pb-6 pr-10 text-[16px] text-fg-muted max-w-[560px]">{f.a}</p>
              </details>
            ))}
          </div>
        </div>
      </Container>
    </section>
  );
}
