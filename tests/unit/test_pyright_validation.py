"""
Test to validate that pyright type checking passes on the src directory.

This test ensures that all source code passes pyright type checking,
which is important for maintaining code quality and catching type-related
bugs early. Pyright provides language-aware diagnostics similar to what
the LSP server provides during development.
"""

import contextlib
import hashlib
import json
import shutil
import subprocess
import time
from pathlib import Path

import pytest

# Ensure pyright validation tests run sequentially to prevent subprocess resource conflicts
pytestmark = pytest.mark.xdist_group("pyright_validation")


def _calculate_directory_hash(directory: Path) -> str:
    """Calculate a hash of Python files for cache invalidation.

    Uses directory mtime plus every ``*.py`` file mtime (stable, avoids stale cache).
    """
    hasher = hashlib.md5()
    try:
        dir_stat = directory.stat()
        hasher.update(f"{directory}:{dir_stat.st_mtime}".encode())
    except OSError:
        pass

    py_files = sorted(directory.rglob("*.py"), key=lambda p: p.as_posix())
    for path in py_files:
        try:
            file_stat = path.stat()
            hasher.update(
                f"{path.relative_to(directory).as_posix()}:{file_stat.st_mtime_ns}".encode()
            )
        except OSError:
            continue
    return hasher.hexdigest()


def _calculate_pyright_inputs_hash(src_dir: Path, config_file: Path) -> str:
    """Calculate a hash for pyright inputs to support cache invalidation."""
    hasher = hashlib.md5()
    hasher.update(_calculate_directory_hash(src_dir).encode())

    try:
        config_stat = config_file.stat()
        hasher.update(f"{config_file}:{config_stat.st_mtime}".encode())
    except OSError:
        hasher.update(f"{config_file}:missing".encode())

    return hasher.hexdigest()


def _normalize_pyright_output(text: str) -> str:
    """Normalize pyright output to remove problematic Unicode characters.

    Pyright may output Unicode spacing/formatting characters that don't
    display correctly on Windows consoles. This function normalizes them
    to standard ASCII equivalents.
    """
    if not text:
        return text

    # Replace common problematic Unicode whitespace/formatting characters
    # with their ASCII equivalents
    replacements = {
        "\u00a0": " ",  # Non-breaking space -> regular space
        "\u2000": " ",  # En quad -> regular space
        "\u2001": " ",  # Em quad -> regular space
        "\u2002": " ",  # En space -> regular space
        "\u2003": " ",  # Em space -> regular space
        "\u2004": " ",  # Three-per-em space -> regular space
        "\u2005": " ",  # Four-per-em space -> regular space
        "\u2006": " ",  # Six-per-em space -> regular space
        "\u2007": " ",  # Figure space -> regular space
        "\u2008": " ",  # Punctuation space -> regular space
        "\u2009": " ",  # Thin space -> regular space
        "\u200a": " ",  # Hair space -> regular space
        "\u202f": " ",  # Narrow no-break space -> regular space
        "\u205f": " ",  # Medium mathematical space -> regular space
        "\u3000": " ",  # Ideographic space -> regular space
    }

    result = text
    for unicode_char, replacement in replacements.items():
        result = result.replace(unicode_char, replacement)

    # Some environments (notably when output passes through an OEM code page) can
    # mis-decode UTF-8 non-breaking spaces (0xC2 0xA0) as the two-character
    # sequence "┬á". Replace these artifacts with a normal space so diagnostics
    # remain readable in logs and failure messages.
    result = result.replace("\u252c\u00e1", " ")

    # Also normalize any UTF-8 encoding errors that might have occurred
    # by ensuring the string is properly encoded/decoded
    with contextlib.suppress(UnicodeEncodeError, UnicodeDecodeError):
        # Re-encode and decode to ensure clean UTF-8
        result = result.encode("utf-8", errors="replace").decode(
            "utf-8", errors="replace"
        )

    return result


def _find_pyright_command() -> str:
    """Find the pyright command to use.

    Returns:
        Path to pyright executable or 'pyright' if found in PATH.

    Raises:
        pytest.skip: If pyright is not found.
    """
    # First check if pyright is in PATH
    pyright_path = shutil.which("pyright")
    if pyright_path:
        return pyright_path

    # If not found, skip the test
    pytest.skip("pyright not found in PATH. Install with: npm install -g pyright")


class TestPyrightValidation:
    """Test class for pyright validation of source code."""

    @pytest.fixture(scope="session")
    def pyright_result(self) -> subprocess.CompletedProcess[str]:
        """Run pyright once per session and cache the result."""
        # Get the path to the src directory
        project_root = Path(__file__).parent.parent.parent
        src_path = project_root / "src"
        pyright_config_path = project_root / "pyrightconfig.src.json"

        # Ensure src directory exists
        assert src_path.exists(), f"Source directory not found at {src_path}"
        assert src_path.is_dir(), f"Source path {src_path} is not a directory"
        assert (
            pyright_config_path.exists()
        ), f"Pyright src config not found at {pyright_config_path}"

        # Find pyright command
        pyright_cmd = _find_pyright_command()

        # Setup cache
        cache_dir = project_root / ".pytest_cache"
        cache_dir.mkdir(exist_ok=True)
        cache_file = cache_dir / "pyright_validation_cache.json"

        # Calculate hash for cache invalidation
        src_hash = _calculate_pyright_inputs_hash(src_path, pyright_config_path)

        # Load existing cache
        cache: dict[str, str | int | float] = {}
        if cache_file.exists():
            try:
                with open(cache_file, encoding="utf-8") as f:
                    cache = json.load(f)
            except (OSError, json.JSONDecodeError):
                cache = {}

        # Check if cache is valid
        current_time = time.time()
        cache_timeout = 3600.0  # 1 hour

        cache_timestamp = cache.get("timestamp", 0)
        if isinstance(cache_timestamp, int | float):
            timestamp = float(cache_timestamp)
        else:
            timestamp = 0.0

        if (
            cache.get("src_hash") == src_hash
            and current_time - timestamp < cache_timeout
            and "returncode" in cache
        ):
            # Cache hit - return cached result
            cache_returncode = cache.get("returncode", 0)
            if isinstance(cache_returncode, int):
                returncode = cache_returncode
            else:
                returncode = 0

            # Normalize cached output as well
            cached_stdout = _normalize_pyright_output(str(cache.get("stdout", "")))
            cached_stderr = _normalize_pyright_output(str(cache.get("stderr", "")))

            return subprocess.CompletedProcess(
                args=[pyright_cmd, "--project", str(pyright_config_path)],
                returncode=returncode,
                stdout=cached_stdout,
                stderr=cached_stderr,
            )

        # Cache miss - run pyright
        # Run pyright on the src directory
        # Use a dedicated high-signal config for src/ to keep CI output actionable.
        try:
            result = subprocess.run(
                [pyright_cmd, "--project", str(pyright_config_path)],
                capture_output=True,
                text=True,
                encoding="utf-8",
                errors="replace",  # Replace invalid UTF-8 sequences instead of failing
                timeout=300,  # 5 minute timeout
                cwd=project_root,
            )

            # Normalize output to remove problematic Unicode characters
            normalized_stdout = _normalize_pyright_output(result.stdout)
            normalized_stderr = _normalize_pyright_output(result.stderr)

            # Save to cache
            cache = {
                "src_hash": src_hash,
                "timestamp": current_time,
                "returncode": result.returncode,
                "stdout": normalized_stdout,
                "stderr": normalized_stderr,
            }

            try:
                with open(cache_file, "w", encoding="utf-8") as f:
                    json.dump(cache, f, indent=2)
            except OSError:
                pass

            # Create a new result with normalized output
            result = subprocess.CompletedProcess(
                args=result.args,
                returncode=result.returncode,
                stdout=normalized_stdout,
                stderr=normalized_stderr,
            )

            return result
        except subprocess.TimeoutExpired:
            pytest.fail("pyright validation timed out after 5 minutes")
        except FileNotFoundError:
            pytest.skip("pyright not found. Install with: npm install -g pyright")

    def test_pyright_passes_on_src(
        self, pyright_result: subprocess.CompletedProcess[str]
    ) -> None:
        """
        Test that pyright type checking passes on the src directory.

        This test runs pyright on the src directory and fails if any
        type checking errors are detected. This helps ensure code
        quality and catches type-related issues early.

        The test uses the project's pyrightconfig.src.json configuration file
        to ensure consistent type checking behavior with a high signal/noise ratio.

        The pyright execution is cached at session level to improve performance.
        """
        # Check if pyright found any errors
        # Pyright exits with code 0 on success, non-zero on errors
        if pyright_result.returncode != 0:
            # pyright found errors, create a detailed failure message
            error_msg = (
                f"pyright type checking failed on src directory!\n\n"
                f"Exit code: {pyright_result.returncode}\n\n"
                f"STDOUT:\n{pyright_result.stdout}\n\n"
                f"STDERR:\n{pyright_result.stderr}\n\n"
                f"This indicates there are type checking errors in the source code.\n"
                f"Please run 'pyright --project pyrightconfig.src.json' locally to see the specific errors and fix them."
            )

            pytest.fail(error_msg)

        # pyright passed successfully
        # The result might still contain some output (like warnings)
        # but as long as the return code is 0, we consider it passed
        assert (
            pyright_result.returncode == 0
        ), f"pyright failed with unexpected return code: {pyright_result.returncode}"

    def test_pyright_config_exists(self) -> None:
        """
        Test that pyright src configuration exists in pyrightconfig.src.json.

        This ensures that the pyright validation is using the correct
        configuration for the project.
        """
        project_root = Path(__file__).parent.parent.parent
        pyrightconfig_path = project_root / "pyrightconfig.src.json"

        assert (
            pyrightconfig_path.exists()
        ), f"pyrightconfig.src.json not found at {pyrightconfig_path}"
        assert (
            pyrightconfig_path.is_file()
        ), f"pyrightconfig.src.json at {pyrightconfig_path} is not a file"

        # Verify it contains valid JSON configuration
        try:
            content = pyrightconfig_path.read_text()
            config = json.loads(content)
            assert isinstance(
                config, dict
            ), "pyrightconfig.json must contain a JSON object"
            assert len(content.strip()) > 0, "pyrightconfig.json appears to be empty"
        except json.JSONDecodeError as e:
            pytest.fail(f"pyrightconfig.json contains invalid JSON: {e}")
