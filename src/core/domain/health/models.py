from __future__ import annotations

from pydantic import BaseModel, Field


class EndpointHealthSummary(BaseModel):
    total_endpoints: int
    healthy_endpoints: int
    unhealthy_endpoints: int


class EndpointBackendInfo(BaseModel):
    api_url: str
    backend_type: str
    is_endpoint_healthy: bool


class EndpointHealthStateInfo(BaseModel):
    api_url: str
    is_healthy: bool
    ping_check_success: bool
    http_check_success: bool
    last_ping_check_timestamp: str | None = None
    last_http_check_timestamp: str | None = None
    last_successful_ping_timestamp: str | None = None
    last_successful_http_timestamp: str | None = None
    consecutive_ping_failures: int
    consecutive_http_failures: int
    last_ping_latency_ms: float | None = None
    last_http_latency_ms: float | None = None
    last_http_status_code: int | None = None
    last_ping_error: str | None = None
    last_http_error: str | None = None
    backends_using_url: list[str]


class HealthInfo(BaseModel):
    enabled: bool
    note: str | None = None
    endpoints: list[EndpointHealthStateInfo] = Field(default_factory=list)
    backends: list[EndpointBackendInfo] = Field(default_factory=list)
    summary: EndpointHealthSummary | None = None
    error: str | None = None


class MemoryHealthInfo(BaseModel):
    enabled: bool
    available: bool
    database_connected: bool | None = None
    analysis_worker_running: bool | None = None
    queue_depth: int | None = None
    active_sessions: int | None = None
    buffered_sessions: int | None = None
    note: str | None = None
    error: str | None = None


class SystemHealthInfo(BaseModel):
    service_provider_present: bool
    IRequestProcessor_resolvable: bool | None = None
    IRequestProcessor_error: str | None = None
    ChatController_resolvable: bool | None = None
    ChatController_error: str | None = None
    endpoint_health: HealthInfo | None = None
    memory_health: MemoryHealthInfo | None = None
    registered_descriptors: list[str] = Field(default_factory=list)
    descriptor_error: str | None = None
    error: str | None = None
    access_mode: str | None = None
