# https://docs.docker.com/develop/develop-images/build_enhancements/
# https://www.docker.com/blog/faster-builds-in-compose-thanks-to-buildkit-support/
export DOCKER_BUILDKIT := 1
export DOCKER_SCAN_SUGGEST := false
export COMPOSE_DOCKER_CLI_BUILD := 1

ifndef DOCKER_HOST_IP
	ifeq ($(OS),Windows_NT)
		DOCKER_HOST_IP := $(shell powershell -noprofile -command '(Get-NetIPConfiguration | Where-Object {$$_.IPv4DefaultGateway -ne $$null -and $$_.NetAdapter.Status -ne "Disconnected"}).IPv4Address.IPAddress' )
	else
		UNAME_S := $(shell uname)
		ifeq ($(UNAME_S),Linux)
				DOCKER_HOST_IP := $(shell ip route get 1 | head -1 | awk '{print $$7}' )
		endif
		ifeq ($(UNAME_S),Darwin)
				DOCKER_HOST_IP := $(shell ifconfig | grep "inet " | grep -Fv 127.0.0.1 | awk '{print $$2}' )
		endif
	endif
endif

export DOCKER_HOST_IP

# check if we should use podman compose or docker compose
# no one should be using docker-compose anymore
ifeq (, $(shell which podman))
	DOCKER_COMMAND := docker
	DOCKER_SOCKET := /var/run/docker.sock
else
	DOCKER_COMMAND := podman
	DOCKER_SOCKET := $(XDG_RUNTIME_DIR)/podman/podman.sock
endif

export DOCKER_COMMAND
export DOCKER_SOCKET

start: build
	$(DOCKER_COMMAND) compose up --remove-orphans --force-recreate -d

up: build
	$(DOCKER_COMMAND) compose up --remove-orphans --force-recreate

down:
	$(DOCKER_COMMAND) compose down --remove-orphans

logs:
	$(DOCKER_COMMAND) compose logs -f

build: .env
	$(DOCKER_COMMAND) compose build 

.env:
	touch .env
	