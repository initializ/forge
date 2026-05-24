#!/usr/bin/env python3
"""
forge-aws-sign — reference client for Forge's aws_sigv4 auth provider.

Why this script exists
======================
Forge's aws_sigv4 provider authenticates callers by reflecting their Sigv4
signature to AWS STS GetCallerIdentity. The signature is bound to the
*destination host* in its canonical-headers input, so callers cannot just
sign for Forge's URL and expect STS to validate the result.

The correct client-side pattern is:

  1. Construct a hypothetical request shape addressed to STS itself
     (POST https://sts.<region>.amazonaws.com/, body
     "Action=GetCallerIdentity&Version=2011-06-15").
  2. Sign that hypothetical request with the caller's AWS credentials.
  3. Take the resulting Authorization + X-Amz-Date (+ X-Amz-Security-Token
     for temporary credentials) headers and attach them to the REAL request
     that's being sent to Forge.
  4. Forge forwards those headers verbatim to STS, which validates them
     against its own host. Forge stamps an Identity from the returned ARN.

This is identical in spirit to the aws-iam-authenticator pattern that EKS
uses. Standard tools (awscurl, the AWS CLI, `boto3.client('sts')`) do NOT
do this automatically — they always sign for the URL you're calling. So a
small wrapper like this one is required.

Usage
=====
  python3 forge-aws-sign.py [--region us-east-1] [--url URL] [--body BODY]

  Defaults:
    --region:  us-east-1
    --url:     http://localhost:9999/tasks/send
    --body:    {"task":"hello"}

Reads AWS credentials the same way boto3 does: env vars, profile, SSO,
IRSA, instance profile, etc. — whichever applies.

Exits 0 on HTTP 2xx, 1 otherwise. Prints the response body either way.
"""
from __future__ import annotations

import argparse
import sys

try:
    import requests
    from botocore.session import Session
    from botocore.auth import SigV4Auth
    from botocore.awsrequest import AWSRequest
except ImportError as e:
    print(f"missing dependency: {e}", file=sys.stderr)
    print("install with: pip3 install --user boto3 requests", file=sys.stderr)
    sys.exit(2)


def main() -> int:
    parser = argparse.ArgumentParser(description="Forge aws_sigv4 reference client")
    parser.add_argument("--region", default="us-east-1", help="AWS region used in the Sigv4 scope")
    parser.add_argument("--url", default="http://localhost:9999/tasks/send", help="Forge endpoint to POST to")
    parser.add_argument("--body", default='{"task":"hello"}', help="JSON body to send to Forge")
    parser.add_argument("--profile", default=None, help="AWS profile to use (default: boto3's default)")
    parser.add_argument("-v", "--verbose", action="store_true", help="Show signed headers in output")
    args = parser.parse_args()

    session = Session(profile=args.profile) if args.profile else Session()
    credentials = session.get_credentials()
    if credentials is None:
        print("No AWS credentials found. Try `aws sso login` or `aws configure`.", file=sys.stderr)
        return 2
    frozen = credentials.get_frozen_credentials()

    # 1-2. Sign for STS's host (NOT Forge's host)
    sts_url = f"https://sts.{args.region}.amazonaws.com/"
    sts_body = "Action=GetCallerIdentity&Version=2011-06-15"
    sts_req = AWSRequest(
        method="POST",
        url=sts_url,
        data=sts_body,
        headers={"Content-Type": "application/x-www-form-urlencoded"},
    )
    SigV4Auth(frozen, "sts", args.region).add_auth(sts_req)

    # 3. Reuse the signed headers in a request to Forge
    forge_headers = {
        "Authorization": sts_req.headers["Authorization"],
        "X-Amz-Date": sts_req.headers["X-Amz-Date"],
        "Content-Type": "application/json",
    }
    if "X-Amz-Security-Token" in sts_req.headers:
        forge_headers["X-Amz-Security-Token"] = sts_req.headers["X-Amz-Security-Token"]

    if args.verbose:
        print(f"POST {args.url}")
        print(f"  Authorization:        {forge_headers['Authorization'][:80]}...")
        print(f"  X-Amz-Date:           {forge_headers['X-Amz-Date']}")
        has_tok = "X-Amz-Security-Token" in forge_headers
        print(f"  X-Amz-Security-Token: {'<present>' if has_tok else '<absent>'}")
        print()

    # 4. Send. Forge will reflect the headers to STS.
    resp = requests.post(args.url, headers=forge_headers, data=args.body)
    print(f"HTTP {resp.status_code}")
    print(resp.text)
    return 0 if 200 <= resp.status_code < 300 else 1


if __name__ == "__main__":
    sys.exit(main())
