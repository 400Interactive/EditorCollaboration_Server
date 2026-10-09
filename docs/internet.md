# Access from the Internet

On a local network, `ws://<ip>:7878/ws` is enough, and a password is required: the server refuses to start without
`COLLAB_PASSWORD` unless it listens only on loopback (`COLLAB_HOST=127.0.0.1`). As soon as collaborators connect
from home, pick one of the options below. With `ws://` over the Internet, the password and all traffic travel in
clear text: the server prints a warning at startup when a password is set without TLS. Prefer `COLLAB_HOST=127.0.0.1`
plus option B.

| Option | For | Server Url |
| --- | --- | --- |
| A. VPN (Tailscale, ZeroTier) | Small team, no domain name, nothing to open | `ws://<vpn-ip>:7878/ws` |
| B. HTTPS reverse proxy (Caddy) | A server with a domain name | `wss://collab.example.com/ws` |
| C. Direct certificate | You already have a certificate | `wss://collab.example.com:7878/ws` |

## A. VPN (the simplest)

Install [Tailscale](https://tailscale.com) (or ZeroTier) on the server and on every collaborator's PC, in the same
network. The VPN encrypts the traffic and no port is opened to the Internet.
Server address: `tailscale ip -4` on the server (for example `100.64.12.3`), so
**Server Url** = `ws://100.64.12.3:7878/ws`.

## B. Reverse proxy with automatic HTTPS (recommended for a server)

You need a domain name pointing at the machine (DNS `A` record), and ports 80 and 443 open (forwarded by the
router if the machine is at home). [Caddy](https://caddyserver.com) gets and renews the Let's Encrypt certificate
by itself.

1. Install Caddy (`sudo apt install caddy` on Debian/Ubuntu, see their website for other systems).
2. Make the server listen locally only, so that it can only be reached through Caddy: `COLLAB_HOST=127.0.0.1`
   (in `/etc/editor-collab.env` with systemd), or `"127.0.0.1:7878:7878"` in `docker-compose.yml`.
3. `/etc/caddy/Caddyfile` (template: `deploy/Caddyfile.example`):

   ```
   collab.example.com {
   	reverse_proxy 127.0.0.1:7878
   }
   ```

4. `sudo systemctl reload caddy`, then check `https://collab.example.com/health` in a browser.

**Server Url** = `wss://collab.example.com/ws`. Nginx or Traefik work too, as long as they forward WebSockets
(`Upgrade` and `Connection` headers).

## C. Certificate handled by the server itself

If you already have a certificate and its key (PEM format):

```sh
collab-server -data /path/to/data -tls-cert /path/to/fullchain.pem -tls-key /path/to/privkey.pem
```

The server then speaks `wss://` directly on its port. It must be restarted when the certificate is renewed.

## Forwarding a port on the router (B and C, server at home)

In the router's admin page, forward the TCP ports (443 and 80 for Caddy, or 7878 for option C) to the server's
local address, and give it a fixed local address (DHCP reservation). Never expose port 7878 over `ws://` without
a password.
