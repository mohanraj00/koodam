# Security

## Supported versions

I fix security problems only in the latest release. The API is `v1alpha1`, so a fix can need an upgrade to the next minor version.

| Version | Supported |
| --- | --- |
| 0.4.x | Yes |
| 0.3.x and earlier | No |

## Report a problem

Use GitHub private vulnerability reporting: open the Security tab of this repository and select "Report a vulnerability". Do not open a public issue, a pull request or a discussion for a security problem.

Give these items in the report:

- the version of the chart, the controller image or the CLI;
- the steps that show the problem, with the smallest manifest that you can make;
- the effect, for example a key that a different namespace can read.

Do not put a real gateway key, master key, login file or token in the report. Use a test value.

## Response goal

- I answer the report within 7 days.
- I tell you if I accept the problem, and my plan for the fix.
- I publish a GitHub security advisory with the fix release. I name you in the advisory if you agree.

## The threat model

[docs/threat-model.md](docs/threat-model.md) gives the threats that Koodam controls, the control and the test for each threat, and the risks that stay. Read it before you install Koodam in a shared cluster.
