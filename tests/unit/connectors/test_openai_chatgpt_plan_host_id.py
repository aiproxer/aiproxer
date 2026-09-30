"""Unit tests for SIWC ext_agent_host_id minting and format validation."""

from __future__ import annotations

import re
from datetime import datetime, timezone
from uuid import UUID

import pytest
from pydantic import ValidationError

from src.connectors.openai_chatgpt_plan.models import (
    ChatGPTPlanHostState,
    is_valid_ext_agent_host_id,
)
from src.connectors.openai_chatgpt_plan.storage import _new_host_id


def test_new_host_id_mints_urn_uuid_v4() -> None:
    value = _new_host_id()
    assert value.startswith("urn:uuid:")
    assert is_valid_ext_agent_host_id(value)
    uuid_part = value.removeprefix("urn:uuid:")
    assert re.fullmatch(
        r"[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-"
        r"[0-9a-fA-F]{4}-[0-9a-fA-F]{12}",
        uuid_part,
    )
    assert UUID(uuid_part).version == 4
    # Fresh mint each call
    assert _new_host_id() != value


@pytest.mark.parametrize(
    "value",
    [
        "urn:uuid:11111111-1111-4111-8111-111111111111",
        "urn:ietf:params:oauth:jwk-thumbprint:abcDEFghiJKLmnoPQRstuVW_xyz012",
        "did:key:z6MkiTBz1ymuepAQ4HEHYSF1H8quG5GLVVQR3djdX3mDooWp",
    ],
)
def test_accepted_siwc_host_id_formats(value: str) -> None:
    assert is_valid_ext_agent_host_id(value)
    state = ChatGPTPlanHostState(
        schema_version=1,
        ext_agent_host_id=value,
        created_at=datetime.now(timezone.utc),
    )
    assert state.ext_agent_host_id == value


@pytest.mark.parametrize(
    "value",
    [
        "bt75cCs7A6xXS2uAq4rwA3MHgE3vlmQkdf8ynGGFcCI",
        "testhostid00000001",
        "urn:uuid:not-a-uuid",
        "urn:uuid:11111111111141118111111111111111",  # missing hyphens
        "uuid:11111111-1111-4111-8111-111111111111",
        "did:web:example.com",
        "user@example.com",
        "urn:uuid:11111111-1111-4111-8111-111111111111 extra",
        "",
        "   ",
    ],
)
def test_rejects_bare_opaque_and_other_invalid_host_ids(value: str) -> None:
    assert not is_valid_ext_agent_host_id(value)
    with pytest.raises(ValidationError):
        ChatGPTPlanHostState(
            schema_version=1,
            ext_agent_host_id=value,
            created_at=datetime.now(timezone.utc),
        )
