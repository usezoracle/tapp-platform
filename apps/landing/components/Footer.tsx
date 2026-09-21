import { LogoWithMark } from "./Logo";
import { Container } from "./Section";
import { LINKS } from "@/lib/links";

const columns = [
  {
    title: "Cardholders",
    links: [
      { label: "Get the card", href: LINKS.getCard },
      { label: "Sign in", href: LINKS.signIn },
      { label: "The cards", href: "#cards" },
      { label: "Questions", href: "#faq" },
    ],
  },
  {
    title: "Businesses",
    links: [
      { label: "List your business", href: LINKS.business },
      { label: "Sign in to the portal", href: LINKS.businessSignIn },
      { label: "How it works", href: "#how" },
    ],
  },
  {
    title: "Exchange",
    links: [
      { label: "The market", href: LINKS.market },
      { label: "In plain words", href: "#trust" },
    ],
  },
];

export function Footer() {
  return (
    <footer className="border-t border-line py-14 md:py-16">
      <Container>
        <div className="grid gap-12 md:grid-cols-12">
          <div className="md:col-span-4">
            <LogoWithMark height={22} className="text-fg" />
            <p className="mt-5 text-[14px] text-fg-muted">Freedom Exchange · Lagos</p>
          </div>
          {columns.map((c) => (
            <div key={c.title} className="md:col-span-2">
              <h4 className="font-mono text-[12px] tracking-[0.12em] uppercase text-fg-subtle">{c.title}</h4>
              <ul className="mt-4 space-y-2.5">
                {c.links.map((l) => (
                  <li key={l.label}>
                    <a href={l.href} className="text-[14px] text-fg-muted hover:text-fg transition-colors">
                      {l.label}
                    </a>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
        <div className="mt-14 flex flex-col gap-3 border-t border-line pt-6 md:flex-row md:items-center md:justify-between">
          <p className="text-[13px] text-fg-subtle">
            Shares can go down as well as up. Nothing on this page is financial advice.
          </p>
          <p className="font-mono text-[12px] text-fg-subtle">© {new Date().getFullYear()} Freedom</p>
        </div>
      </Container>
    </footer>
  );
}
