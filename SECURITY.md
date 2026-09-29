# Security Policy

Hookyard sits between your services and third-party APIs, and it often holds vendor credentials and
request payloads. We take security reports seriously and appreciate responsible disclosure.

## Supported versions

Hookyard is in early development. Until `v1.0`, security fixes are released only for the **latest
minor version**.

| Version | Supported |
| --- | --- |
| latest `0.x` | ✅ |
| older `0.x` | ❌ |

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues, discussions or pull
requests.**

Report them privately through
[GitHub private vulnerability reporting](https://github.com/tanvir001728/hookyard/security/advisories/new).
If you can't use that, email **tanvir001728@gmail.com** with the subject `[hookyard security]`.

Please include as much of the following as you can:

- The type of issue (for example SSRF, credential exposure, authentication bypass, injection)
- The affected version or commit, and the relevant configuration
- Step-by-step instructions to reproduce the issue
- The impact of the issue, including how an attacker might exploit it

## What to expect

- **Acknowledgement** within 3 business days.
- **Initial assessment** within 7 business days, including whether we accept the report and its
  severity.
- **Fix and disclosure:** we will agree on a disclosure timeline with you, publish a GitHub Security
  Advisory once a fix is released, and credit you unless you prefer to stay anonymous.

## Scope notes for deployers

Hookyard sends HTTP requests to destinations configured by operators. When you deploy it:

- Keep the API and dashboard on a private network, or behind authentication, and never expose them
  publicly without a token.
- Store vendor credentials in environment variables or secret files, never in version control.
- Only configure upstreams you trust. Hookyard forwards requests to exactly the hosts you define.
