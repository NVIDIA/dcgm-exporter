# SECURITY

## Security

NVIDIA takes the security of our software products and services seriously, which includes all source code repositories managed through our GitHub organizations.

If you believe you have found a security vulnerability in any NVIDIA-owned repository that meets [NVIDIA's definition of a security vulnerability](https://www.nvidia.com/en-us/security/psirt-policies/), please report it to us as described below.

## Remote DCGM hostengine connections

The `sources.dcgm.hostengine` setting (`--remote-hostengine-info`) connects the
exporter to a standalone DCGM hostengine. DCGM's TCP protocol provides no
authentication, authorization, or encryption. Treat the hostengine as an
administrative interface and connect only to a trusted endpoint. See the
[DCGM transport security guidance](https://docs.nvidia.com/datacenter/dcgm/latest/reference/command-line-reference/nv-hostengine.html#transport-and-client-addresses).

- For same-host connections, keep a TCP listener on loopback or use a
  pathname-based Unix-domain socket. Restrict access to the socket and its
  containing directory to the service identities that need it; a local socket
  relies on filesystem permissions, not cryptographic client authentication.
- When TCP access between hosts is required, use a trusted management network
  and host/network firewall rules that allow only the required client hosts.
  Do not expose the hostengine port to public or untrusted networks. If the
  connection crosses an untrusted network, protect it with an authenticated,
  encrypted tunnel or VPN managed by the operator.

The exporter's `--web-config-file` TLS and basic-auth settings protect incoming
HTTP requests. They do not protect or authenticate the separate connection
from the exporter to the DCGM hostengine. Restrict that connection independently.

## Reporting Security Issues

**Please do not report security vulnerabilities through public GitHub issues.**

To report any security vulnerabilities, please contact the NVIDIA Product Security Incident Response Team (PSIRT) at:

1. [https://www.nvidia.com/en-us/security/report-vulnerability/](https://www.nvidia.com/en-us/security/report-vulnerability/).
2. If you prefer to submit without logging in, please email [psirt@nvidia.com](mailto:psirt@nvidia.com). If you report a potential vulnerability via email, please encrypt your communication using NVIDIA's public PGP key ([see PGP Key page](https://www.nvidia.com/en-us/security/pgp-key/)).
3. Alternatively, you can report a security issue through GitHub using the GitHub Security Advisories feature at [https://github.com/NVIDIA/dcgm-exporter/security/advisories/new](https://github.com/NVIDIA/dcgm-exporter/security/advisories/new).

Please include the requested information listed below (as much as you can provide) to help us better understand the nature and scope of the possible issue:

- Type of issue (e.g., buffer overflow, remote code execution, etc.)
- Permanent link of the source file(s) related to the manifestation of the issue
- Any special configuration required to reproduce the issue
- Step-by-step instructions to reproduce the issue
- Proof-of-concept or exploit code (if possible)
- Impact of the issue, including how an attacker might exploit the issue

This information will help us triage your report more quickly.

NVIDIA reserves the right to delete vulnerability reports until they're fixed.

## Preferred Languages

We prefer all communications to be in English.

## **Coordinated Vulnerability Disclosure**

NVIDIA strives to follow Coordinated Vulnerability Disclosure (CVD). CVD is a process by which independent reporters who discover a vulnerability in our product contact NVIDIA directly and allow us the opportunity to investigate and remediate the vulnerability before the reporter discloses the information to the public.

NVIDIA PSIRT will coordinate with the reporter throughout the vulnerability investigation and provide the reporter with updates on progress as appropriate. With the reporter's agreement, NVIDIA PSIRT may recognize the reporter on our Acknowledgement page for finding a valid product vulnerability and privately reporting the issue. After an update or mitigation information is publicly released by NVIDIA, the reporter is welcome to discuss the vulnerability publicly.

Following NVIDIA's CVD allows us to protect our customers while coordinating public disclosures and appropriately acknowledging the reporter(s) for their findings.

Occasionally NVIDIA will discover security vulnerabilities in products from other vendors. NVIDIA will follow its standard Coordinated Vulnerability Disclosure process and communicate the identified issue to the affected vendor or a third-party coordination center if this occurs.
