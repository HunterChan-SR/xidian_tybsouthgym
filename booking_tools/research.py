"""Offline research planning and evidence review. Never connects to the booking service."""
import json
from collections import Counter, defaultdict
from datetime import date, datetime, timedelta, timezone
from email.utils import parsedate_to_datetime
from booking_tools.build import verified_build_id, digest
from booking_tools.session import write_json

CN = timezone(timedelta(hours=8))
MUTATIONS = {"/Field/OrderField", "/Field/OrderFieldFree"}
ABORTED_BASELINE_STATUSES = {"schema_changed", "date_mapping_changed", "price_missing_or_invalid",
    "duplicate_slot", "unexpected_response", "local_error", "clock_skew_too_large", "cancelled", "observation_only_window_closed"}

def read_json(path):
    return json.loads(path.read_text(encoding="utf-8"))

def prepare(root, day, revise=False):
    config = read_json(root / "booking.json")
    research = read_json(root / "research/plan.json")
    build = read_json(root / "runtime/build-manifest.json")
    build_id = verified_build_id(root, build)
    validation = read_json(root / "runtime/local-validation.json")
    if not validation.get("passed") or validation.get("sourceSHA256") != build_id:
        raise ValueError("当前源码尚未通过离线检查，请运行 python main.py check")
    now = datetime.now(CN)
    release = datetime.fromisoformat(day + "T" + config["ReleaseTime"]).replace(tzinfo=CN)
    goal_date = (date.fromisoformat(day) + timedelta(days=config["DateAdd"])).isoformat()
    fingerprint = {"configFileSHA256": digest(root / "booking.json"), "buildID": build_id,
                   "binarySHA256": build["binarySHA256"], "researchPlanSHA256": digest(root / "research/plan.json")}
    path = root / "runtime/research" / day / "plan.json"
    previous = read_json(path) if path.exists() else None
    if previous and previous["fingerprint"] == fingerprint:
        return previous
    if previous and not revise:
        raise ValueError("当日计划存在但版本或配置已变化；核对原因后使用 --revise 保存新修订，旧计划保留")
    revision = previous.get("revision", 1) + 1 if previous else 1
    if previous:
        write_json(path.with_name("plan-revision-%d.json" % previous.get("revision", 1)), previous)
    result = {"day": day, "preparedAt": now.isoformat(), "preparedBeforeRelease": now < release,
              "revision": revision, "targetDate": goal_date, "fingerprint": fingerprint,
              "strategy": research["activeStrategy"], "experiment": research["activeExperiment"],
              "goal": {key: config[key] for key in ["VenueNo", "FieldTypeNo", "BeginTime", "EndTime", "MaxTotalCents", "PreferredFields", "AllowOtherFields", "WindowSeconds", "PollSeconds", "RequestTimeoutSeconds"]},
              "eveningFlexible": config.get("EveningFlexible", False),
              "allDayFallback": config.get("AllDayFallback", False),
              "configuredAliases": [a["Name"] for a in config.get("Accounts", [])],
              "decisionRule": "优先获得目标预约；一次只调整一个实验变量；观测不自动证明假设；偏离计划须记录原因。"}
    write_json(path, result)
    return result

def split_possible(event):
    rows = event.get("availableSlots", [])
    cap = event.get("maxTotalCents", 0)
    for first in rows:
        for second in rows:
            if (first["begin"] == event.get("begin") and first["end"] == second["begin"]
                    and second["end"] == event.get("end")
                    and first["priceCents"] + second["priceCents"] <= cap):
                return True
    return False

def analyze(events):
    groups = []
    current = None
    unattributed = 0
    for event in events:
        if event.get("event") == "run_start":
            current = {"start": event, "events": [], "end": None}
            groups.append(current)
        if current is None:
            unattributed += 1
            continue
        current["events"].append(event)
        if event.get("event") == "run_end":
            current["end"] = event
            current = None
    summaries = []
    for group in groups:
        rows = group["events"]
        snapshots = [e for e in rows if e.get("event") == "inventory_observation"]
        submissions = [e for e in rows if e.get("event") == "submit"]
        requests = [e for e in rows if e.get("path")]
        end = group["end"] or {}
        start = group["start"]
        mode = start.get("mode", "unknown")
        same = sum(e.get("fullWindowCandidates", 0) > 0 for e in snapshots)
        split = sum(split_possible(e) for e in snapshots)
        split_only = sum(split_possible(e) and e.get("fullWindowCandidates", 0) == 0 for e in snapshots)
        degraded = [e for e in rows if e.get("event") == "observation_only"]
        protocol_stop = bool(degraded) or bool(end.get("observationOnlyReason"))
        summaries.append({"startedAt": start.get("at"), "mode": mode, "buildID": start.get("buildID"),
            "configSHA256": start.get("configSHA256"), "configFileSHA256": start.get("configFileSHA256"), "completed": group["end"] is not None,
            "status": end.get("status", "end_not_recorded"), "requests": len(requests),
            "accountsObserved": sorted({e["account"] for e in requests if e.get("account")}),
            "submissions": len(submissions), "firstSubmitAt": submissions[0].get("at") if submissions else None,
            "errors": dict(Counter(e["error"] for e in requests if e.get("error"))),
            "businessMessages": dict(Counter(e["message"] for e in requests if e.get("message"))),
            "http429": sum(e.get("httpStatus") == 429 for e in requests),
            "observationOnly": protocol_stop,
            "observationOnlyReason": end.get("observationOnlyReason") or (degraded[0].get("reason") if degraded else None),
            "readFailures": dict(Counter(e["reason"] for e in rows if e.get("event") == "inventory_failure" and e.get("reason"))),
            "inventoryStateCounts": dict(sum((Counter(e.get("stateCounts", {})) for e in snapshots), Counter())),
            "inventoryObservations": len(snapshots), "sameCourtPossibleObservations": same,
            "splitPossibleObservations": split, "splitOnlyObservations": split_only,
            "inventoryFirstObservedAt": snapshots[0].get("at") if snapshots else None,
            "inventoryLastObservedAt": snapshots[-1].get("at") if snapshots else None,
            "eligibleForInventoryComparison": mode == "book" and bool(snapshots) and group["end"] is not None and end.get("status") not in ABORTED_BASELINE_STATUSES and not protocol_stop,
            "baselineExclusionReason": "program_or_protocol_abort" if mode == "book" and (protocol_stop or end.get("status") in ABORTED_BASELINE_STATUSES) else None,
            "unresolvedSubmission": bool(submissions) and (not group["end"] or end.get("status") in {"order_uncertain", "order_verification_required", "order_details_mismatch", "network_error"})})
    return {"runs": summaries, "unattributedEvents": unattributed,
            "competitionRunsWithInventory": sum(r["eligibleForInventoryComparison"] for r in summaries),
            "caution": "库存可行性是单次单账号查询时的条件判断，不证明提交时仍可获得；probe、测试单和缺少结束记录的运行不纳入库存策略对照。"}

def review(root, day):
    summarize(root, day)
    event_path = root / "runtime/events" / (day + ".jsonl")
    events, malformed = [], 0
    if event_path.exists():
        for line in event_path.read_text(encoding="utf-8").splitlines():
            try:
                value = json.loads(line)
                if not isinstance(value, dict):
                    raise ValueError("not an event object")
                events.append(value)
            except (ValueError, TypeError):
                malformed += 1
    result = analyze(events)
    folder = root / "runtime/research" / day
    plan_path = folder / "plan.json"
    plan = read_json(plan_path) if plan_path.exists() else None
    issues = []
    if not event_path.exists():
        issues.append("missing_event_file")
    if malformed:
        issues.append("malformed_or_partial_events")
    if not plan:
        issues.append("no_preregistered_plan")
    elif not plan["preparedBeforeRelease"]:
        issues.append("plan_created_after_release")
    if plan and any(r.get("buildID") != plan["fingerprint"]["buildID"] for r in result["runs"] if r["mode"] == "book"):
        issues.append("booking_build_differs_from_plan")
    if plan and any(r.get("configFileSHA256") != plan["fingerprint"]["configFileSHA256"] for r in result["runs"] if r["mode"] == "book"):
        issues.append("booking_config_differs_from_plan_or_hash_missing")
    if not result["competitionRunsWithInventory"]:
        issues.append("no_comparable_competition_inventory")
    if any(r["unresolvedSubmission"] for r in result["runs"]):
        issues.append("reconcile_orders_before_any_retry")
    recommendations = []
    if any(r.get("baselineExclusionReason") for r in result["runs"]):
        issues.append("competition_interrupted_by_program_or_protocol_error")
    if "reconcile_orders_before_any_retry" in issues:
        recommendations.append("先核对结果不明的订单，保留保护，不因查询一次未找到就重发。")
    if any(r["splitOnlyObservations"] for r in result["runs"] if r["eligibleForInventoryComparison"]):
        recommendations.append("观察到同场不可行而分场在快照中可行；更新H6证据，不能据此声称分场提交一定成功。")
    if any(r["http429"] for r in result["runs"]):
        recommendations.append("出现明确HTTP限流，遵守等待要求；下一轮不提高总请求速率。")
    if "no_comparable_competition_inventory" in issues:
        recommendations.append("缺少可比放票数据，维持基线；不把只读检查或测试单计作抢票成功/失败样本。")
    result.update({"day": day, "reviewedAt": datetime.now(CN).isoformat(), "malformedEvents": malformed,
                   "issues": issues, "recommendations": recommendations,
                   "hypothesesAutomaticallyChanged": False})
    write_json(folder / "review.json", result)
    decision_path = folder / "decision.json"
    if not decision_path.exists():
        write_json(decision_path, {"status": "needs_evidence_review", "action": "keep_baseline_pending_review",
            "evidence": ["review.json"], "supportedHypotheses": [], "contradictions": [], "confounders": [],
            "nextSingleChange": None, "rollbackCondition": None,
            "instruction": "复盘后填写事实、解释与下一步；未经审阅的建议不会自动修改预约配置。"})
    return result


def summarize(root, day):
    events = []
    path = root / "runtime" / "events" / (day + ".jsonl")
    malformed = 0
    lines = path.read_text(encoding="utf-8").splitlines() if path.exists() else []
    for line in lines:
        try:
            event = json.loads(line)
            if not isinstance(event, dict):
                malformed += 1
                continue
            events.append(event)
        except json.JSONDecodeError:
            malformed += 1  # A currently running writer may leave a partial final line.
    groups = defaultdict(list)
    messages = Counter()
    statuses = Counter()
    offsets = []
    submits = []
    runs = []
    for event in events:
        if event.get("event") == "submit":
            submits.append(event)
            continue
        if event.get("event") == "run_start":
            runs.append(event)
        if not event.get("path"):
            continue
        groups[event["path"]].append(event.get("latencyMs", 0))
        statuses[str(event.get("httpStatus", 0))] += 1
        if event.get("message"):
            messages[event["message"]] += 1
        if event.get("serverDate"):
            try:
                server = parsedate_to_datetime(event["serverDate"])
                local = datetime.fromisoformat(event["at"])
                offsets.append(round((server - local).total_seconds() - event["latencyMs"] / 2000, 3))
            except (ValueError, TypeError, KeyError):
                pass
    endpoints = {}
    for name, values in groups.items():
        values.sort()
        endpoints[name] = {"requests": len(values), "medianMs": values[len(values)//2],
                           "p95Ms": values[min(len(values)-1, int(len(values)*0.95))]}
    result = {"day": day, "eventFilePresent": path.exists(), "malformedEvents": malformed, "requests": sum(statuses.values()), "httpStatus": dict(statuses),
              "endpoints": endpoints, "businessMessages": dict(messages), "submissions": submits, "runs": runs,
              "approxServerClockOffsetSeconds": {"min": min(offsets), "max": max(offsets)} if offsets else None,
              "clockCaution": "HTTP Date has one-second precision; do not infer account or IP limits from one run."}
    target = root / "runtime" / ("report-" + day + ".json")
    write_json(target, result)
    return result

