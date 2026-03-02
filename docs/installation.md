# Installation

> Part of [Forge Documentation](../README.md)

Forge can be installed via Homebrew, pre-built binary, or manual download on Windows.

## macOS (Homebrew)

```bash
brew install initializ/tap/forge
```

## Linux / macOS (Binary)

```bash
curl -sSL https://github.com/initializ/forge/releases/latest/download/forge-$(uname -s)-$(uname -m).tar.gz | tar xz
sudo mv forge /usr/local/bin/
```

## Windows

Download the latest `.zip` from [GitHub Releases](https://github.com/initializ/forge/releases/latest) and add to your PATH.

## Verify

```bash
forge --version
```

---
← [Quick Start](quickstart.md) | [Back to README](../README.md) | [Architecture](architecture.md) →
