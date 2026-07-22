#!/usr/bin/env python3

import json
import re
import sys
from datetime import datetime
from pathlib import Path
from zoneinfo import ZoneInfo


def fail(message):
    print(f"ERROR: {message}", file=sys.stderr)
    raise SystemExit(1)


def marked_section(raw, name):
    match = re.search(
        rf"(?:^|\n){name}_BEGIN\n(.*?)\n{name}_END(?:\n|$)",
        raw,
        re.DOTALL,
    )
    if not match:
        fail(f"Kopia output is missing {name}_BEGIN/{name}_END markers")
    return match.group(1)


def display_diagnostics(label, diagnostics):
    diagnostics = diagnostics.strip()
    if diagnostics:
        print(f"{label}:", file=sys.stderr)
        print(diagnostics, file=sys.stderr)


def safe_diagnostic_text(value):
    return not any(character in value for character in "[]{}")


def parse_snapshot_json(payload):
    payload = payload.strip()
    if not payload:
        fail("Kopia snapshot list produced no JSON payload")

    try:
        data = json.loads(payload)
    except json.JSONDecodeError as original_error:
        decoder = json.JSONDecoder()
        candidates = []

        for start, character in enumerate(payload):
            if character != "[":
                continue
            try:
                candidate, consumed = decoder.raw_decode(payload[start:])
            except json.JSONDecodeError:
                continue
            if not isinstance(candidate, list):
                continue

            prefix = payload[:start].strip()
            suffix = payload[start + consumed :].strip()
            if safe_diagnostic_text(prefix) and safe_diagnostic_text(suffix):
                candidates.append((candidate, prefix, suffix))

        if len(candidates) != 1:
            fail(f"Kopia snapshot JSON is malformed or ambiguous: {original_error}")

        data, prefix, suffix = candidates[0]
        display_diagnostics(
            "Kopia snapshot stdout diagnostics",
            "\n".join(part for part in (prefix, suffix) if part),
        )

    if not isinstance(data, list):
        fail("Kopia snapshot JSON must contain exactly one array")

    return data


def format_snapshots(raw, timezone_name):
    status_match = re.search(r"(?:^|\n)SNAPSHOT_COMMAND_STATUS=([0-9]+)(?:\n|$)", raw)
    if not status_match:
        fail("Kopia output is missing SNAPSHOT_COMMAND_STATUS")

    diagnostics = marked_section(raw, "SNAPSHOT_DIAGNOSTICS")
    display_diagnostics("Kopia diagnostics", diagnostics)

    status = int(status_match.group(1))
    if status != 0:
        fail(f"Kopia snapshot command failed with exit code {status}")

    data = parse_snapshot_json(marked_section(raw, "SNAPSHOT_JSON"))
    timezone = ZoneInfo(timezone_name)

    print()
    print(f"{'LOCAL_TIME':<24} {'SNAPSHOT_ID':<40} {'SIZE':<12} {'RETENTION'}")
    print("-" * 105)

    for item in data:
        utc = item.get("startTime", "")
        snapshot = ((item.get("rootEntry") or {}).get("obj")) or item.get("id") or ""
        size = ((item.get("stats") or {}).get("totalSize")) or ""
        retention = ",".join(item.get("retentionReason") or [])

        local = utc
        try:
            utc_parse = re.sub(r"\.[0-9]+Z$", "Z", utc)
            parsed = datetime.fromisoformat(utc_parse.replace("Z", "+00:00"))
            local = parsed.astimezone(timezone).strftime("%Y-%m-%d %H:%M:%S %Z")
        except (TypeError, ValueError):
            pass

        print(f"{local:<24} {snapshot:<40} {str(size):<12} {retention}")


def main():
    if len(sys.argv) != 3:
        fail(f"Usage: {sys.argv[0]} <snapshot-log> <timezone>")

    format_snapshots(Path(sys.argv[1]).read_text(), sys.argv[2])


if __name__ == "__main__":
    main()
