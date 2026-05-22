import { useEffect, useRef, useState } from "react";
import { fetchJSON, type QueryParams } from "./api";

// Window-level CustomEvent emitted by the TopBar's Refresh button.
// Every useApi instance listens and bumps its tick — gives the
// operator a single "reload everything" affordance without
// per-hook plumbing.
const REFRESH_EVENT = "dashboard-refresh";

export type ApiState<T> = {
  data: T | null;
  loading: boolean;
  error: Error | null;
  reload: () => void;
};

// useApi fetches `path` with `params` on mount and whenever `deps`
// changes. Pages typically pass [win, tool, project] from useFilters
// so a filter change re-fires every query.
//
// Aborts in-flight requests on unmount + on rapid filter changes so
// stale responses can't clobber fresh ones. No caching — the dashboard
// is point-in-time enough that re-fetching on filter change is the
// right call.
export function useApi<T>(
  path: string | null,
  params?: QueryParams,
  deps: unknown[] = [],
): ApiState<T> {
  const [data, setData] = useState<T | null>(null);
  const [loading, setLoading] = useState(path != null);
  const [error, setError] = useState<Error | null>(null);
  const [tick, setTick] = useState(0);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => {
    function onRefresh() {
      setTick((t) => t + 1);
    }
    window.addEventListener(REFRESH_EVENT, onRefresh);
    return () => window.removeEventListener(REFRESH_EVENT, onRefresh);
  }, []);

  useEffect(() => {
    if (!path) {
      setLoading(false);
      return;
    }
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;
    setLoading(true);
    setError(null);
    fetchJSON<T>(path, params, { signal: ac.signal })
      .then((v) => {
        if (!ac.signal.aborted) setData(v);
      })
      .catch((err: unknown) => {
        if (ac.signal.aborted) return;
        const e = err instanceof Error ? err : new Error(String(err));
        if (e.name === "AbortError") return;
        setError(e);
      })
      .finally(() => {
        if (!ac.signal.aborted) setLoading(false);
      });
    return () => ac.abort();
    // path is in the dep list so callers can omit it from deps when
    // it's a literal string. params is serialized by callers into deps
    // (e.g. [win, tool] over passing the whole params object) because
    // a stable identity isn't guaranteed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, tick, ...deps]);

  return { data, loading, error, reload: () => setTick((t) => t + 1) };
}
