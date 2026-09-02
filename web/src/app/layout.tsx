import type { Metadata } from "next";
import Link from "next/link";
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
        <div className="layout">
          <header className="masthead">
            <div>
              <h1>ReLab</h1>
              <p>
                Read-only view of runs, tasks and recovery. Everything here is
                reconstructed from the event journal.
              </p>
            </div>
            <nav className="tabs" aria-label="Sections">
              <Link href="/">Overview</Link>
              <Link href="/runs">Runs</Link>
              <Link href="/workers">Workers</Link>
            </nav>
          </header>
          <main>{children}</main>
        </div>
      </body>
    </html>
  );
}
