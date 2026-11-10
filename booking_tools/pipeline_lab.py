"""Offline event model for account scheduling; no network, credentials or orders.

The server's commit time and reply time are distinct. A client timeout cannot
undo an earlier commit. The single inventory frame only informs unsent work.
Timing inputs are scenarios, not inferred platform thresholds or success rates.
"""
from dataclasses import dataclass
import argparse
import heapq
import json


@dataclass(frozen=True)
class Scenario:
    name: str
    # Complete, same-court two-hour candidates, represented by synthetic names.
    inventory: tuple = ("court-2", "court-3")
    inventory_ms: int = 480
    response_ms: tuple = (900, 450, 300)
    commit_ms: tuple = (100, 100, 100)
    defaults: tuple = ("court-1", "court-2", "court-3")
    # Competitors occupy a whole candidate at this simulated time.
    taken_at: tuple = ()
    lost_replies: tuple = ()
    cancel_confirmed: bool = True


def simulate(scenario, policy, *, stagger_ms=150, max_inflight=2):
    """Return client-visible events plus separate mock-server outcome truth.

    serial: each fallback waits for the previous reply, poll interval and query.
    prepared_serial: preparation overlaps; order submissions still wait for reply.
    hedge: at most two in flight, each account submits at most once; no retries.
    First success/unknown closes dispatch, but already sent work is still drained.
    """
    if policy not in {"serial", "prepared_serial", "hedge"}:
        raise ValueError("unknown policy")
    if max_inflight not in {1, 2, 3} or stagger_ms < 1:
        raise ValueError("invalid bounded schedule")
    if any(commit > reply for commit, reply in zip(scenario.commit_ms, scenario.response_ms)):
        raise ValueError("commit must precede response in this model")
    # Nine ms calendar gate and ~78 ms current local/challenge handoff.
    # TLS is assumed ready for every policy, so this compares scheduling only.
    calendar_ms, handoff_ms, poll_ms = 9, 78, 2000
    queue, events = [], []
    def enqueue(at, kind, account=-1):
        priority = {"commit": 0, "response": 1, "cancel": 2, "dispatch": 3}[kind]
        heapq.heappush(queue, (at, priority, kind, account))

    inflight, attempted, actual, chosen, response_at = set(), set(), {}, {}, {}
    held, known, pending_cancel = set(), set(), set()
    used_fields, reasons = set(), []
    winner, first_success, stop, peak, peak_inflight = None, None, False, 0, 0
    taken = dict(scenario.taken_at)
    start = calendar_ms + handoff_ms
    enqueue(start + (scenario.inventory_ms if policy == "serial" else 0), "dispatch", 0)
    if policy == "hedge":
        for account in (1, 2):
            enqueue(start + account * stagger_ms, "dispatch", account)

    while queue:
        now, _, kind, account = heapq.heappop(queue)
        if kind == "dispatch":
            if stop or account in attempted:
                continue
            if len(inflight) >= max_inflight:
                enqueue(min(response_at[a] for a in inflight), "dispatch", account)
                continue
            # A snapshot is a hint; dispatch never assumes it reserves a court.
            source = "prediction"
            field = scenario.defaults[account]
            if now >= calendar_ms + scenario.inventory_ms:
                available = [f for f in scenario.inventory if f not in used_fields]
                if available:
                    field, source = available[0], "first_snapshot"
                elif policy == "serial":
                    events.append({"ms": now, "event": "no_candidate", "account": account})
                    break
            chosen[account] = field
            attempted.add(account)
            inflight.add(account)
            used_fields.add(field)
            peak_inflight = max(peak_inflight, len(inflight))
            response_at[account] = now + scenario.response_ms[account]
            enqueue(now + scenario.commit_ms[account], "commit", account)
            enqueue(response_at[account], "response", account)
            events.append({"ms": now, "event": "dispatch", "account": account,
                           "field": field, "source": source})
        elif kind == "commit":
            field = chosen[account]
            occupied = now >= taken.get(field, float("inf"))
            occupied |= any(chosen[a] == field for a in held)
            actual[account] = not occupied
            if actual[account]:
                held.add(account)
                peak = max(peak, len(held))
            # This is mock-server truth, never used as an early client signal.
        elif kind == "response":
            inflight.remove(account)
            result = "unknown" if account in scenario.lost_replies else (
                "success" if actual[account] else "slot_taken")
            events.append({"ms": now, "event": "response", "account": account, "result": result})
            if result == "unknown":
                stop = True
                reasons.append("order_unknown")
            elif result == "success":
                stop = True
                known.add(account)
                if winner is None:
                    winner, first_success = account, now
                else:
                    pending_cancel.add(account)
                    enqueue(now + 100, "cancel", account)
            elif policy != "hedge" and not stop and account < 2:
                extra_query = scenario.inventory_ms if policy == "serial" else 0
                enqueue(now + poll_ms + handoff_ms + extra_query, "dispatch", account + 1)
        else:
            if scenario.cancel_confirmed:
                held.remove(account)
                pending_cancel.remove(account)
            else:
                reasons.append("cancel_unknown")
            events.append({"ms": now, "event": "cancel_result", "account": account,
                           "confirmed": scenario.cancel_confirmed})

    return {
        "scenario": scenario.name, "policy": policy,
        "first_success_ms": first_success, "attempts": len(attempted),
        "peak_inflight": peak_inflight, "peak_held": peak,
        "remaining_held_mock_truth": len(held),
        "needs_reconciliation": bool(reasons), "reasons": reasons,
        "events": events,
    }


def examples():
    return [
        Scenario("prediction_hit"),
        Scenario("prediction_miss", taken_at=(("court-1", 0),)),
        Scenario("inventory_stale", taken_at=(("court-1", 0), ("court-2", 500))),
        Scenario("late_success_after_winner"),
        Scenario("first_response_lost", lost_replies=(0,)),
        Scenario("cancel_not_confirmed", cancel_confirmed=False),
        Scenario("correlated_same_target", defaults=("court-1",) * 3),
    ]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--compact", action="store_true")
    args = parser.parse_args()
    rows = [simulate(s, p) for s in examples() for p in ("serial", "prepared_serial", "hedge")]
    if args.compact:
        rows = [{k: v for k, v in row.items() if k != "events"} for row in rows]
    print(json.dumps({"offline_model_only": True, "rows": rows}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
