import unittest
from booking_tools.pipeline_lab import Scenario, simulate


class PipelineLabTests(unittest.TestCase):
    def test_hedge_acts_before_previous_response_and_drains_late_success(self):
        result = simulate(Scenario("late"), "hedge")
        dispatches = [e for e in result["events"] if e["event"] == "dispatch"]
        self.assertEqual([e["account"] for e in dispatches], [0, 1])
        self.assertEqual(result["peak_inflight"], 2)
        self.assertEqual(result["peak_held"], 2)
        self.assertEqual(result["remaining_held_mock_truth"], 1)
        self.assertEqual(result["first_success_ms"], 687)
        self.assertTrue(any(e["event"] == "cancel_result" for e in result["events"]))

    def test_unknown_response_does_not_mean_no_order(self):
        result = simulate(Scenario("lost", lost_replies=(1,)), "hedge")
        self.assertTrue(result["needs_reconciliation"])
        self.assertIn("order_unknown", result["reasons"])
        self.assertEqual(result["attempts"], 2)
        self.assertEqual(result["remaining_held_mock_truth"], 2)

    def test_unconfirmed_cancel_does_not_release_holding(self):
        result = simulate(Scenario("cancel", cancel_confirmed=False), "hedge")
        self.assertEqual(result["remaining_held_mock_truth"], 2)
        self.assertIn("cancel_unknown", result["reasons"])

    def test_first_snapshot_can_already_be_stale_at_commit(self):
        scenario = Scenario("stale", taken_at=(("court-1", 0), ("court-2", 500)))
        result = simulate(scenario, "serial")
        response = next(e for e in result["events"] if e["event"] == "response")
        self.assertEqual(response["result"], "slot_taken")

    def test_preparation_overlap_keeps_single_submission(self):
        result = simulate(Scenario("miss", taken_at=(("court-1", 0),)), "prepared_serial")
        self.assertEqual(result["peak_inflight"], 1)
        self.assertEqual(result["peak_held"], 1)
        self.assertEqual(result["attempts"], 2)

    def test_three_simultaneous_requires_three_holding_capacity(self):
        result = simulate(Scenario("triple"), "hedge", max_inflight=3)
        self.assertEqual(result["peak_held"], 3)
        self.assertEqual(result["remaining_held_mock_truth"], 1)

    def test_server_truth_cannot_trigger_client_dispatch_or_stop(self):
        scenario = Scenario("pending", commit_ms=(1, 1, 1))
        result = simulate(scenario, "hedge")
        # A has committed before B sends; the client still has no response.
        self.assertEqual(result["attempts"], 2)

    def test_shared_target_is_correlated(self):
        result = simulate(Scenario("same", inventory=("court-1",), defaults=("court-1",) * 3), "hedge")
        self.assertEqual(result["peak_held"], 1)
        self.assertEqual(result["attempts"], 3)

    def test_observer_updates_only_unsent_account(self):
        result = simulate(Scenario("new-hint", defaults=("court-1",) * 3), "hedge")
        dispatches = [e for e in result["events"] if e["event"] == "dispatch"]
        self.assertEqual(dispatches[0]["source"], "prediction")
        self.assertEqual(dispatches[1]["source"], "prediction")
        self.assertEqual(dispatches[2]["source"], "first_snapshot")
        self.assertEqual(dispatches[2]["field"], "court-2")
