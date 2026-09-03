import type { Metadata, Viewport } from "next";
import { StatusLine } from "./mode-banner";
import { Nav } from "./nav";
import "./globals.css";

export const metadata: Metadata = {
  title: "ReLab",
  description:
    "ReLab breaks workflows on purpose and records whether they recover. Read-only view of runs, tasks, events and workers.",
};

// The browser chrome takes the page's own ground in each theme, so the address
// bar does not sit as a bright band above a dark page.
export const viewport: Viewport = {
  themeColor: [
    { media: "(prefers-color-scheme: light)", color: "#f5f2ea" },
    { media: "(prefers-color-scheme: dark)", color: "#131211" },
  ],
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en">
      <body>
        {/* Every page is a table of dense data behind a fixed masthead, so
            skipping past it is the difference between one tab press and
            fifteen. */}
        <a className="skip" href="#main">
          Skip to content
        </a>
        <div className="layout">
          <header className="masthead">
            <div>
              <h1>ReLab</h1>
              <p>Break it. Watch it recover.</p>
            </div>
            <Nav />
          </header>
          <main id="main">{children}</main>
        </div>
        <StatusLine />
      </body>
    </html>
  );
}
