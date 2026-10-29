#!/usr/bin/env bash
# Each container gets a fresh host key and the key the test generated.
set -euo pipefail

ssh-keygen -q -t ed25519 -N '' -f /etc/ssh/ssh_host_ed25519_key
install -d -m 700 -o burro -g burro /home/burro/.ssh
printf '%s\n' "$AUTHORIZED_KEY" >/home/burro/.ssh/authorized_keys
chown burro:burro /home/burro/.ssh/authorized_keys
chmod 600 /home/burro/.ssh/authorized_keys

# Docker tests mount the host's socket; the login user joins the group that
# owns it, whatever its id is on this host.
if [ -S /var/run/docker.sock ]; then
	gid=$(stat -c %g /var/run/docker.sock)
	group=$(getent group "$gid" | cut -d: -f1 || true)
	if [ -z "$group" ]; then
		group=hostdocker
		groupadd -g "$gid" "$group"
	fi
	usermod -aG "$group" burro
fi
exec /usr/sbin/sshd -D -e
