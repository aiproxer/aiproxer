from __future__ import annotations

import json
import logging
import re
from collections import defaultdict
from difflib import get_close_matches
from pathlib import Path
from typing import Any

from src.core.config.models.misc import ModelRegistryConfig
from src.core.domain.model_capabilities import ModelLimits
from src.core.domain.model_catalog_match import (
    ModelCatalogMatchResult,
    ModelCatalogMatchTier,
)

logger = logging.getLogger(__name__)

# Minimum length of request string for prefix-based catalog matching (avoids "gpt" -> many).
_MIN_PREFIX_LEN = 8

# Fuzzy match cutoff (difflib): higher = stricter.
_FUZZY_CUTOFF = 0.86

_TOKEN_SPLIT = re.compile(r"[^a-z0-9]+", re.IGNORECASE)


class ModelCatalogService:
    """Service for managing model metadata from an external registry."""

    _BACKEND_TO_CATALOG_PROVIDER: dict[str, str] = {
        "gemini": "google",
        "openai": "openai",
        "anthropic": "anthropic",
        "openrouter": "openrouter",
        "deepseek": "deepseek",
        "mistral": "mistral",
        "openai-codex": "openai",
        "gemini-oauth-free": "google",
        "gemini-oauth-plan": "google",
        "gemini-cloud-project": "google",
        "gemini-cli-cloud-project": "google",
        "antigravity-oauth": "google",
        "kiro-oauth-auto": "google",
        "openai-responses": "openai",
        "openai-chatgpt-plan": "openai",
    }

    def __init__(self, config: ModelRegistryConfig) -> None:
        self._config = config
        self._provider_limits: dict[str, dict[str, ModelLimits]] = {}
        self._provider_modalities: dict[str, dict[str, set[str]]] = {}
        self._casefold_to_canonical: dict[str, dict[str, str]] = {}
        self._global_casefold_hits: dict[str, list[tuple[str, str]]] = {}
        self._tail_index: dict[str, list[tuple[str, str]]] = {}
        self._total_model_entries: int = 0
        self._catalog_loaded: bool = False
        self.load_catalog()

    def load_catalog(self) -> None:
        """Load the model catalog from cache or bootstrap file."""
        cache_path = Path(self._config.cache_path)
        bootstrap_path = Path(self._config.bootstrap_path)

        path_to_load = cache_path if cache_path.exists() else bootstrap_path

        if not path_to_load.exists():
            logger.warning(
                "Model catalog file not found at %s or %s", cache_path, bootstrap_path
            )
            return

        try:
            with open(path_to_load, encoding="utf-8") as f:
                data = json.load(f)
                self._parse_catalog(data)
                if logger.isEnabledFor(logging.INFO):
                    logger.info(
                        "Loaded %d model entries from %s",
                        self._total_model_entries,
                        path_to_load,
                    )
        except Exception as e:
            logger.error(
                "Failed to load model catalog from %s: %s",
                path_to_load,
                e,
                exc_info=True,
            )

    def _parse_catalog(self, data: dict[str, Any]) -> None:
        """Parse the raw catalog data into provider-scoped structures."""
        provider_limits: dict[str, dict[str, ModelLimits]] = defaultdict(dict)
        provider_modalities: dict[str, dict[str, set[str]]] = defaultdict(dict)
        casefold_map: dict[str, dict[str, str]] = defaultdict(dict)
        global_cf: dict[str, list[tuple[str, str]]] = defaultdict(list)
        tail_index: dict[str, list[tuple[str, str]]] = defaultdict(list)
        total = 0

        for provider_id, provider_info in data.items():
            if not isinstance(provider_info, dict):
                continue
            models_data = provider_info.get("models", {})
            if not isinstance(models_data, dict):
                continue
            for model_id, info in models_data.items():
                if not isinstance(model_id, str):
                    continue
                limits_raw = info.get("limit", {}) if isinstance(info, dict) else {}
                context_window = limits_raw.get("context")
                max_output = limits_raw.get("output")

                modalities = (
                    info.get("modalities", {}) if isinstance(info, dict) else {}
                )
                input_modalities: set[str] | None = None
                if isinstance(modalities, dict):
                    raw_inputs = modalities.get("input")
                    if isinstance(raw_inputs, list):
                        input_modalities = {
                            str(item) for item in raw_inputs if isinstance(item, str)
                        }

                model_limits = ModelLimits(
                    context_window=context_window,
                    max_output_tokens=max_output,
                    max_input_tokens=context_window,
                )

                provider_limits[provider_id][model_id] = model_limits
                if input_modalities:
                    provider_modalities[provider_id][model_id] = input_modalities

                cf = model_id.casefold()
                casefold_map[provider_id][cf] = model_id
                global_cf[cf].append((provider_id, model_id))

                tail = model_id.split("/")[-1].casefold()
                tail_index[tail].append((provider_id, model_id))
                total += 1

        self._provider_limits = dict(provider_limits)
        self._provider_modalities = dict(provider_modalities)
        self._casefold_to_canonical = dict(casefold_map)
        self._global_casefold_hits = dict(global_cf)
        self._tail_index = dict(tail_index)
        self._total_model_entries = total
        self._catalog_loaded = total > 0

    @staticmethod
    def _map_backend_to_provider(backend_type: str | None) -> str | None:
        if not backend_type or not str(backend_type).strip():
            return None
        key = str(backend_type).strip().casefold()
        for cand, prov in ModelCatalogService._BACKEND_TO_CATALOG_PROVIDER.items():
            if cand.casefold() == key:
                return prov
        return None

    @staticmethod
    def _route_variants(model_name: str) -> list[str]:
        s = model_name.strip()
        out: list[str] = [s]
        seen = {s.casefold()}
        sl = s.casefold()
        for suf in (":free", ":nitro"):
            if sl.endswith(suf):
                stripped = s[: -len(suf)]
                if stripped.casefold() not in seen:
                    out.append(stripped)
                    seen.add(stripped.casefold())
        return out

    def _result_for(
        self,
        provider_id: str,
        canonical_id: str,
        tier: ModelCatalogMatchTier,
    ) -> ModelCatalogMatchResult:
        lim = self._provider_limits.get(provider_id, {}).get(canonical_id)
        mods_raw = self._provider_modalities.get(provider_id, {}).get(canonical_id)
        mods_f = frozenset(mods_raw) if mods_raw else None
        key = f"{provider_id}/{canonical_id}"
        return ModelCatalogMatchResult(
            tier=tier,
            limits=lim,
            input_modalities=mods_f,
            resolved_catalog_key=key,
            catalog_provider_id=provider_id,
        )

    def _token_overlap_pick(
        self, request_norm: str, provider_id: str
    ) -> ModelCatalogMatchResult | None:
        models = self._provider_limits.get(provider_id, {})
        if not models:
            return None
        req_tokens = [t for t in _TOKEN_SPLIT.split(request_norm) if len(t) >= 2]
        if len(req_tokens) < 2:
            return None
        req_set = set(req_tokens)
        scored: list[tuple[float, str]] = []
        for mid in models:
            mtoks = [t for t in _TOKEN_SPLIT.split(mid.casefold()) if len(t) >= 2]
            if not mtoks:
                continue
            inter = len(req_set.intersection(mtoks))
            score = inter / max(len(req_set), 1)
            scored.append((score, mid))
        scored.sort(key=lambda x: (-x[0], x[1]))
        if not scored:
            return None
        if len(scored) < 2:
            best_s, best_id = scored[0]
            if best_s >= 0.5:
                return self._result_for(
                    provider_id, best_id, ModelCatalogMatchTier.TOKEN_OVERLAP
                )
            return None
        best_s, best_id = scored[0]
        second_s, _ = scored[1]
        if best_s < 0.5:
            return None
        if best_s - second_s < 0.01:
            return None
        return self._result_for(
            provider_id, best_id, ModelCatalogMatchTier.TOKEN_OVERLAP
        )

    def _fuzzy_pick(self, nm: str, provider_id: str) -> ModelCatalogMatchResult | None:
        models = self._provider_limits.get(provider_id, {})
        keys = list(models.keys())
        if not keys:
            return None
        matches = get_close_matches(nm, keys, n=3, cutoff=_FUZZY_CUTOFF)
        if len(matches) != 1:
            return None
        return self._result_for(provider_id, matches[0], ModelCatalogMatchTier.FUZZY)

    def _resolve_scoped(
        self, nm: str, provider_id: str
    ) -> ModelCatalogMatchResult | None:
        models = self._provider_limits.get(provider_id, {})
        if not models:
            return None
        cf_map = self._casefold_to_canonical.get(provider_id, {})

        if nm in models:
            return self._result_for(provider_id, nm, ModelCatalogMatchTier.EXACT)

        ncf = nm.casefold()
        if ncf in cf_map:
            canon = cf_map[ncf]
            return self._result_for(
                provider_id, canon, ModelCatalogMatchTier.NORMALIZED
            )

        if "/" in nm:
            tail = nm.split("/")[-1].strip()
            if tail in models:
                return self._result_for(
                    provider_id, tail, ModelCatalogMatchTier.VENDOR_STRIPPED
                )
            tcf = tail.casefold()
            if tcf in cf_map:
                return self._result_for(
                    provider_id, cf_map[tcf], ModelCatalogMatchTier.VENDOR_STRIPPED
                )

        ncf = nm.casefold()
        if len(ncf) >= _MIN_PREFIX_LEN:
            prefix_hits = [
                mid
                for mid in models
                if mid.casefold().startswith(ncf) and mid.casefold() != ncf
            ]
            if len(prefix_hits) == 1:
                return self._result_for(
                    provider_id, prefix_hits[0], ModelCatalogMatchTier.PREFIX
                )

        overlap = self._token_overlap_pick(nm.casefold(), provider_id)
        if overlap is not None:
            return overlap

        fuzzy = self._fuzzy_pick(nm, provider_id)
        if fuzzy is not None:
            return fuzzy

        return None

    def _infer_colon_provider(self, nm: str) -> ModelCatalogMatchResult | None:
        """If ``provider:model`` and provider is a catalog id, resolve as scoped."""
        if ":" not in nm or "/" in nm:
            return None
        left, _, right = nm.partition(":")
        pid = left.strip()
        rmod = right.strip()
        if not pid or not rmod:
            return None
        if pid not in self._provider_limits:
            return None
        return self._resolve_scoped(rmod, pid)

    def _resolve_unscoped(self, nm: str) -> ModelCatalogMatchResult | None:
        colon = self._infer_colon_provider(nm)
        if colon is not None:
            return colon

        ncf = nm.casefold()
        hits = self._global_casefold_hits.get(ncf, [])
        if len(hits) == 1:
            pid, mid = hits[0]
            return self._result_for(pid, mid, ModelCatalogMatchTier.NORMALIZED)
        if len(hits) > 1:
            return None

        tail = nm.split("/")[-1].casefold()
        tail_hits = self._tail_index.get(tail, [])
        if len(tail_hits) == 1:
            pid, mid = tail_hits[0]
            return self._result_for(pid, mid, ModelCatalogMatchTier.VENDOR_STRIPPED)
        return None

    def resolve(
        self, model_name: str, backend_type: str | None = None
    ) -> ModelCatalogMatchResult:
        """Resolve catalog limits/modalities with ordered fallbacks (see plan)."""
        if not self._catalog_loaded:
            return ModelCatalogMatchResult(
                tier=ModelCatalogMatchTier.NONE,
                limits=None,
                input_modalities=None,
                resolved_catalog_key=None,
                catalog_provider_id=None,
            )

        provider_id = self._map_backend_to_provider(backend_type)
        variants = self._route_variants(model_name)

        if provider_id is not None:
            for v in variants:
                hit = self._resolve_scoped(v, provider_id)
                if hit is not None:
                    return hit
            return ModelCatalogMatchResult(
                tier=ModelCatalogMatchTier.NONE,
                limits=None,
                input_modalities=None,
                resolved_catalog_key=None,
                catalog_provider_id=None,
            )

        for v in variants:
            hit = self._resolve_unscoped(v)
            if hit is not None:
                return hit

        return ModelCatalogMatchResult(
            tier=ModelCatalogMatchTier.NONE,
            limits=None,
            input_modalities=None,
            resolved_catalog_key=None,
            catalog_provider_id=None,
        )

    def get_limits(
        self, model_name: str, backend_type: str | None = None
    ) -> ModelLimits | None:
        """Look up limits for a model, optionally restricted by backend type."""
        return self.resolve(model_name, backend_type).limits

    def has_model(self, model_name: str, backend_type: str | None = None) -> bool:
        """Return True if the catalog has an entry for the model."""
        if not self._catalog_loaded:
            return False
        return self.resolve(model_name, backend_type).limits is not None

    def get_input_modalities(
        self, model_name: str, backend_type: str | None = None
    ) -> set[str] | None:
        """Look up input modalities for a model (e.g., {'text', 'image'})."""
        mods = self.resolve(model_name, backend_type).input_modalities
        if mods is None:
            return None
        return set(mods)
