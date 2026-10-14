---
description: >-
  Reach the collaboration server from outside the local network with a VPN,
  Caddy, or a certificate on the server.
---

# Internet

On a local network, `ws://<ip>:7878/ws` is enough. As soon as people connect from home, pick one of the options below. With plain `ws://` on the Internet, the password and every message travel in clear text. The server prints a warning at startup when a password is set and TLS is not.

| Option                         | Use it when                                          | Server URL                         |
| ------------------------------ | ---------------------------------------------------- | ---------------------------------- |
| A. VPN (Tailscale, ZeroTier)   | Small team, no domain, nothing to open on the router | `ws://<vpn-ip>:7878/ws`            |
| B. HTTPS reverse proxy (Caddy) | A server with a domain name                          | `wss://collab.example.com/ws`      |
| C. Certificate on the server   | You already have a PEM certificate                   | `wss://collab.example.com:7878/ws` |

## A. VPN

Install [Tailscale](https://tailscale.com) or ZeroTier on the server and on every collaborator's PC, in the same network. The VPN encrypts the traffic, and no port is opened to the Internet.

On the server, `tailscale ip -4` prints the address, for example `100.64.12.3`. **Server URL** is `ws://100.64.12.3:7878/ws`.

## B. Reverse proxy

You need a domain name pointing at the machine (a DNS `A` record) and ports 80 and 443 open. If the machine is at home, the router forwards those ports. [Caddy](https://caddyserver.com) obtains and renews the Let's Encrypt certificate.

{% stepper %}
{% step %}
On Debian or Ubuntu: `sudo apt install caddy`. Other systems are covered on the Caddy site.
{% endstep %}

{% step %}
The collaboration server should be reachable only through Caddy. Set `COLLAB_HOST=127.0.0.1` in `/etc/editor-collab.env` when you use systemd, or `"127.0.0.1:7878:7878"` in `docker-compose.yml`.
{% endstep %}

{% step %}
`/etc/caddy/Caddyfile`:

```
collab.example.com {
    reverse_proxy 127.0.0.1:7878
}
```

Then `sudo systemctl reload caddy`, and open `https://collab.example.com/health` in a browser.
{% endstep %}
{% endstepper %}

**Server URL** is `wss://collab.example.com/ws`. Nginx or Traefik work too, as long as they forward WebSockets (`Upgrade` and `Connection` headers).

## C. Certificate on the server

If you already have a certificate and its key, PEM format:

```sh
collab-server -data /path/to/data -tls-cert /path/to/fullchain.pem -tls-key /path/to/privkey.pem
```

The server then speaks `wss://` on its own port. Restart it when the certificate is renewed.

## Port forwarding at home

For B and C, when the server is on a home connection: in the router admin page, forward TCP 443 and 80 (Caddy) or TCP 7878 (option C) to the server's local address. Give that machine a fixed local address with a DHCP reservation.

{% hint style="danger" %}
Do not expose port 7878 as plain `ws://` without a password.
{% endhint %}
