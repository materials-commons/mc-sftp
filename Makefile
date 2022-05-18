.PHONY: bin all fmt deploy server

all: fmt bin

fmt:
	-go fmt ./...

bin: server

server:
	(cd ./cmd/mc-sshd; go build)

run: server
	./cmd/mc-sshd/mc-sshd

deploy: deploy-server

deploy-server: server
	@sudo supervisorctl stop mc-sshd:mc-sshd_00
	sudo cp cmd/mc-sshd/mc-sshd /usr/local/bin
	sudo chmod a+rx /usr/local/bin/mc-sshd
	sudo cp operations/supervisord.d/mc-sshd.ini /etc/supervisord.d
	@sudo supervisorctl start mc-sshd:mc-sshd_00
