import type { Metadata } from "next";
import { ModeBanner } from "./mode-banner";
import { Nav } from "./nav";
import "./globals.css";

export const metadata: Metadata = {
  title: "ReLab",
  description:
    "Read-only view of ReLab runs, tasks, events and workers. A debugging surface, not the product.",
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
              <p>
                Runs, tasks, workers and recovery, reconstructed from the event
                journal. Read-only.
              </p>
            </div>
            <Nav />
          </header>
          <ModeBanner />
          <main id="main">{children}</main>
        </div>
      </body>
    </html>
  );
}
