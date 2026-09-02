import Link from "next/link";

export default function NotFound() {
  return (
    <div className="notice">
      <h3>No such run</h3>
      <p>
        That run id is not in the database. <Link href="/runs">See all runs</Link>.
      </p>
    </div>
  );
}
