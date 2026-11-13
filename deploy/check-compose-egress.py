"""Validate normalized `docker compose config --format json` without dependencies."""
import json
import sys

config = json.load(sys.stdin)
services = config["services"]
assert set(services) == {"bot", "worker", "egress"}
assert config["networks"]["app"]["internal"] is True
for network in config["networks"].values():
    assert network.get("enable_ipv6") is False
for name, service in services.items():
    assert not service.get("ports") and service.get("network_mode") != "host"
    assert not service.get("privileged", False)
    assert service["read_only"] and service["cap_drop"] == ["ALL"]
    assert service["dns"] == ["127.0.0.1"]
    expected = {"app", "outbound"} if name == "egress" else {"app"}
    assert set(service["networks"]) == expected
    if name != "egress":
        env = service["environment"]
        for key in ["HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy", "EGRESS_PROXY_URL"]:
            assert env[key] == "http://egress:3128"
        assert env["STRICT_EGRESS"] == "true"
        assert env["NO_PROXY"] == env["no_proxy"] == "worker,localhost,127.0.0.1,::1"
    for mount in service["volumes"]:
        assert mount["target"] not in {"/var/run/docker.sock", "/run/secrets"}
        if name != "egress":
            assert not mount["source"].endswith("/proxy.json")
egress = services["egress"]
assert set(egress["environment"]) == {"PROXY_CONFIG_FILE"}
assert len(egress["volumes"]) == 1
assert egress["volumes"][0]["target"] == "/run/proxy/proxy.json"
assert egress["volumes"][0]["read_only"]
assert services["bot"]["image"] == egress["image"]
print("Strict Compose egress topology and secret-mount checks passed")
