# Deployment

Runs nicedroid as a Podman container behind a reverse proxy, with a SQLite file
that survives redeploys. Written for a Debian host with rootful Podman, but the
shape is not Debian-specific.

The published instance is <https://nicedroid.linota.cn> (Caddy → `127.0.0.1:8020`).

## Why a scratch image

`modernc.org/sqlite` is pure Go, so the binary is static and needs no libc. The
image therefore contains the executable, a CA bundle for outbound TLS, and
tzdata — nothing else. No shell, no package manager, and no base image to pull,
which also means the build works on a host with no registry access. 16.5 MB.

## Files

| File | Purpose |
| --- | --- |
| `Containerfile` | scratch image; context needs `nicedroid`, `ca-certificates.crt`, `zoneinfo/` |
| `nicedroid.service` | systemd unit (`podman generate systemd --new` form) |

## Deploying

Cross-compile on the dev machine, since the target needs no toolchain:

```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath -ldflags="-s -w" -o nicedroid ./cmd/server
```

Stage the build context on the host and build:

```bash
install -d /root/work/nicedroid/{data,context}
cp nicedroid Containerfile /root/work/nicedroid/context/
cp /etc/ssl/certs/ca-certificates.crt /usr/share/zoneinfo -r /root/work/nicedroid/context/
mv /root/work/nicedroid/context/Containerfile /root/work/nicedroid/
podman build -f /root/work/nicedroid/Containerfile -t localhost/nicedroid:latest /root/work/nicedroid/context
```

Credentials live in an env file, not in the unit or the image:

```bash
cat > /root/work/nicedroid/nicedroid.env <<'EOF'
NICEDROID_ADMIN_USER=admin
NICEDROID_ADMIN_PASSWORD=change-me
TZ=Asia/Shanghai
EOF
chmod 600 /root/work/nicedroid/nicedroid.env
```

Install and start the service:

```bash
cp deploy/nicedroid.service /etc/systemd/system/container-nicedroid.service
systemctl daemon-reload
systemctl enable --now container-nicedroid
```

The container publishes only to loopback; the reverse proxy is the public face.

### Redeploying

```bash
scp nicedroid host:/root/work/nicedroid/context/
ssh host 'podman build -q -f /root/work/nicedroid/Containerfile -t localhost/nicedroid:latest /root/work/nicedroid/context && systemctl restart container-nicedroid'
```

The unit uses the `--new` form, so it runs a fresh container from the new image
each start — there is no stale container to remove first.

## Reverse proxy (Caddy)

```
nicedroid.linota.cn {
	bind 203.0.113.10
	encode gzip
	reverse_proxy 127.0.0.1:8020
}
```

`bind` pins the listener to the public address on a host that also has a
Tailscale interface, matching how the other sites on that box are configured.
Caddy provisions and renews the certificate automatically; no other TLS setup is
needed. Validate before reloading, so a typo does not take down the other sites
sharing the file:

```bash
caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile && systemctl reload caddy
```

## Notes

- **Back up `data/nicedroid.db`.** It holds the upstream API keys in plaintext
  and is the only stateful thing here.
- **Keep the proxy behind client keys.** `/v1/*` is default-deny, so a
  deployment with no client key answers 503 rather than serving the upstream
  quota openly. That is deliberate — see AGENTS.md.
- **The admin console is public.** It authenticates with its own credentials,
  but nothing else rate-limits it; if that matters, add proxy-level protection
  in the reverse proxy rather than in the app.
