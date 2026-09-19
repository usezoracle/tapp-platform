import { Button } from "./Button";
import { Container, Heading } from "./Section";
import { LINKS } from "@/lib/links";

const benefits = [
  {
    title: "They come back.",
    line: "A customer who owns a slice of your shop chooses it over the one next door.",
  },
  {
    title: "Listing takes an afternoon.",
    line: "Register, tell us about the business, set your terms. That is the whole of it.",
  },
  {
    title: "You set the share.",
    line: "You choose how much of each tap becomes shares. Not us.",
  },
];

export function ForBusinesses() {
  return (
    <section id="businesses" className="scope-dark bg-ground text-fg border-y border-line py-24 md:py-32">
      <Container>
        <div className="grid gap-12 lg:grid-cols-12 lg:gap-8">
          <div className="lg:col-span-5">
            <Heading
              eyebrow="For businesses"
              title="Turn customers into owners."
              lede="List your shop on Freedom and every tap at your counter hands the customer a small piece of it. They come back, because now it is theirs too."
            />
            <div className="mt-8 flex flex-wrap gap-3">
              <Button href={LINKS.business}>List your business</Button>
              <Button href={LINKS.businessSignIn} variant="hairline">
                Sign in to the portal
              </Button>
            </div>
          </div>
          <ol className="lg:col-span-6 lg:col-start-7 divide-y divide-line border-y border-line">
            {benefits.map((b, i) => (
              <li key={b.title} className="grid grid-cols-[48px_1fr] gap-4 py-7">
                <span className="font-mono text-[12px] tracking-[0.12em] text-fg-subtle pt-1.5">0{i + 1}</span>
                <div>
                  <h3 className="display text-[24px] md:text-[26px]">{b.title}</h3>
                  <p className="mt-2 text-[16px] text-fg-muted max-w-[420px]">{b.line}</p>
                </div>
              </li>
            ))}
          </ol>
        </div>
      </Container>
    </section>
  );
}
