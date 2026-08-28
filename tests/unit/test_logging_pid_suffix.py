from src.core.cli_support.logging_configurator import LoggingConfigurator
from src.core.config.app_config import AppConfig, LoggingConfig


def test_timestamp_suffix_applied_once() -> None:
    import re

    # Mock datetime to ensure consistent timestamp during test
    timestamp_pattern = re.compile(r"-\d{8}_\d{6}-p\d+\.log$")

    cfg = AppConfig(
        logging=LoggingConfig(log_file="logs/proxy.log", capture_file="wire.log")
    )

    # Use LoggingConfigurator directly to avoid importing src.core.cli,
    # which triggers backend connector imports that can cause isolation issues
    configurator = LoggingConfigurator()
    updated = configurator.apply_pid_suffixes(cfg)

    log_file = updated.logging.log_file
    capture_file = updated.logging.capture_file

    assert log_file is not None
    assert capture_file is not None
    assert timestamp_pattern.search(log_file)
    assert timestamp_pattern.search(capture_file)

    # Verify format is YYYYMMDD_HHmmss-pPID
    # suffix example: -20260131_120400-p12345
    match = timestamp_pattern.search(log_file)
    assert match is not None
    suffix = match.group(0)[:-4]  # Remove .log
    assert suffix.startswith("-")
    parts = suffix[1:].split("-p")
    assert len(parts) == 2
    ts, pid = parts
    assert len(ts) == 15  # YYYYMMDD_HHMMSS
    assert ts[:8].isdigit()
    assert ts[8] == "_"
    assert ts[9:].isdigit()
    assert pid.isdigit()

    updated_again = configurator.apply_pid_suffixes(updated)

    assert updated_again.logging.log_file == updated.logging.log_file
    assert updated_again.logging.capture_file == updated.logging.capture_file
