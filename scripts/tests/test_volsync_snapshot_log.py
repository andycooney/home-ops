import json
import subprocess
import tempfile
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]
FORMATTER = ROOT / "scripts" / "volsync-snapshot-log.py"
OBSERVED_WARNING = """Found too many index blobs (1204), this may result in degraded performance.

Please ensure periodic repository maintenance is enabled or run 'kopia maintenance'."""


def snapshot(start_time, root, size, retention):
    return {
        "id": f"manifest-{root}",
        "startTime": start_time,
        "stats": {"totalSize": size},
        "rootEntry": {"obj": root},
        "retentionReason": retention,
    }


def log_text(payload, diagnostics="", status=0):
    return f"""SNAPSHOT_COMMAND_STATUS={status}
SNAPSHOT_DIAGNOSTICS_BEGIN
{diagnostics}
SNAPSHOT_DIAGNOSTICS_END
SNAPSHOT_JSON_BEGIN
{payload}
SNAPSHOT_JSON_END
"""


class SnapshotLogTests(unittest.TestCase):
    def run_formatter(self, raw):
        with tempfile.NamedTemporaryFile("w", delete=False) as temporary:
            temporary.write(raw)
            path = temporary.name
        try:
            return subprocess.run(
                ["python3", str(FORMATTER), path, "America/New_York"],
                text=True,
                capture_output=True,
                check=False,
            )
        finally:
            Path(path).unlink()

    def test_clean_json_preserves_metadata_and_ordering(self):
        snapshots = [
            snapshot("2026-06-17T20:00:52Z", "root-old", 3961886, ["latest-2"]),
            snapshot("2026-06-18T01:00:57Z", "root-new", 4031766, ["latest-1", "daily-1"]),
        ]
        result = self.run_formatter(log_text(json.dumps(snapshots)))

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertLess(result.stdout.index("root-old"), result.stdout.index("root-new"))
        self.assertIn("2026-06-17 16:00:52 EDT", result.stdout)
        self.assertIn("3961886", result.stdout)
        self.assertIn("latest-1,daily-1", result.stdout)
        self.assertEqual(result.stderr, "")

    def test_warning_before_json_is_preserved(self):
        payload = f"warning before payload\n{json.dumps([])}"
        result = self.run_formatter(log_text(payload))

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("warning before payload", result.stderr)

    def test_warning_after_json_is_preserved(self):
        payload = f"{json.dumps([])}\nwarning after payload"
        result = self.run_formatter(log_text(payload))

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("warning after payload", result.stderr)

    def test_observed_index_blob_warning_is_preserved(self):
        result = self.run_formatter(log_text("[]", diagnostics=OBSERVED_WARNING))

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Found too many index blobs (1204)", result.stderr)
        self.assertIn("kopia maintenance", result.stderr)

    def test_malformed_json_fails_clearly(self):
        result = self.run_formatter(log_text('[{"startTime":'))

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("snapshot JSON is malformed or ambiguous", result.stderr)

    def test_absent_json_fails_clearly(self):
        result = self.run_formatter(log_text(""))

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("produced no JSON payload", result.stderr)

    def test_nonzero_kopia_command_fails_and_preserves_diagnostics(self):
        result = self.run_formatter(log_text("", diagnostics="repository unavailable", status=17))

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("repository unavailable", result.stderr)
        self.assertIn("failed with exit code 17", result.stderr)

    def test_multiple_json_values_fail_as_ambiguous(self):
        result = self.run_formatter(log_text("[]\n[]"))

        self.assertNotEqual(result.returncode, 0)
        self.assertIn("malformed or ambiguous", result.stderr)


if __name__ == "__main__":
    unittest.main()
