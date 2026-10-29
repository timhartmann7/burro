FROM debian:stable-slim@sha256:13e0a2e78aa0554ad5ff66e4e260c6dbcdff807eb1f39d43b9fb0288aae88b04

RUN apt-get update \
	&& apt-get install --yes --no-install-recommends \
		bash docker-cli docker-compose iproute2 openssh-server rsync \
	&& rm -rf /var/lib/apt/lists/* /etc/ssh/ssh_host_* \
	&& useradd --create-home --shell /bin/bash burro \
	&& usermod --password '*' burro \
	&& mkdir -p /run/sshd

COPY sshd_config /etc/ssh/sshd_config.d/burro.conf
COPY entrypoint.sh /usr/local/bin/entrypoint.sh

EXPOSE 22
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
