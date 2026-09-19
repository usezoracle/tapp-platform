import type { NextConfig } from "next";
import path from "node:path";

const nextConfig: NextConfig = {
  // Same reason as apps/pwa: pnpm keeps the real packages in the workspace
  // root's node_modules/.pnpm and leaves symlinks here, and the directory
  // above the repo carries its own lockfiles. Rooting Turbopack anywhere but
  // the monorepo root either picks the wrong workspace or refuses to compile
  // files "outside the project directory".
  turbopack: {
    root: path.resolve(__dirname, "..", ".."),
  },
};

export default nextConfig;
