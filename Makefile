.PHONY: bin all fmt deploy server

all: fmt bin

ssh-hostkey:
	@[ -f $(HOME)/.ssh/hostkey ] || ssh-keygen -t ed25519 -N '' -f $(HOME)/.ssh/hostkey

fmt:
	-go fmt ./...

bin: server

server:
	(cd ./cmd/mc-sshd; go build)

run: server
	./cmd/mc-sshd/mc-sshd

deploy: ssh-hostkey deploy-server

deploy-server: server
	sudo cp cmd/mc-sshd/mc-sshd /usr/local/bin
	sudo chmod a+rx /usr/local/bin/mc-sshd
	sudo cp operations/supervisord.d/mc-sshd.ini /etc/supervisord.d
	@sudo supervisorctl update all
