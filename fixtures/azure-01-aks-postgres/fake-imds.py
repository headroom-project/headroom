# A stub Azure instance metadata token endpoint, so the azurerm provider can
# plan with no subscription.
#
# Unlike the AWS provider, azurerm has no skip_credentials_validation: it
# acquires a token at configure time and reads the tenant out of the token's
# claims before it will plan anything. Nothing in these fixtures ever calls the
# ARM API afterwards (no data sources, every resource is a create), so the
# token only has to parse, not authenticate.
#
#   python fake-imds.py &            # listens on 127.0.0.1:47712
#   terraform plan -out=tfplan
#
# The provider is pointed at it with use_msi = true and msi_endpoint.

import base64
import json
import time
from http.server import BaseHTTPRequestHandler, HTTPServer

PORT = 47712
TENANT = "11111111-1111-1111-1111-111111111111"
CLIENT = "22222222-2222-2222-2222-222222222222"


def b64(obj):
    raw = json.dumps(obj, separators=(",", ":")).encode()
    return base64.urlsafe_b64encode(raw).rstrip(b"=").decode()


def token():
    now = int(time.time())
    header = {"typ": "JWT", "alg": "none"}
    claims = {
        "aud": "https://management.azure.com/",
        "iss": "https://sts.windows.net/%s/" % TENANT,
        "tid": TENANT,
        "appid": CLIENT,
        "oid": "33333333-3333-3333-3333-333333333333",
        "sub": "33333333-3333-3333-3333-333333333333",
        "iat": now,
        "nbf": now,
        "exp": now + 86400,
    }
    return "%s.%s.%s" % (b64(header), b64(claims), "c3R1Yg")


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps(
            {
                "access_token": token(),
                "client_id": CLIENT,
                "expires_in": "86400",
                "expires_on": str(int(time.time()) + 86400),
                "ext_expires_in": "86400",
                "not_before": str(int(time.time())),
                "resource": "https://management.azure.com/",
                "token_type": "Bearer",
            }
        ).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


if __name__ == "__main__":
    HTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
