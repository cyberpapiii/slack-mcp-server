#!/usr/bin/env bash
# Report field latency for the Plug-managed slack server. Read-only.
#
# Mines Plug's daily logs (~/Library/Logs/plug/plug.log.YYYY-MM-DD) for the
# per-call `duration_ms` Plug records on every proxied slack tool call, and for
# the gap between "spawning server process" and "connected" on each start.
# Plug discards the server's stderr, so these proxy lines are the only field
# timing there is.
#
# usage: scripts/perf.sh [since] [until]
#   since, until  YYYY-MM-DD bounds on the log file date, inclusive
#                 (default: every log file present)
#
# Output: per-tool n/p50/p75/p95/max in ms, busiest first; cold-start
# percentiles; failed calls. Compare runs from before and after a deploy by
# passing the deploy date as `since` or `until`.
set -euo pipefail

logdir="${PLUG_LOG_DIR:-$HOME/Library/Logs/plug}"
since="${1:-0000-00-00}"
until="${2:-9999-99-99}"

files=()
for f in "$logdir"/plug.log.*; do
  [ -e "$f" ] || continue
  d="${f##*plug.log.}"
  if [[ ! "$d" < "$since" && ! "$d" > "$until" ]]; then
    files+=("$f")
  fi
done
if [ ${#files[@]} -eq 0 ]; then
  echo "no Plug logs in $logdir for $since..$until" >&2
  exit 1
fi

grep -h '"server":"slack"' "${files[@]}" | python3 -c '
import collections, datetime, json, sys

def ts(d):
    return datetime.datetime.fromisoformat(d["timestamp"].replace("Z", "+00:00"))

def q(a, p):
    return a[min(len(a) - 1, int(p * len(a)))]

calls = collections.defaultdict(list)
starts, failures = [], []
spawned = None
for line in sys.stdin:
    try:
        d = json.loads(line)
    except ValueError:
        continue
    f = d.get("fields", {})
    msg = f.get("message", "")
    if msg == "proxy tool call completed":
        calls[f["tool"]].append(f["duration_ms"])
    elif msg == "upstream tool call failed":
        failures.append("%s %s %s" % (d["timestamp"][:19], f.get("tool"), f.get("error")))
    elif msg == "spawning server process":
        spawned = ts(d)
    elif msg.startswith("connected to") and spawned is not None:
        starts.append((ts(d) - spawned).total_seconds() * 1000)
        spawned = None

print("%-34s %6s %6s %6s %6s %7s" % ("tool", "n", "p50", "p75", "p95", "max"))
for tool, ms in sorted(calls.items(), key=lambda kv: -sum(kv[1])):
    ms.sort()
    print("%-34s %6d %6d %6d %6d %7d" % (tool, len(ms), q(ms, .5), q(ms, .75), q(ms, .95), ms[-1]))
if starts:
    starts.sort()
    print("\ncold start (spawn to connected) n=%d p50=%d p75=%d max=%d ms"
          % (len(starts), q(starts, .5), q(starts, .75), starts[-1]))
print("\nfailed calls: %d" % len(failures))
for f in failures[-10:]:
    print("  " + f)
'
