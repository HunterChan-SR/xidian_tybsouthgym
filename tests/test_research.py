import json
import tempfile
import unittest
from pathlib import Path
from booking_tools.build import digest, source_fingerprint
from booking_tools.research import analyze, prepare, review, split_possible

class ResearchTests(unittest.TestCase):
    def snapshot(self):
        return {"event":"inventory_observation", "at":"2026-10-19T12:00:01+08:00",
                "begin":"19:00", "end":"21:00", "maxTotalCents":2400, "fullWindowCandidates":0,
                "availableSlots":[{"field":"YMQ004", "begin":"19:00", "end":"20:00", "priceCents":1200},
                                  {"field":"YMQ007", "begin":"20:00", "end":"21:00", "priceCents":1200}]}

    def test_probe_is_not_competition_evidence(self):
        events=[]
        for mode in ("probe","book","test-book"):
            events += [{"event":"run_start","mode":mode}, self.snapshot(),
                       {"event":"run_end","status":"no_matching_slots"}]
        result=analyze(events)
        self.assertEqual(result["competitionRunsWithInventory"],1)
        self.assertEqual(result["runs"][1]["splitOnlyObservations"],1)
        self.assertFalse(result["runs"][0]["eligibleForInventoryComparison"])
        self.assertFalse(result["runs"][2]["eligibleForInventoryComparison"])

    def test_split_requires_contiguity_and_combined_budget(self):
        observation=self.snapshot()
        self.assertTrue(split_possible(observation))
        observation["availableSlots"][1]["priceCents"]=1201
        self.assertFalse(split_possible(observation))
        observation["availableSlots"][1]["priceCents"]=1200
        observation["availableSlots"][1]["begin"]="20:30"
        self.assertFalse(split_possible(observation))

    def test_interrupted_submission_remains_unresolved(self):
        result=analyze([{"event":"old"},{"event":"run_start","mode":"book"},
                        {"event":"submit","account":"one"}])
        self.assertEqual(result["unattributedEvents"],1)
        self.assertTrue(result["runs"][0]["unresolvedSubmission"])
        self.assertFalse(result["runs"][0]["eligibleForInventoryComparison"])

    def test_protocol_abort_keeps_observations_but_excludes_baseline(self):
        result=analyze([{"event":"run_start","mode":"book"},self.snapshot(),
                        {"event":"run_end","status":"schema_changed"}])
        self.assertEqual(result["runs"][0]["inventoryObservations"],1)
        self.assertFalse(result["runs"][0]["eligibleForInventoryComparison"])
        self.assertEqual(result["runs"][0]["baselineExclusionReason"],"program_or_protocol_abort")
        self.assertEqual(result["competitionRunsWithInventory"],0)

    def test_read_only_degradation_keeps_evidence_but_not_baseline(self):
        for status in ("observation_only_window_closed", "no_usable_accounts", "cancelled"):
            result=analyze([{"event":"run_start","mode":"book"},
                            {"event":"observation_only","reason":"schema_changed"},
                            {"event":"inventory_failure","reason":"schema_changed"},self.snapshot(),
                            {"event":"run_end","status":status}])
            run=result["runs"][0]
            self.assertEqual(run["inventoryObservations"],1)
            self.assertEqual(run["observationOnlyReason"],"schema_changed")
            self.assertEqual(run["readFailures"],{"schema_changed":1})
            self.assertFalse(run["eligibleForInventoryComparison"])
            self.assertEqual(result["competitionRunsWithInventory"],0)

    def test_missing_logs_are_not_zero_failure_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            result=review(Path(directory),"2026-10-19")
            self.assertIn("missing_event_file",result["issues"])
            self.assertIn("no_comparable_competition_inventory",result["issues"])
            self.assertEqual(result["runs"],[])

    def test_plan_change_requires_preserved_revision(self):
        with tempfile.TemporaryDirectory() as directory:
            root=Path(directory)
            (root/"research").mkdir();(root/"runtime").mkdir()
            config={"ReleaseTime":"12:00:00","DateAdd":2,"VenueNo":"01","FieldTypeNo":"001",
                    "BeginTime":"19:00","EndTime":"21:00","MaxTotalCents":2400,"PreferredFields":["YMQ004"],
                    "AllowOtherFields":True,"WindowSeconds":90,"PollSeconds":2,"RequestTimeoutSeconds":5,"Accounts":[]}
            (root/"booking.json").write_text(json.dumps(config))
            (root/"main.go").write_bytes(b"source");(root/"booking.exe").write_bytes(b"binary")
            fingerprint, files = source_fingerprint(root)
            (root/"runtime/build-manifest.json").write_text(json.dumps({"schemaVersion":3,"sourceSHA256":fingerprint,"sourceFiles":files,"binarySHA256":digest(root/"booking.exe")}))
            (root/"runtime/local-validation.json").write_text(json.dumps({"passed":True,"sourceSHA256":fingerprint}))
            (root/"research/plan.json").write_text(json.dumps({"activeStrategy":{"id":"S0"},"activeExperiment":{"id":"E0"}}))
            first=prepare(root,"2099-01-01")
            self.assertEqual(first,prepare(root,"2099-01-01"))
            config["PollSeconds"]=3;(root/"booking.json").write_text(json.dumps(config))
            with self.assertRaises(ValueError):prepare(root,"2099-01-01")
            second=prepare(root,"2099-01-01",revise=True)
            self.assertEqual(second["revision"],2)
            self.assertEqual(json.loads((root/"runtime/research/2099-01-01/plan-revision-1.json").read_text(encoding="utf-8")),first)
            (root/"runtime/local-validation.json").write_text(json.dumps({"passed":False,"sourceSHA256":fingerprint}))
            with self.assertRaises(ValueError): prepare(root,"2099-01-02")

if __name__=="__main__":unittest.main()
