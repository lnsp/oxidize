# oxidize — build, embed the Console UI, and release to the VM.
#
# Local:
#   make ui            build console/dist and copy into the Go embed dir
#   make build         build the local binary (bin/oxidize)
#   make run           build + run locally (needs PROXMOX_HOST + TOKEN)
#
# Release (to the Tailscale-reachable VM):
#   make deploy        cross-compile + ship binary + restart service
#   make release       rebuild UI, then deploy (full release)
#   make provision     first-time install of the systemd service (idempotent)
#
# Connects over Tailscale SSH as your tailnet user (root login is disabled), so
# files are staged in /tmp and privileged steps run via sudo. Override as needed:
#   make deploy DEPLOY_HOST=root@192.168.1.116 SSH_OPTS="-i ~/.ssh/key" SUDO=
#
# DEPLOY_HOST  ssh destination (user@host). Default: the VM's cloud-init user.
# SUDO         command prefix for privileged steps ("sudo" by default; set empty
#              when deploying as root).

DEPLOY_HOST ?= lennart@oxidize.taild4f4.ts.net
SSH_OPTS    ?=
SUDO        ?= sudo
SSH         := ssh $(SSH_OPTS)
SCP         := scp $(SSH_OPTS)

.PHONY: ui build build-linux run deploy release provision caddy clean

# Build the console (API_MODE=nexus => relative /v1 calls, no mock service
# worker) and copy the artifact into the Go embed location.
ui:
	cd console && npm ci && API_MODE=nexus npm run build
	rm -rf internal/static/dist
	cp -r console/dist internal/static/dist

build:
	go build -o bin/oxidize ./cmd/oxidize

build-linux:
	GOOS=linux GOARCH=amd64 go build -o bin/oxidize-linux-amd64 ./cmd/oxidize

run: build
	./bin/oxidize

# Update an already-provisioned VM: ship the binary atomically and restart.
deploy: build-linux
	$(SCP) bin/oxidize-linux-amd64 $(DEPLOY_HOST):/tmp/oxidize.upload
	$(SSH) $(DEPLOY_HOST) '$(SUDO) install -m755 /tmp/oxidize.upload /usr/local/bin/oxidize && rm -f /tmp/oxidize.upload && $(SUDO) systemctl restart oxidize && $(SUDO) systemctl --no-pager is-active oxidize'

# Full release: rebuild the UI, embed it, then deploy.
release: ui deploy

# First-time install of the systemd service. Idempotent: it never overwrites an
# existing /etc/oxidize/oxidize.env, so secrets are preserved across runs. After
# the first provision, fill in /etc/oxidize/oxidize.env and upload the Proxmox
# token to /etc/oxidize/TOKEN, then `make deploy`.
provision: build-linux
	$(SCP) bin/oxidize-linux-amd64 deploy/oxidize.service deploy/oxidize.env.example $(DEPLOY_HOST):/tmp/
	$(SSH) $(DEPLOY_HOST) '$(SUDO) install -m755 /tmp/oxidize-linux-amd64 /usr/local/bin/oxidize && \
		$(SUDO) install -m644 /tmp/oxidize.service /etc/systemd/system/oxidize.service && \
		$(SUDO) install -d -m700 /etc/oxidize /var/lib/oxidize && \
		([ -f /etc/oxidize/oxidize.env ] || $(SUDO) install -m600 /tmp/oxidize.env.example /etc/oxidize/oxidize.env) && \
		rm -f /tmp/oxidize-linux-amd64 /tmp/oxidize.service /tmp/oxidize.env.example && \
		$(SUDO) systemctl daemon-reload && $(SUDO) systemctl enable --now oxidize && $(SUDO) systemctl --no-pager is-active oxidize'

# Install/refresh Caddy (Tailscale HTTPS) on the VM.
caddy:
	$(SCP) deploy/Caddyfile $(DEPLOY_HOST):/tmp/Caddyfile
	$(SSH) $(DEPLOY_HOST) '$(SUDO) install -m644 /tmp/Caddyfile /etc/caddy/Caddyfile && rm -f /tmp/Caddyfile && ($(SUDO) systemctl reload caddy || $(SUDO) systemctl restart caddy) && $(SUDO) systemctl --no-pager is-active caddy'

clean:
	rm -rf bin internal/static/dist
