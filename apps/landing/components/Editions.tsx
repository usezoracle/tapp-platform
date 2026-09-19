import { Button } from "./Button";
import { Container, Heading } from "./Section";
import { EDITIONS, cardBox } from "./cards";
import { LINKS } from "@/lib/links";

/** The three editions, larger and still, on the checker. */
export function Editions() {
  return (
    <section id="cards" className="relative overflow-hidden border-t border-line py-24 md:py-32">
      <div className="checker" aria-hidden />
      <Container className="relative">
        <Heading eyebrow="The cards" title="Three editions. One idea." align="center" />
        <ul className="mt-16 grid gap-14 md:grid-cols-3 md:gap-8 items-end">
          {EDITIONS.map((e) => {
            const box = cardBox(e.orientation, 176);
            return (
              <li key={e.id} className="flex flex-col items-center text-center">
                <div className="flex h-[280px] items-end">
                  <div className="card-3d" style={{ ...box, "--short": "176px" } as React.CSSProperties}>
                    <img src={e.src} alt={`The ${e.name} card`} width={box.width} height={box.height} loading="lazy" />
                  </div>
                </div>
                <h3 className="display mt-8 text-[22px]">{e.name}</h3>
                <p className="mt-2 max-w-[260px] text-[15px] text-fg-muted">{e.line}</p>
              </li>
            );
          })}
        </ul>
        <div className="mt-14 flex justify-center">
          <Button href={LINKS.getCard}>Get the card</Button>
        </div>
      </Container>
    </section>
  );
}
