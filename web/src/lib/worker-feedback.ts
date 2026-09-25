import type { Activity } from "./types";

// A Worker delivery belongs in the conversation but never changes the main
// Agent's running/settled state, including when another user turn is active.
export function lastMainActivity(items: Activity[]): Activity | undefined {
  return items.findLast((item) => !item.metadata?.worker_feedback);
}
