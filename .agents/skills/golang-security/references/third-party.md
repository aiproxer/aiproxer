# Third-Party Data Leak Rules

Third-party monitoring and analytics services can inadvertently transmit sensitive user data to external systems.

**Rules:**

1. PII MUST be filtered before sending to third-party services.
2. Error tracking MUST NOT receive raw user data — use `BeforeSend` hooks to redact.

---

## Overview

These rules detect Go code that sends data to third-party services. Always review what data is being transmitted and ensure it complies with privacy regulations (GDPR, CCPA, etc.).

---

## Common Vulnerable Services

| Service          | Risk                                                  |
| ---------------- | ----------------------------------------------------- |
| Airbrake         | Error tracking - sensitive data may be sent           |
| Bugsnag          | Error tracking - sensitive data exposure              |
| Sentry           | Error tracking - sensitive data in breadcrumbs/events |
| Rollbar          | Error tracking - sensitive data leaks                 |
| Honeybadger      | Error tracking - sensitive data leaks                 |
| New Relic        | Monitoring - sensitive data exposure                  |
| Datadog          | Monitoring - sensitive data in telemetry              |
| OpenTelemetry    | Observability - sensitive data in traces/metrics      |
| Google Analytics | Analytics - PII tracking risks                        |
| Algolia          | Search API - data exfiltration risks                  |
| Segment          | Analytics - PII tracking risks                        |
| BigQuery         | Analytics - sensitive data in queries                 |
| ClickHouse       | Database - sensitive data queries                     |
| Elasticsearch    | Search engine - sensitive data in queries             |

---

## Error Tracking Services

**Bad:**

```go
errorReporter.Capture(err, r) // ships the raw request, headers, and error text
```

**Good:**

```go
// Strip credentials and personal data before an event leaves the process.
// Verify the pinned SDK's defaults (PII flags, integrations, breadcrumbs) as well.
func scrubEvent(ev *ReportEvent) *ReportEvent {
    for _, h := range []string{"Authorization", "Cookie", "X-Api-Key"} {
        delete(ev.Headers, h)
    }
    ev.Message = redact(ev.Message)
    return ev
}
```

---

## Analytics/Monitoring Services

**Bad:**

```go
analytics.Track("user_signed_up", analytics.Properties{
    "email":   user.Email,   // DON'T: PII!
    "phone":   user.Phone,   // DON'T: PII!
    "address": user.Address, // DON'T: PII!
})
```

**Good:**

```go
analytics.Track("user_signed_up", analytics.Properties{
    "user_id":        user.ID,          // only after purpose/access/retention review
    "plan":           user.Plan,
    "country":        user.CountryCode,
})

// Use a protected, rotated key when correlation is necessary; this remains linkable data.
func correlationID(key []byte, value string) string {
    mac := hmac.New(sha256.New, key)
    _, _ = mac.Write([]byte(value))
    return hex.EncodeToString(mac.Sum(nil)[:16])
}
```

An internal ID, salted hash, or keyed hash is pseudonymous when it links events. Minimize it, restrict access, set retention/deletion rules, and document why correlation is needed. Prefer no identifier when aggregate metrics answer the question.

---

## Data Filtering Layer

```go
type DataFilter struct {
    sensitiveFields []string
}

func NewDataFilter() *DataFilter {
    return &DataFilter{
        sensitiveFields: []string{
            "password", "token", "secret", "key", "email",
            "phone", "address", "ssn", "credit_card", "bank_account",
        },
    }
}

func (f *DataFilter) Filter(data map[string]interface{}) map[string]interface{} {
    result := make(map[string]interface{})
    for k, v := range data {
        keyLower := strings.ToLower(k)
        isSensitive := false
        for _, field := range f.sensitiveFields {
            if strings.Contains(keyLower, field) {
                isSensitive = true
                break
            }
        }
        if isSensitive {
            result[k] = "[REDACTED]"
        } else {
            result[k] = v
        }
    }
    return result
}
```

---

## Review Checklist

Before integrating any third-party service:

- [ ] Identify what data is being sent
- [ ] Remove any PII/PHI from transmitted data
- [ ] Review data residency requirements
- [ ] Implement data retention policies
- [ ] Set up data export logging/auditing
- [ ] Configure error handling to avoid data exposure
- [ ] Review terms of service for data usage
- [ ] Implement user consent management
- [ ] Support data deletion requests
- [ ] Conduct regular data flow audits

---

## CWE References

- **CWE-200**: Exposure of Sensitive Information
- **CWE-359**: Exposure of Private Personal Information
- **CWE-201**: Information Exposure Through Sent Data
