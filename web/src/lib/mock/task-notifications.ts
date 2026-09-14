import type { NotificationCategory, NotificationQuery, NotificationSummary } from "../task-notifications";

export type MockNotificationRecord = { task: string; category: NotificationCategory; id: string; pending: boolean };
// Keep creation identity separate from changing status, just like the database.
export class MockNotificationLedger {
  private born = new Map<string, number>();
  private initialized = false;
  private clock = 0;
  summarize(queries: NotificationQuery[], mode: string, records: MockNotificationRecord[]): NotificationSummary {
    const now = Math.max(Date.now() * 1000, this.clock + 1);
    this.clock = now;
    for (const row of records) {
      const key = JSON.stringify([row.task, row.category, row.id]);
      if (!this.born.has(key)) this.born.set(key, this.initialized ? now : 0);
    }
    this.initialized = true;
    return {
      snapshot: String(now),
      observed_at: now,
      items: queries.map((query) => {
        const counts = { task_id: query.task_id, findings: 0, assets: 0, intercepts: 0 };
        for (const row of records) {
          if (row.task !== query.task_id || !row.pending || (mode === "findings" && row.category !== "findings"))
            continue;
          const cursor = query[row.category];
          const born = this.born.get(JSON.stringify([row.task, row.category, row.id])) ?? 0;
          if (cursor && born > Number(cursor)) counts[row.category]++;
        }
        return counts;
      }),
    };
  }
}
