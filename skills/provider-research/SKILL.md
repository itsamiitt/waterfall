---
name: provider-research
description: >-
  Strict uniform template for documenting each data Provider and competitor so all entries are
  directly comparable. Requires source_url + verified_date on every factual claim; anything missing
  them is auto-flagged UNVERIFIED. Invoke whenever adding/updating a Provider or competitor entry.
version: 1.0.0
---

# Skill: provider-research

## Purpose
Make every Provider/competitor entry **comparable** (same fields, same units) and **honest** (every
fact cited or flagged). Powers `03-Provider-Research.md`, `01-Market-Research.md`, and the
`/provider-audit` command.

## Hard rules
1. **Every factual cell** about a Provider/competitor MUST carry an inline `[source_url, verified_date]`
   or be written as the literal token `UNVERIFIED`. No exceptions, no "common knowledge".
2. Use a **real, citable, primary source** where possible (official API docs, pricing page, trust/
   compliance page, status page). Secondary sources (blogs, comparison sites) are allowed but must
   be labeled `secondary` and are weaker evidence.
3. **Units are fixed** (below). Convert everything into them; never mix.
4. If a Provider violates the API-first/no-scraping constraint, mark it `EXCLUDED` with reason.
5. Never invent rate limits, prices, latency, or compliance status. Unknown = `UNVERIFIED`.

## Fixed units
- Latency: milliseconds (p50/p95), labeled `vendor-stated` vs `measured`.
- Rate limit: requests/second AND requests/day (note burst vs sustained).
- Price: USD; per-credit AND effective per-successful-match where derivable; note credit model.
- Coverage/accuracy: % with the population it was measured on; vendor-claimed vs independent.
- Compliance: enum {SOC2-Type-II, SOC2-Type-I, ISO27001, GDPR-DPA, CCPA, HIPAA, none, UNVERIFIED}.

## Uniform entry template (copy per Provider)
```md
### <Provider Name>  — category: <business-email|mobile|company|intent|verification|identity|...>
- Status: <ACTIVE-CANDIDATE | EXCLUDED | DEPRIORITIZED>   (EXCLUDED needs a reason)
- Website / docs root: <url> [verified_date]
| Attribute | Value | Source | Verified date | Confidence |
|-----------|-------|--------|---------------|------------|
| Auth method | e.g. API key header / OAuth2 | <url> | YYYY-MM-DD | VERIFIED/UNVERIFIED |
| REST API | yes/no + base URL | | | |
| Bulk API | yes/no + max batch | | | |
| Batch/async API | yes/no | | | |
| Pagination | cursor/offset/none | | | |
| Webhooks | yes/no + events | | | |
| Rate limit (req/s) | | | | |
| Rate limit (req/day) | | | | |
| Concurrency cap | | | | |
| Latency p50/p95 (ms) | + vendor-stated/measured | | | |
| Coverage | % + population | | | |
| Accuracy | % + method | | | |
| Pricing model | credits/subscription/usage | | | |
| Price (USD) | per credit + per match | | | |
| Error codes | list + meanings | | | |
| Retry behavior | idempotency support? 429 semantics? | | | |
| SDKs | langs | | | |
| API stability/versioning | versioned? deprecation policy? | | | |
| Regional availability | regions / data residency | | | |
| Compliance | SOC2/ISO/GDPR/CCPA | | | |
- Coverage strengths: <fields it is best for>
- Known weaknesses / gaps:
- Waterfall placement hypothesis: <first/parallel/fallback for which fields, and why>
```

## Checklist before marking a Provider entry `VERIFIED`
- [ ] Every row has a source_url + verified_date OR says `UNVERIFIED`.
- [ ] Units match the fixed units above.
- [ ] Compliance claims link to the vendor trust/compliance page, not a blog.
- [ ] Pricing reflects a dated snapshot (pricing changes — record the date).
- [ ] API-first constraint satisfied (else `EXCLUDED`).
- [ ] A waterfall-placement hypothesis is stated (feeds `08`/`09` routing).
