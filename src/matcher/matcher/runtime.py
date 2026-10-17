"""Explicit Kafka configuration for local Kafka and Azure Event Hubs."""
import os
from pathlib import Path

from .contract import loads, require


def kafka_config(environ=None, *, prefix="MATCHER"):
    env = os.environ if environ is None else environ
    local = env.get(prefix + "_LOCAL_TEST") == "1"
    if env.get(prefix + "_KAFKA_CONFIG_FILE"):
        config = loads(Path(env[prefix + "_KAFKA_CONFIG_FILE"]).read_bytes())
    else:
        # Names accepted by the supplied Azure IaC/CD service configuration.
        brokers = env.get(prefix + "_KAFKA_BOOTSTRAP_SERVERS", env.get("KAFKA_BOOTSTRAP_SERVERS", ""))
        connection = env.get(prefix + "_KAFKA_CONNECTION_STRING", env.get("KAFKA_CONNECTION_STRING", ""))
        require(bool(brokers))
        if connection:
            config = {"bootstrap.servers": brokers, "security.protocol": "SASL_SSL",
                      "sasl.mechanism": "PLAIN", "sasl.username": "$ConnectionString",
                      "sasl.password": connection}
        else:
            require(local and ".servicebus.windows.net" not in brokers)
            config = {"bootstrap.servers": brokers, "security.protocol": "PLAINTEXT"}
    require(isinstance(config, dict) and bool(config.get("bootstrap.servers")))
    require(config.get("security.protocol") in (("PLAINTEXT", "SSL", "SASL_SSL") if local else ("SSL", "SASL_SSL")))
    if ".servicebus.windows.net" in config["bootstrap.servers"]:
        require(config.get("security.protocol") == "SASL_SSL")
        config.setdefault("socket.keepalive.enable", True)
        config.setdefault("metadata.max.age.ms", 180000)
        config.setdefault("request.timeout.ms", 60000)
    return config


def token_file(environ=None):
    env = os.environ if environ is None else environ
    if env.get("MATCHER_TOKEN_FILE"):
        return env["MATCHER_TOKEN_FILE"]
    value = env.get("MATCHER_BEARER_TOKEN", "")
    require(bool(value) and all(33 <= ord(c) <= 126 for c in value))
    # Container-private, owner-only file. Never print the token or put it in argv.
    import tempfile
    descriptor, name = tempfile.mkstemp(prefix="matcher-token-")
    with os.fdopen(descriptor, "w") as target:
        target.write(value)
    import atexit
    atexit.register(lambda: Path(name).unlink(missing_ok=True))
    return name


def token_provider(environ=None):
    """Dedicated workload key mode; automatically refresh a five-minute JWT.

    This key authenticates only the matcher identity. It cannot authorise an
    explicit rerun, which requires the API's separate operator signing key.
    """
    env = os.environ if environ is None else environ
    secret_file = env.get("MATCHER_SIGNING_SECRET_FILE")
    secret_value = env.get("MATCHER_SIGNING_SECRET")
    if not secret_file and not secret_value:
        return None
    issuer, audience = env.get("MATCHER_TOKEN_ISSUER"), env.get("MATCHER_TOKEN_AUDIENCE")
    require(bool(issuer) and bool(audience))
    import base64
    import hashlib
    import hmac
    import time
    from .contract import canonical
    def provide():
        secret = Path(secret_file).read_text().strip() if secret_file else secret_value
        require(len(secret) >= 32)
        def encode(value):
            return base64.urlsafe_b64encode(canonical(value)).rstrip(b"=")
        now = int(time.time())
        header = encode({"alg": "HS256", "typ": "JWT", "kid": "worker"})
        payload = encode({"iss": issuer, "aud": audience, "sub": "matching-worker", "iat": now,
                          "exp": now + 300, "scope": "matching.execute matching.read"})
        unsigned = header + b"." + payload
        signature = hmac.new(secret.encode(), unsigned, hashlib.sha256).digest()
        return (unsigned + b"." + base64.urlsafe_b64encode(signature).rstrip(b"=")).decode()
    provide()  # Fail configuration validation before starting the consumer.
    return provide
