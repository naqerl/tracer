#!/usr/bin/env python3
"""
Migration script to fix journalctl log entries.

This script:
1. Backs up all app databases
2. Fixes journalctl log entries by extracting raw MESSAGE from the journalctl JSON
3. Updates the message column with the raw MESSAGE content
4. For apps with JSON MESSAGE (like cyberpass), extracts Python fields into context
5. Removes journalctl internal fields from context
"""

import json
import os
import sqlite3
import subprocess
import sys
from pathlib import Path
from datetime import datetime

DATA_DIR = "/var/lib/tracer"
APPS_DIR = os.path.join(DATA_DIR, "apps")
BACKUP_DIR = os.path.join(DATA_DIR, "backups")

# Apps that log JSON to journalctl MESSAGE field (Python JSON logs)
JSON_MESSAGE_APPS = {
    "cyberpass-backend-production",
    "cyberpass-backend-staging",
    "mimi",
    "lumicare",
}

# Journalctl internal fields to remove from context
JOURNALCTL_INTERNAL_FIELDS = {
    "__CURSOR",
    "__MONOTONIC_TIMESTAMP",
    "__SEQNUM",
    "__SEQNUM_ID",
    "_BOOT_ID",
    "_CAP_EFFECTIVE",
    "_CMDLINE",
    "_EXE",
    "_GID",
    "_MACHINE_ID",
    "_RUNTIME_SCOPE",
    "_SELINUX_CONTEXT",
    "_STREAM_ID",
    "_SYSTEMD_CGROUP",
    "_SYSTEMD_INVOCATION_ID",
    "_SYSTEMD_SLICE",
    "_TRANSPORT",
    "_UID",
    "SYSLOG_FACILITY",
    "__REALTIME_TIMESTAMP",
    "PRIORITY",
    "MESSAGE",
    "MESSAGE_ID",
    "CODE_FILE",
    "CODE_FUNC",
    "CODE_LINE",
    "CPU_USAGE_NSEC",
    "INVOCATION_ID",
    "SOURCE_REALTIME_TIMESTAMP",
    "TID",
    "UNIT",
}


def create_backup():
    """Create a timestamped backup of all app databases."""
    print(f"\n[1/4] Creating backup...")

    os.makedirs(BACKUP_DIR, exist_ok=True)

    timestamp = datetime.now().strftime("%Y%m%d_%H%M%S")
    backup_file = os.path.join(BACKUP_DIR, f"apps_backup_{timestamp}.tar.gz")

    # Get list of all app database files
    db_files = []
    for f in os.listdir(APPS_DIR):
        if f.endswith(".db") and not f.endswith("-shm") and not f.endswith("-wal"):
            db_path = os.path.join(APPS_DIR, f)
            db_files.append(db_path)

    if not db_files:
        print("  No app databases found to backup")
        return False

    # Create tar archive
    cmd = ["tar", "-czf", backup_file, "-C", APPS_DIR] + [
        os.path.basename(f) for f in db_files
    ]
    result = subprocess.run(cmd, capture_output=True, text=True)

    if result.returncode != 0:
        print(f"  Backup failed: {result.stderr}")
        return False

    backup_size = os.path.getsize(backup_file) / (1024 * 1024)
    print(f"  Backup created: {backup_file} ({backup_size:.2f} MB)")

    # Also backup WAL and SHM files for each database
    for db_file in db_files:
        db_name = os.path.basename(db_file)
        base_name = db_file.replace(".db", "")

        for ext in ["-shm", "-wal"]:
            aux_file = base_name + ext
            if os.path.exists(aux_file):
                subprocess.run(
                    ["cp", aux_file, os.path.join(BACKUP_DIR, f"{db_name}{ext}")],
                    capture_output=True,
                )
                print(f"  Backup: {db_name}{ext}")

    return True


def is_journalctl_entry(raw: str) -> bool:
    """Check if an entry appears to be from journalctl."""
    if not raw:
        return False
    try:
        data = json.loads(raw)
        # Check for journalctl-specific fields
        return any(
            field in data
            for field in [
                "__REALTIME_TIMESTAMP",
                "PRIORITY",
                "MESSAGE",
                "_SYSTEMD_UNIT",
            ]
        )
    except (json.JSONDecodeError, TypeError):
        return False


def extract_raw_message(raw: str) -> str | None:
    """Extract the raw MESSAGE from journalctl JSON."""
    try:
        data = json.loads(raw)
        msg = data.get("MESSAGE")
        if msg is not None:
            return str(msg)
    except (json.JSONDecodeError, TypeError):
        pass
    return None


def build_context(raw: str, app_name: str = None) -> str:
    """Build a clean context from journalctl raw data.

    For JSON MESSAGE apps (cyberpass, mimi, lumicare):
    - Extract Python JSON fields from MESSAGE into context
    - Remove journalctl internal fields

    For other apps:
    - Keep context minimal/empty (no journalctl wrapper data)
    """
    try:
        data = json.loads(raw)
        context = {}

        # Check if MESSAGE contains JSON (common for Python apps)
        message = data.get("MESSAGE")
        if message:
            try:
                msg_data = json.loads(str(message))
                # Extract useful fields from Python JSON log
                if isinstance(msg_data, dict):
                    for key, value in msg_data.items():
                        if key in ("message", "msg"):
                            continue  # Skip duplicate of message field
                        if isinstance(value, (str, int, float, bool)):
                            context[key] = value
                        else:
                            context[key] = str(value)
            except (json.JSONDecodeError, TypeError):
                pass

        # Remove journalctl internal fields
        for field in JOURNALCTL_INTERNAL_FIELDS:
            data.pop(field, None)

        # For non-JSON apps, context stays empty
        # For JSON apps, context now has Python fields

        return json.dumps(context) if context else "{}"
    except (json.JSONDecodeError, TypeError):
        return "{}"


def fix_database(db_path: str, app_name: str = None) -> tuple[int, int]:
    """
    Fix journalctl entries in a database.

    Returns: (total_processed, fixed_count)
    """
    if not os.path.exists(db_path):
        return 0, 0

    fixed = 0
    total = 0

    # Extract app name from filename
    if app_name is None:
        db_name = os.path.basename(db_path).replace(".db", "")
        app_name = db_name

    conn = sqlite3.connect(db_path)
    conn.row_factory = sqlite3.Row
    cursor = conn.cursor()

    # Get all entries with raw journalctl data
    cursor.execute(
        "SELECT id, raw, message, context FROM logs WHERE raw LIKE '%__REALTIME_TIMESTAMP%' OR raw LIKE '%\"MESSAGE\":%'"
    )
    rows = cursor.fetchall()

    for row in rows:
        total += 1
        row_id = row["id"]
        raw = row["raw"]
        current_message = row["message"]
        current_context = row["context"]

        if not is_journalctl_entry(raw):
            continue

        # Extract raw message from journalctl
        raw_message = extract_raw_message(raw)
        if raw_message is None:
            continue

        # Build clean context
        new_context = build_context(raw, app_name)

        # Check if entry needs fixing
        needs_fix = False

        # Fix 1: Message differs from raw MESSAGE
        if raw_message != current_message:
            needs_fix = True

        # Fix 2: Context still has journalctl internal fields
        try:
            ctx_data = json.loads(current_context) if current_context else {}
            has_journalctl_fields = any(
                field in ctx_data
                for field in [
                    "_COMM",
                    "SYSLOG_IDENTIFIER",
                    "_HOSTNAME",
                    "_PID",
                    "_SYSTEMD_UNIT",
                    "_TRANSPORT",
                ]
            )
            if has_journalctl_fields:
                needs_fix = True
        except json.JSONDecodeError:
            needs_fix = True

        if needs_fix:
            cursor.execute(
                "UPDATE logs SET message = ?, context = ? WHERE id = ?",
                (raw_message, new_context, row_id),
            )
            fixed += 1

    conn.commit()
    conn.close()

    return total, fixed


def run_migration():
    """Run the full migration."""
    print("\n" + "=" * 60)
    print("Journalctl Log Migration Script")
    print("=" * 60)

    # Step 1: Backup
    if not create_backup():
        print("\nERROR: Backup failed, aborting migration")
        sys.exit(1)

    # Step 2: Find all databases
    print(f"\n[2/4] Scanning for app databases in {APPS_DIR}...")

    databases = []
    for f in os.listdir(APPS_DIR):
        if f.endswith(".db") and not f.endswith("-shm") and not f.endswith("-wal"):
            db_path = os.path.join(APPS_DIR, f)
            db_size = os.path.getsize(db_path) / (1024 * 1024)
            databases.append((f, db_path, db_size))

    if not databases:
        print("  No app databases found")
        return

    print(f"  Found {len(databases)} databases:")
    for name, _, size in sorted(databases):
        print(f"    - {name} ({size:.2f} MB)")

    # Step 3: Process each database
    print(f"\n[3/4] Processing databases...")

    total_processed = 0
    total_fixed = 0

    for name, db_path, size in sorted(databases):
        print(f"\n  Processing {name}...", end=" ", flush=True)
        processed, fixed = fix_database(db_path, name.replace(".db", ""))
        total_processed += processed
        total_fixed += fixed
        print(f"checked {processed}, fixed {fixed}")

    # Step 4: Summary
    print(f"\n[4/4] Migration Complete")
    print("=" * 60)
    print(f"  Total entries checked: {total_processed}")
    print(f"  Total entries fixed:   {total_fixed}")
    print(f"  Backup location:       {BACKUP_DIR}")
    print("=" * 60)


if __name__ == "__main__":
    run_migration()
