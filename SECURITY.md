# Security policy

Only the latest minor release receives fixes.

To report a vulnerability, use GitHub's private vulnerability reporting on this
repository (Security → Report a vulnerability) or email the maintainer listed on the
GitHub profile. Please do not open a public issue. Expect an acknowledgement within a
week.

Tenant isolation bugs (one tenant reading or writing another's rows) are the highest
severity. Include the schema, roles and the exact sequence of calls that reproduce it.
The security model is described in [`docs/security.md`](docs/security.md).
