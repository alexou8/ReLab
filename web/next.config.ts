import type { NextConfig } from "next";

const config: NextConfig = {
  // The dashboard reads from the ReLab API and renders on the server. There is
  // no client-side data fetching and no browser-side API credentials.
  //
  // That is also why there is no rewrite proxying /api to the control plane.
  // A rewrite would only matter if the browser were doing the fetching, and it
  // would come with real costs: the API would become publicly reachable through
  // the dashboard's origin, every request would take an extra hop, and CORS —
  // which does not currently apply to anything — would start to. The reasoning
  // is written out in docs/deployment.md so the next person does not have to
  // re-derive it.
  reactStrictMode: true,
  poweredByHeader: false,
  // Vercel builds these itself; the standalone output is what web/Dockerfile
  // and the compose stack use, and it makes the image roughly ten times
  // smaller than shipping node_modules.
  output: process.env.RELAB_STANDALONE === "1" ? "standalone" : undefined,
};

export default config;
