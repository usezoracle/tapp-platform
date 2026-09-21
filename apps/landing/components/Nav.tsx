import { Button } from "./Button";
import { LogoWithMark } from "./Logo";
import { LINKS } from "@/lib/links";

const links = [
  { label: "Cards", href: "#cards" },
  { label: "For businesses", href: LINKS.business },
  { label: "Market", href: LINKS.market },
  { label: "Sign in", href: LINKS.signIn },
];

export function Nav() {
  return (
    <header className="sticky top-0 z-40 h-14 border-b border-line bg-ground">
      <div className="mx-auto flex h-full max-w-[1200px] items-center justify-between px-4 sm:px-6 md:px-8">
        <a href="#top" className="flex items-center text-fg" aria-label="Freedom, home">
          <LogoWithMark height={20} />
        </a>
        <nav className="hidden md:flex items-center gap-7" aria-label="Primary">
          {links.map((l) => (
            <a key={l.label} href={l.href} className="text-[14px] font-medium text-fg-muted hover:text-fg transition-colors">
              {l.label}
            </a>
          ))}
        </nav>
        <div className="flex items-center gap-4">
          <a href={LINKS.signIn} className="md:hidden text-[14px] font-medium text-fg-muted hover:text-fg">
            Sign in
          </a>
          <Button href={LINKS.getCard} className="h-9">
            Get the card
          </Button>
        </div>
      </div>
    </header>
  );
}
