#!/usr/bin/env bash
set -euo pipefail
umask 0077

if [[ ${EUID} -ne 0 || $# -ne 4 ]]; then
    echo "Usage: sudo $0 <server-binary> <helper-binary> <auth-binary> <config.json>" >&2
    exit 2
fi

server_binary=$(realpath "$1")
helper_binary=$(realpath "$2")
auth_binary=$(realpath "$3")
config_file=$(realpath "$4")
deploy_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

getent passwd lanshare-admin >/dev/null || useradd --system --home-dir /var/lib/lanshare-admin --shell /sbin/nologin lanshare-admin
install -d -o root -g root -m 0755 /opt/lanshare-admin
install -d -o root -g lanshare-admin -m 0750 /etc/lanshare-admin
install -d -o lanshare-admin -g lanshare-admin -m 0700 /var/lib/lanshare-admin
install -m 0755 "$server_binary" /opt/lanshare-admin/lanshare-admin
install -m 0755 "$helper_binary" /opt/lanshare-admin/lanshare-helper
install -m 0755 "$auth_binary" /opt/lanshare-admin/lanshare-auth
install -o root -g lanshare-admin -m 0640 "$config_file" /etc/lanshare-admin/config.json

managed_share_file=$(sed -nE 's/.*"managed_share_file"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' "$config_file")
managed_share_file=${managed_share_file:-/etc/samba/lanshare-admin-shares.conf}
if [[ ! -e "$managed_share_file" ]]; then
    install -o root -g root -m 0644 /dev/null "$managed_share_file"
fi
if ! grep -Fq "include = $managed_share_file" /etc/samba/smb.conf; then
    cp -p /etc/samba/smb.conf /etc/samba/smb.conf.pre-lanshare-admin
    printf '\ninclude = %s\n' "$managed_share_file" >> /etc/samba/smb.conf
fi
testparm -s /etc/samba/smb.conf >/dev/null

visudo -cf "$deploy_dir/lanshare-admin.sudoers"
install -o root -g root -m 0440 "$deploy_dir/lanshare-admin.sudoers" /etc/sudoers.d/lanshare-admin
install -o root -g root -m 0644 "$deploy_dir/lanshare-admin.service" /etc/systemd/system/lanshare-admin.service

systemctl daemon-reload
admin_count=$(runuser -u lanshare-admin -- /opt/lanshare-admin/lanshare-auth count)
if [[ "$admin_count" == 0 ]]; then
    echo "Initial administrator credentials (shown once):"
    runuser -u lanshare-admin -- /opt/lanshare-admin/lanshare-auth create admin
fi
systemctl enable --now lanshare-admin

listen_port=$(sed -nE 's/.*"listen_addr"[[:space:]]*:[[:space:]]*"[^"]*:([0-9]+)".*/\1/p' "$config_file")
if [[ -n "$listen_port" && -n "${LAN_CIDR:-}" ]] && command -v firewall-cmd >/dev/null; then
    lan_cidr=$LAN_CIDR
    rule="rule family=\"ipv4\" source address=\"$lan_cidr\" port port=\"$listen_port\" protocol=\"tcp\" accept"
    firewall-cmd --permanent --zone=public --query-rich-rule="$rule" >/dev/null || firewall-cmd --permanent --zone=public --add-rich-rule="$rule"
    firewall-cmd --reload
fi

echo "LanShare Admin installed."
