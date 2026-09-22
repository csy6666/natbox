# Security Policy

## Supported Versions

Security fixes are developed against the `main` branch and the latest release.

## Reporting a Vulnerability

Please do not publish credentials, private keys, server addresses, or an exploit
in a public issue. Use GitHub's private vulnerability reporting for this
repository when available. Otherwise contact the repository owner privately
through GitHub and include a minimal reproduction, affected version, impact,
and any logs with secrets removed.

Natbox should normally listen on loopback behind an SSH tunnel or an
authenticated reverse proxy. Never commit `NATBOX_TOKEN`, administrator hashes,
SSH keys, `.env` files, SQLite databases, or production backups.
