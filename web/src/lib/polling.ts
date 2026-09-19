// One request generation at a time. Slow responses are allowed to finish;
// explicit refresh cancels them, and hidden pages consume no polling traffic.
export function startPolling(
  load: (signal: AbortSignal, manual: boolean) => Promise<unknown>,
  interval: number | null,
  visibility: Pick<Document, "hidden" | "addEventListener" | "removeEventListener"> = document,
  timeout = 15000,
) {
  let stopped = false;
  let running = false;
  let requested = false;
  let manual = false;
  let controller: AbortController | undefined;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const run = async () => {
    if (stopped || running || visibility.hidden) return;
    running = true;
    requested = false;
    const isManual = manual;
    manual = false;
    controller = new AbortController();
    const deadline = setTimeout(() => controller?.abort(new Error("请求超时，请重试")), timeout);
    try {
      await load(controller.signal, isManual);
    } catch {
      /* caller renders errors */
    } finally {
      clearTimeout(deadline);
      running = false;
      if (!stopped && !visibility.hidden) {
        if (requested) void run();
        else if (interval !== null) timer = setTimeout(run, interval);
      }
    }
  };
  const refresh = () => {
    clearTimeout(timer);
    requested = true;
    manual = true;
    if (running) controller?.abort();
    else void run();
  };
  const onVisibility = () => {
    clearTimeout(timer);
    if (visibility.hidden) controller?.abort();
    else if (running) requested = true;
    else void run();
  };
  visibility.addEventListener("visibilitychange", onVisibility);
  void run();
  return {
    refresh,
    stop() {
      stopped = true;
      clearTimeout(timer);
      controller?.abort();
      visibility.removeEventListener("visibilitychange", onVisibility);
    },
  };
}
