import type { NextConfig } from "next";

const config: NextConfig = {
  // The dashboard reads from the ReLab API and renders on the server. There is
  // no client-side data fetching and no browser-side API credentials, because
  // there is nothing here a viewer could do that the API does not already
  // allow anyone who can reach it.
  reactStrictMode: true,
  poweredByHeader: false,
};

export default config;
