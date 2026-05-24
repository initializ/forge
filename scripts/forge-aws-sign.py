#!/usr/bin/env python3
"""
forge-aws-sign — reference client for Forge's aws_sigv4 auth provider.

The aws_sigv4 provider uses the pre-signed URL pattern (same approach as
aws-iam-authenticator for EKS). The client mints a pre-signed STS
GetCallerIdentity URL using its own AWS SDK, then sends it to Forge as
a Bearer token of the form:

    Authorization: Bearer forge-aws-v1.<base64url-of-presigned-sts-url>

Forge invokes the pre-signed URL on STS, which validates the signature
against its own host (because that's what was signed), and returns the
caller's canonical ARN.

Usage
=====
  # Print just the token (use it however you want):
  python3 forge-aws-sign.py --token-only --region us-east-1

  # Make a one-shot call to Forge:
  python3 forge-aws-sign.py --region us-east-1 \
                            --url http://localhost:9999/tasks/send \
                            --body '{"task":"hello"}'

Reads AWS credentials the same way boto3 does: env vars, profile, SSO,
IRSA, instance profile, etc.

Exits 0 on HTTP 2xx (or when --token-only succeeds); 1 otherwise.
"""
from __future__ import annotations

import argparse
import base64
import sys

try:
    import boto3
    import requests
except ImportError as e:
    print(f"missing dependency: {e}", file=sys.stderr)
    print("install with: pip3 install --user boto3 requests", file=sys.stderr)
    sys.exit(2)


def mint_token(region: str, profile: str | None, expires: int = 900) -> str:
    """Mint a forge-aws-v1 token from the current AWS credentials.

    `expires` (seconds) is the TTL baked into the pre-signed URL; max 900
    per STS limits. Forge accepts any valid TTL — the token simply becomes
    unusable once STS rejects the expired signature.
    """
    session = boto3.Session(profile_name=profile) if profile else boto3.Session()
    sts = session.client("sts", region_name=region)
    url = sts.generate_presigned_url("get_caller_identity", ExpiresIn=expires)
    encoded = base64.urlsafe_b64encode(url.encode()).rstrip(b"=").decode()
    return "forge-aws-v1." + encoded


def main() -> int:
    parser = argparse.ArgumentParser(description="Forge aws_sigv4 reference client")
    parser.add_argument("--region", default="us-east-1", help="AWS region used in the Sigv4 scope")
    parser.add_argument("--url", default="http://localhost:9999/tasks/send", help="Forge endpoint to POST to")
    parser.add_argument("--body", default='{"task":"hello"}', help="JSON body to send to Forge")
    parser.add_argument("--profile", default=None, help="AWS profile (default: boto3's default chain)")
    parser.add_argument("--expires", type=int, default=900, help="Pre-signed URL TTL in seconds (max 900)")
    parser.add_argument("--token-only", action="store_true", help="Print only the token, don't make a request")
    parser.add_argument("-v", "--verbose", action="store_true", help="Verbose output")
    args = parser.parse_args()

    try:
        token = mint_token(args.region, args.profile, args.expires)
    except Exception as e:
        print(f"failed to mint token: {e}", file=sys.stderr)
        return 1

    if args.token_only:
        print(token)
        return 0

    if args.verbose:
        print(f"POST {args.url}", file=sys.stderr)
        print(f"  Authorization: Bearer {token[:60]}...", file=sys.stderr)

    resp = requests.post(
        args.url,
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
        data=args.body,
    )
    print(f"HTTP {resp.status_code}")
    print(resp.text)
    return 0 if 200 <= resp.status_code < 300 else 1


if __name__ == "__main__":
    sys.exit(main())
