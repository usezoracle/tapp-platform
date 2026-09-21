import { Nav } from "@/components/Nav";
import { Hero } from "@/components/Hero";
import { LiveStrip } from "@/components/LiveStrip";
import { HowItWorks } from "@/components/HowItWorks";
import { Editions } from "@/components/Editions";
import { ForBusinesses } from "@/components/ForBusinesses";
import { Market } from "@/components/Market";
import { Trust } from "@/components/Trust";
import { Faq } from "@/components/Faq";
import { Footer } from "@/components/Footer";

export default function Page() {
  return (
    <div id="top">
      <Nav />
      <main>
        <Hero />
        <LiveStrip />
        <HowItWorks />
        <Editions />
        <ForBusinesses />
        <Market />
        <Trust />
        <Faq />
      </main>
      <Footer />
    </div>
  );
}
