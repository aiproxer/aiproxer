# `aipruntime.Options` registration guidance

`pkg/aipruntime.Options` exposes the canonical non-money authority
registration seams used by runtime admission and coordination, plus the
optional attachment seams:

- `RequestRegistrations`
- `AttemptRegistrations`
- `ConcurrencyRegistration`
- `UsageLimits`
- `AdminMounts`
- `EnterpriseCapabilities`
- `MeteringObservationSink`

Legacy parallel provider fields were removed during the alpha-stage architecture
convergence. Monetary rating, customer settlement, and provider-cost accounting
are not runtime registration concerns; a host that needs them attaches exactly
one explicit `pkg/aipsdk/billing.Binding` through
`pkg/aipruntime.BuildWithBilling`. The public runtime surface carries none of
that logic itself, and the in-tree money implementation that predates this seam
is still being relocated to the Enterprise tree.

Example request-authority registration:

```go
opts := aipruntime.Options{
    RequestRegistrations: []authority.RequestRegistration{{
        Descriptor: authority.ProviderDescriptor{
            ID: "quota", Kind: authority.ProviderKindAuthority,
            Postures: []authority.StagePosture{{
                Stage: authority.StageRequestAdmit,
                Strength: authority.StrengthRequired,
                FailureBehavior: authority.FailureFailClosed,
            }},
        },
        Priority: authority.RequestPriorityQuotaBudgetRate,
        Provider: myRequest{},
    }},
}
```

Do not attach a stream-time rater or monetary ledger to the runtime. Provide
the pricing/rate snapshots and the durable money ports at the binding boundary
instead. Configuration decides whether an attachment exists: every port a host
does not attach keeps its no-op default. The stock command is the transitional
case — it still composes the in-tree money path and declares the billing
capability until that path is relocated, while usage limits are already
enforced only through an injected port.
