import Link from "next/link";
import { mode } from "@/lib/api";

export default function NotFound() {
  return (
    <div className="notice" role="alert">
      <h3>No such run</h3>
      <p>
        {mode() === "demo"
          ? "That run id is not in the recording this deployment serves. The recording holds five runs."
          : "That run id is not in the database."}{" "}
        <Link href="/runs">See all runs</Link>.
      </p>
    </div>
  );
}
