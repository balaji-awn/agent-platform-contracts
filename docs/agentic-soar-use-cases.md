# Top 20 Agentic SOAR Workflow Use Cases

Each use case lists the trigger that starts it, the trigger type, and what the agent does once it runs.

## Trigger types

| Type | Description |
|---|---|
| Event | Webhook or queue message from a SIEM, EDR, IdP, CSPM, or other tool |
| Message | Email, chat, or user report |
| Schedule | Cron-based recurring run |
| Threshold | Metric or count crossing a defined limit |
| Manual | Analyst action or case state change |
| Chained | Output of another workflow emits an event |

---

## 1. Triage & Investigation

| # | Use case | Trigger | Trigger type | What the agent does |
|---|---|---|---|---|
| 1 | Alert triage and deduplication | New alert from SIEM, EDR, or NDR | Event | Clusters related alerts, suppresses noise, assigns severity with a rationale |
| 2 | Phishing email analysis | Email in report-phishing mailbox, or user clicks "Report" in mail client | Message | Parses headers, detonates URLs and attachments, checks sender reputation, decides to quarantine or close |
| 3 | Enrichment orchestration | New IOC added to a case, or alert contains unenriched observables | Event / Manual | Selects which threat-intel, WHOIS, geo-IP, and asset lookups are worth running |
| 4 | Suspicious login investigation | IdP event: impossible travel, repeated MFA denials, new device or country | Event | Correlates travel, MFA fatigue, device fingerprints, and user history before escalating |
| 5 | Endpoint alert investigation | EDR detection: suspicious process, credential dumping, unsigned binary | Event | Pulls process tree, parent/child anomalies, file hashes; summarizes likely intent |
| 6 | Cloud misconfiguration triage | CSPM finding, or cloud audit event (e.g. bucket policy changed to public) | Event | Weighs findings against real exposure and data sensitivity |
| 7 | Insider threat signals | DLP alert, HR resignation/termination event, unusual bulk downloads | Event | Combines DLP, HR context, and access anomalies into a case narrative for human review |

## 2. Response & Containment

| # | Use case | Trigger | Trigger type | What the agent does |
|---|---|---|---|---|
| 8 | Adaptive containment | Case severity raised to high/critical, or "confirmed malicious" verdict | Manual / Chained | Chooses host isolation, account disablement, or token revocation based on blast radius and business criticality |
| 9 | Compromised credential response | Leaked credential feed match, password spray detection, or confirmed takeover from #4 | Event / Chained | Resets credentials, kills sessions, audits recent actions, notifies the owner |
| 10 | Malware outbreak scoping | Confirmed malware verdict on a host, or new malicious hash on blocklist | Chained / Event | Hunts same IOCs and TTPs across the fleet; builds affected-asset list |
| 11 | Ransomware early response | Mass file renames/modifications, shadow copy deletion, known signatures | Threshold / Event | Detects mass encryption, triggers segmentation, snapshots critical systems |
| 12 | Business email compromise handling | New external forwarding rule, finance-flagged payment change, or user report | Event / Message | Traces mailbox rules, forwarding, and suspicious payments; coordinates with finance |

## 3. Proactive & Hunting

| # | Use case | Trigger | Trigger type | What the agent does |
|---|---|---|---|---|
| 13 | Hypothesis-driven threat hunting | New threat intel report, weekly schedule, or analyst start | Event / Schedule / Manual | Turns a report into queries, runs them, interprets results |
| 14 | Vulnerability prioritization | Scan completes, new CVE added to CISA KEV, public exploit released | Event | Ranks CVEs by exploitability, exposure, and compensating controls rather than CVSS alone |
| 15 | Threat intel ingestion | New RSS/TAXII item, vendor advisory email, PDF in shared folder | Event / Message | Extracts IOCs and TTPs from unstructured reports; maps to MITRE ATT&CK and detections |
| 16 | Detection gap analysis | Monthly schedule, closed incident with no detection, ATT&CK update | Schedule / Event | Compares ATT&CK coverage against recent incidents; suggests new detection rules |

## 4. Operations & Reporting

| # | Use case | Trigger | Trigger type | What the agent does |
|---|---|---|---|---|
| 17 | Case summarization and handoff | Shift-change schedule, case reassigned, case idle for N hours | Schedule / Manual / Threshold | Writes summaries, timelines, and next steps for open incidents |
| 18 | Incident report drafting | Case status changes to resolved or closed | Manual | Generates executive and technical post-incident reports from case data |
| 19 | Access review automation | Quarterly schedule, HR role change, new privileged group assignment | Schedule / Event | Flags stale, excessive, or toxic-combination permissions; routes approvals |
| 20 | Playbook generation and tuning | Threshold of analyst overrides on same step, or weekly review | Threshold / Schedule | Proposes new workflows or edits based on overrides and recurring manual steps |

---

## Workflow chaining

Workflows often feed each other, so the engine should let a workflow's output emit an event that starts another, not only accept external triggers. Example chains:

- **#4 Suspicious login** → confirmed takeover → **#9 Credential response** → **#8 Adaptive containment**
- **#5 Endpoint alert** → malicious verdict → **#10 Malware scoping** → **#8 Adaptive containment**
- **#3 Enrichment** → invoked as a sub-step by #1, #2, #4, #5, #12
- Any response workflow → case closed → **#18 Incident report**
- Closed incidents without detections → **#16 Detection gap analysis**

## Design guidance

- Let the agent handle planning, enrichment choice, correlation, and summarization.
- Keep destructive actions (isolation, account disablement, deletion, payment holds) behind human-approval gates or policy-bound tool permissions.
- Invoke agents as nodes inside a deterministic workflow rather than letting them run an entire playbook end to end.
- Record the agent's rationale on each decision so analysts can audit and override, and feed overrides into #20.
