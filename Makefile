-include .env

JOYRIDE_PORT ?= 4709

run: .env build
	docker run --rm --name gossip-server -p $(JOYRIDE_PORT):$(JOYRIDE_PORT) --env-file .env gossip-server

.env:
	touch .env

build:
	docker build -t gossip-server .
	