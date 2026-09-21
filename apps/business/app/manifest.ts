import type { MetadataRoute } from "next";

export default function manifest(): MetadataRoute.Manifest {
  return {
    name: "Freedom Exchange",
    short_name: "Freedom",
    description: "List your business on Freedom Exchange. Every card tap at your shop buys the customer a slice of it.",
    start_url: "/",
    display: "standalone",
    background_color: "#1c1c1e",
    theme_color: "#1c1c1e",
    icons: [
      { src: "/icon.png", sizes: "512x512", type: "image/png" },
      { src: "/icon.png", sizes: "512x512", type: "image/png", purpose: "maskable" },
      { src: "/apple-icon.png", sizes: "180x180", type: "image/png" },
    ],
  };
}
