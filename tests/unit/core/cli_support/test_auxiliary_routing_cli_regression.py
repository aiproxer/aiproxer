from src.core.cli_support.argument_parser_builder import ArgumentParserBuilder
from src.core.services.backend_registry import backend_registry


def test_auxiliary_routing_enabled_default_is_none():
    """
    Regression test for the misleading CLI source attribution bug.
    Verifies that --enable-auxiliary-routing defaults to None (not False)
    so that the applicator doesn't incorrectly record it as a CLI-sourced value
    when the flag is omitted.
    """
    builder = ArgumentParserBuilder(registry=backend_registry)
    parser = builder.build()

    # 1. Test when flag is omitted
    args = parser.parse_args([])
    assert args.auxiliary_routing_enabled is None

    # 2. Test when flag is provided
    args = parser.parse_args(["--enable-auxiliary-routing"])
    assert args.auxiliary_routing_enabled is True
