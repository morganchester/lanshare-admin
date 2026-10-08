# LanShare Admin

LanShare Admin is a small, self-hosted web console for managing Samba shares
and issuing a separate credential to each trusted device. Administrators can
choose which shares a device may access, rotate or revoke its credential,
publish server directories, and manage access to the console itself.

The application never stores plaintext Samba credentials. It displays each
generated credential once; Samba retains only the password hash.

## Features

- Dashboard with access statistics
- Separate **Users** and **Shares** pages
- Multiple Samba shares per device
- Read/write or read-only shares selected from an allowlisted directory tree
- Independent credentials for macOS, Windows, Linux, and other clients
- Credential rotation, revocation, and share reassignment
- Multiple console administrators stored in SQLite
- Argon2id password hashes and opaque, revocable sessions
- Password recovery through explicitly trusted browsers
- Emergency password reset from the server command line
- `ru-RU` and `en-US` localization catalogs
- An unprivileged web process with a narrow `sudo` helper boundary

## Architecture

The web service runs directly under systemd because the Samba users, Unix
groups, and Samba configuration belong to the host. Running this part in a
container would still require a privileged host agent or broad access to host
account files. The web process remains unprivileged and can invoke only the
validated helper operations listed in `deploy/lanshare-admin.sudoers`.

Runtime data is held in `/var/lib/lanshare-admin/lanshare.db`. The database
contains administrators, password hashes, sessions, trusted recovery devices,
and non-secret device metadata. Samba remains the source of truth for file
access accounts and their password hashes.

## Requirements

The server requires a Linux host with:

- Samba and `smbpasswd`
- systemd
- `sudo`
- firewalld if the installer should create a firewall rule

The build machine requires Go 1.26 or later, Node.js, and npm.

## Build

```sh
git clone <repository-url>
cd lanshare-admin
make test
make build VERSION=0.2.0
```

The three Linux amd64 artifacts appear in `dist/`:

- `lanshare-admin-linux-amd64` — web service;
- `lanshare-helper-linux-amd64` — privileged Samba helper;
- `lanshare-auth-linux-amd64` — emergency administrator utility.

## Configuration

Copy `config.example.json` to `config.json` and adapt it to the server.
`allowed_roots` limits the directory picker; the helper refuses to publish
paths outside these roots. `managed_share_file` is generated atomically and
should not be edited manually. A share with `managed` set to `false` can be
assigned to devices but cannot be deleted in the UI.

```json
{
  "listen_addr": "0.0.0.0:8097",
  "public_url": "http://lanshare.example.com:8097",
  "account_prefix": "lanshare-",
  "smb_server": "fileserver.example.com",
  "samba_config": "/etc/samba/smb.conf",
  "managed_share_file": "/etc/samba/lanshare-admin-shares.conf",
  "allowed_roots": ["/mnt", "/srv"],
  "shares": [
    {
      "id": "photos",
      "name": "Photo archive",
      "group": "photo-share",
      "unc": "\\\\fileserver.example.com\\Photos",
      "path": "/srv/photos",
      "read_only": false,
      "managed": false
    }
  ]
}
```

The host names above use the reserved `example.com` domain and must be
replaced. Set `listen_addr` to a specific interface address if the service
should not listen on every interface. Keep the console on a trusted network or
place it behind HTTPS or a VPN.

## Install

```sh
sudo ./deploy/install-server.sh \
  ./dist/lanshare-admin-linux-amd64 \
  ./dist/lanshare-helper-linux-amd64 \
  ./dist/lanshare-auth-linux-amd64 \
  ./config.json
```

The installer creates no firewall rule by default. To open the configured TCP
port with firewalld, pass the LAN network explicitly and replace the example
CIDR with the network used by the server:

```sh
sudo env LAN_CIDR=10.0.0.0/8 ./deploy/install-server.sh \
  ./dist/lanshare-admin-linux-amd64 \
  ./dist/lanshare-helper-linux-amd64 \
  ./dist/lanshare-auth-linux-amd64 \
  ./config.json
```

The installer prints the initial `admin` password once. Sign in and keep
**Trust this browser for password recovery** enabled. After that, **Forgot
password?** can set a new password in that browser even after logout. Recovery
trust uses a separate HttpOnly cookie and can be revoked from **Password and
recovery** on the dashboard.

For a lost password with no trusted browser, run on the server:

```sh
sudo -u lanshare-admin \
  /opt/lanshare-admin/lanshare-auth reset admin
```

The command generates and prints a new password. You may instead supply a
password as the final argument; it must contain at least 12 characters.

Create another administrator from the UI or from the server:

```sh
sudo -u lanshare-admin \
  /opt/lanshare-admin/lanshare-auth create second-admin
```

## Localization

All user-facing application strings live in `locales/` as flat YAML catalogs:

- `locales/ru-RU.yaml`
- `locales/en-US.yaml`

Use the same key in every catalog. `go test ./...` checks key parity. The
backend selects localized errors from `X-Locale` or `Accept-Language`; the
frontend loads `/api/locales/{locale}` and remembers the selection locally.

## Operations and backup

```sh
sudo systemctl status lanshare-admin
sudo journalctl -u lanshare-admin
curl http://127.0.0.1:8097/healthz
```

Back up `/etc/lanshare-admin`, `/var/lib/lanshare-admin`, the Samba
configuration, and Samba's password database. Treat `lanshare.db` as secret:
it contains active session and recovery-token hashes, even though passwords
cannot be recovered from it.

Upgrade by installing all binaries and restarting only this service:

```sh
sudo install -m 0755 dist/lanshare-admin-linux-amd64 /opt/lanshare-admin/lanshare-admin
sudo install -m 0755 dist/lanshare-helper-linux-amd64 /opt/lanshare-admin/lanshare-helper
sudo install -m 0755 dist/lanshare-auth-linux-amd64 /opt/lanshare-admin/lanshare-auth
sudo systemctl restart lanshare-admin
```

## Security notes

- Expose the console only on a trusted LAN or behind HTTPS or a VPN.
- Login attempts are rate-limited after five failures per source address.
- Mutation requests require a same-origin browser request.
- Session cookies and recovery cookies are HttpOnly and SameSite Strict.
- Password changes and administrator resets revoke existing sessions.
- The service emits CSP, no-store, frame-denial, and MIME-sniffing headers.
- Generated device credentials may remain in clipboard or terminal history;
  clear them after the OS credential store saves the value.

## Development

```sh
npm ci
npm run css
node --check web/app.js
go vet ./...
go test ./...
LANSHARE_CONFIG=./config.example.json \
DATABASE_PATH=/tmp/lanshare-admin.db \
DEVICE_HELPER=/path/to/test-helper \
go run ./cmd/server
```

The example helper path is only a placeholder. Operations that manage devices
or shares require a compatible helper process and the necessary host
permissions; read-only UI and authentication work can be developed separately.

The frontend is framework-free. HTML, JavaScript, compiled Tailwind CSS, and
the YAML localization catalogs are embedded into the web binary.

## License

LanShare Admin is free for personal and noncommercial use under the
[PolyForm Noncommercial License 1.0.0](LICENSE.md).

Selling the software or a solution that incorporates it, providing paid
hosting or SaaS, OEM distribution, and inclusion in a commercial product or
paid customer deployment require a separate written commercial license. See
[Commercial licensing](COMMERCIAL-LICENSING.md) for details.

This is a source-available license, not an OSI-approved open-source license.
