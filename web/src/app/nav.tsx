"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

const SECTIONS = [
  { href: "/", label: "Overview" },
  { href: "/runs", label: "Runs" },
  { href: "/workers", label: "Workers" },
  { href: "/glossary", label: "Glossary" },
];

const REPO = "https://github.com/alexou8/relab";

/**
 * The section tabs.
 *
 * It is the one client component in the dashboard, and it is one so that the
 * current section carries `aria-current`. A screen reader user moving through
 * the tabs otherwise has no way to know which page they are on.
 */
export function Nav() {
  const pathname = usePathname();
  return (
    <nav className="tabs" aria-label="Sections">
      {SECTIONS.map(({ href, label }) => {
        const active =
          href === "/" ? pathname === "/" : pathname.startsWith(href);
        return (
          <Link key={href} href={href} aria-current={active ? "page" : undefined}>
            {label}
          </Link>
        );
      })}
      {/* Outside the section list because it leaves the dashboard, and the
          current-page marking above would be a lie for it. */}
      <a href={REPO} className="nav-out">
        GitHub
      </a>
    </nav>
  );
}
