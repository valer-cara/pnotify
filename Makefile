BINARY     = notifier
BIN_DIR    = $(HOME)/.local/bin
CONFIG_DIR = $(HOME)/.config/pnotifier
UNIT_DIR   = $(HOME)/.config/systemd/user

.PHONY: build install uninstall

build:
	go build -ldflags="-s -w" -o $(BINARY) .

install: build
	install -Dm755 $(BINARY) $(BIN_DIR)/$(BINARY)
	@if [ ! -f $(CONFIG_DIR)/config.json ]; then \
		install -Dm644 config.json $(CONFIG_DIR)/config.json; \
	else \
		echo "$(CONFIG_DIR)/config.json already exists, skipping"; \
	fi
	install -Dm644 pnotifier.service $(UNIT_DIR)/pnotifier.service
	systemctl --user daemon-reload
	systemctl --user enable --now pnotifier.service

uninstall:
	systemctl --user disable --now pnotifier.service || true
	rm -f $(BIN_DIR)/$(BINARY)
	rm -f $(UNIT_DIR)/pnotifier.service
	systemctl --user daemon-reload
