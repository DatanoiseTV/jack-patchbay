.PHONY: build clean install uninstall

BINARY = jack-patchbay
PREFIX ?= /usr/local
SYSTEMD_DIR ?= /etc/systemd/system

build:
	CGO_ENABLED=1 go build -o $(BINARY) -ldflags="-s -w" .

clean:
	rm -f $(BINARY)

install: build
	install -m 755 $(BINARY) $(PREFIX)/bin/$(BINARY)
	@echo "Installed to $(PREFIX)/bin/$(BINARY)"
	@echo "Run 'make install-service' to install the systemd service."

install-service:
	@echo "[Unit]" > $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "Description=JACK Patchbay Web UI" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "After=jackd.service" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "Wants=jackd.service" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "[Service]" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "Type=simple" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "User=jack" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "Group=audio" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "Environment=JACK_NO_AUDIO_RESERVATION=1" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "ExecStart=$(PREFIX)/bin/$(BINARY) -addr :8998" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "Restart=on-failure" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "RestartSec=3" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "[Install]" >> $(SYSTEMD_DIR)/jack-patchbay.service
	@echo "WantedBy=multi-user.target" >> $(SYSTEMD_DIR)/jack-patchbay.service
	systemctl daemon-reload
	systemctl enable jack-patchbay
	@echo "Service installed. Start with: systemctl start jack-patchbay"

uninstall:
	systemctl stop jack-patchbay 2>/dev/null || true
	systemctl disable jack-patchbay 2>/dev/null || true
	rm -f $(SYSTEMD_DIR)/jack-patchbay.service
	rm -f $(PREFIX)/bin/$(BINARY)
	systemctl daemon-reload
	@echo "Uninstalled."
