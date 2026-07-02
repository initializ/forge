# OWASP ASI Conformance Report

Generated from the instrumented conformance suite (`tests/owasp-asi`). Grades come from `docs/security/owasp-asi-conformance.md`; pass/skip/fail and measured rates come from the test run. Skips are tracked backlog gaps, not build breakers.

| Entry | Title | Grade | Pass | xfail | Fail | Backlog | Measured |
|---|---|---|---|---|---|---|---|
| ASI01 | Agent Goal Hijack | Partial | 1 | 0 | 0 | #229 | rate: 1.00 (10/10); residual exfil-success rate: 0.00 |
| ASI02 | Tool Misuse & Exploitation | Enforced | 1 | 0 | 0 | #223,#231,#235 | rate: 1.00 (9/9 must-reject); known-gap cases executed (GAP-PATH) |
| ASI03 | Identity & Privilege Abuse | Partial | 1 | 1 | 0 | #232 | - |
| ASI04 | Agentic Supply Chain | Partial | 1 | 2 | 0 | #227,#228 | - |
| ASI05 | Unexpected Code Execution | Enforced | 1 | 0 | 0 | #234 | rate: 1.00 (8/8) |
| ASI06 | Memory & Context Poisoning | Partial | 0 | 2 | 0 | #225 | - |
| ASI07 | Insecure Inter-Agent Comms | Partial | 0 | 3 | 0 | #226 | - |
| ASI08 | Cascading Failures | Partial | 1 | 1 | 0 | #233 | denied 1 |
| ASI09 | Human-Agent Trust Exploitation | Partial | 0 | 1 | 0 | #223 | - |
| ASI10 | Rogue Agents | Partial | 1 | 2 | 0 | #224,#230 | - |

xfail = `t.Skip` naming a backlog issue; a green run has Fail=0 across all entries.
