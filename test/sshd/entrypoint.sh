#!/usr/bin/env bash
# Each container gets a fresh host key and the key the test generated.
set -euo pipefail

ssh-keygen -q -t ed25519 -N '' -f /etc/ssh/ssh_host_ed25519_key
install -d -m 700 -o burro -g burro /home/burro/.ssh
printf '%s\n' "$AUTHORIZED_KEY" >/home/burro/.ssh/authorized_keys
chown burro:burro /home/burro/.ssh/authorized_keys
chmod 600 /home/burro/.ssh/authorized_keys
exec /usr/sbin/sshd -D -e
